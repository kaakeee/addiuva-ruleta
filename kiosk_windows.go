//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
	esAwayMode        = 0x00000040

	wmClose          = 0x0010
	wmExitExplorer   = 0x05B4 // WM_USER + 436 ("Exit Explorer" limpio sin AutoRestartShell)
	processTerminate = 0x0001
	processQueryInfo = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION
	rmForceShutdown  = 0x0001

	createBreakawayFromJob = 0x01000000
	createDefaultErrorMode = 0x04000000

	kioskWindowTitleMatch  = "Addiuva · Kiosco"
	explorerShellSupported = true
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procSetThreadExecState   = kernel32.NewProc("SetThreadExecutionState")
	procCreateMutexW         = kernel32.NewProc("CreateMutexW")
	procCloseHandle          = kernel32.NewProc("CloseHandle")
	procOpenProcess          = kernel32.NewProc("OpenProcess")
	procTerminateProcess     = kernel32.NewProc("TerminateProcess")
	procQueryFullProcImageW  = kernel32.NewProc("QueryFullProcessImageNameW")
	procGetProcessTimes      = kernel32.NewProc("GetProcessTimes")
	procGetWindowsDirectoryW = kernel32.NewProc("GetWindowsDirectoryW")
	procProcessIdToSessionId = kernel32.NewProc("ProcessIdToSessionId")
	procGetCurrentProcessId  = kernel32.NewProc("GetCurrentProcessId")
	procWaitForSingleObject  = kernel32.NewProc("WaitForSingleObject")

	user32                        = syscall.NewLazyDLL("user32.dll")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procEnumWindows               = user32.NewProc("EnumWindows")
	procGetWindowTextW            = user32.NewProc("GetWindowTextW")
	procGetClassNameW             = user32.NewProc("GetClassNameW")
	procIsWindowVisible           = user32.NewProc("IsWindowVisible")
	procIsWindow                  = user32.NewProc("IsWindow")
	procFindWindowW               = user32.NewProc("FindWindowW")
	procGetShellWindow            = user32.NewProc("GetShellWindow")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procGetWindowThreadProcessId  = user32.NewProc("GetWindowThreadProcessId")
	procSetForegroundWindow       = user32.NewProc("SetForegroundWindow")

	shell32           = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = shell32.NewProc("ShellExecuteW")

	ole32        = syscall.NewLazyDLL("ole32.dll")
	procCoInitEx = ole32.NewProc("CoInitializeEx")
	procCoUninit = ole32.NewProc("CoUninitialize")

	shcore                     = syscall.NewLazyDLL("shcore.dll")
	procSetProcessDpiAwareness = shcore.NewProc("SetProcessDpiAwareness")

	rstrtmgr           = syscall.NewLazyDLL("rstrtmgr.dll")
	procRmStartSession = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterRes  = rstrtmgr.NewProc("RmRegisterResources")
	procRmShutdown     = rstrtmgr.NewProc("RmShutdown")
	procRmEndSession   = rstrtmgr.NewProc("RmEndSession")

	winMu              sync.Mutex
	collectedHwnds     []uintptr
	findTitleNeedles   []string
	requireVisibleHwnd bool

	winCollectEnumCb = syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
		if requireVisibleHwnd {
			vis, _, _ := procIsWindowVisible.Call(hwnd)
			if vis == 0 {
				return 1
			}
		}
		var buf [256]uint16
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 {
			title := syscall.UTF16ToString(buf[:n])
			for _, needle := range findTitleNeedles {
				if needle != "" && strings.Contains(title, needle) {
					collectedHwnds = append(collectedHwnds, hwnd)
					break
				}
			}
		}
		return 1
	})

	explorerMu            sync.Mutex
	collectedTrayHwnds    []uintptr
	collectedShellHwnds   []uintptr
	collectedExplorerPids []uint32
	collectedExplorerSeen map[uint32]struct{}

	explorerEnumCb = syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
		var clsBuf [128]uint16
		n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&clsBuf[0])), uintptr(len(clsBuf)))
		if n == 0 {
			return 1
		}
		cls := syscall.UTF16ToString(clsBuf[:n])
		if cls != "Shell_TrayWnd" && cls != "Shell_SecondaryTrayWnd" && cls != "Progman" {
			return 1
		}
		var pid uint32
		_, _, _ = procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid == 0 || !isExplorerProcess(pid) {
			return 1
		}
		collectedShellHwnds = append(collectedShellHwnds, hwnd)
		if cls == "Shell_TrayWnd" {
			collectedTrayHwnds = append(collectedTrayHwnds, hwnd)
		}
		if _, ok := collectedExplorerSeen[pid]; !ok {
			collectedExplorerSeen[pid] = struct{}{}
			collectedExplorerPids = append(collectedExplorerPids, pid)
		}
		return 1
	})
)

type launchMode int

const (
	launchModeKiosk launchMode = iota
	launchModeApp
	launchModeFallback
)

func initDpiAwareness() {
	if procSetProcessDpiAwarenessCtx.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = (HANDLE)-4
		_, _, _ = procSetProcessDpiAwarenessCtx.Call(^uintptr(3))
		return
	}
	if procSetProcessDpiAwareness.Find() == nil {
		_, _, _ = procSetProcessDpiAwareness.Call(2)
	}
}

func acquireSingleInstance() bool {
	namePtr, err := syscall.UTF16PtrFromString("Local\\AddiuvaRuletaSingleInstance")
	if err != nil {
		return true
	}
	h, _, lastErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h != 0 && lastErr == syscall.Errno(183) { // ERROR_ALREADY_EXISTS
		_, _, _ = procCloseHandle.Call(h)
		if hwnds := findWindowsByTitles(true, kioskWindowTitleMatch); len(hwnds) > 0 {
			_, _, _ = procSetForegroundWindow.Call(hwnds[0])
		}
		return false
	}
	return true
}

// keepSystemAwake mantiene la pantalla y la PC de Windows encendidas sin suspenderse.
func keepSystemAwake(stop <-chan struct{}) {
	setAwake := func() {
		procSetThreadExecState.Call(uintptr(esContinuous | esSystemRequired | esDisplayRequired | esAwayMode))
	}
	setAwake()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			setAwake()
		case <-stop:
			procSetThreadExecState.Call(uintptr(esContinuous))
			return
		}
	}
}

func findBrowserExe() string {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("LocalAppData"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
	}
	for _, c := range candidates {
		if c != "" {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c
			}
		}
	}
	if p, err := exec.LookPath("msedge.exe"); err == nil {
		return p
	}
	if p, err := exec.LookPath("chrome.exe"); err == nil {
		return p
	}
	return ""
}

func findWindowsByTitles(onlyVisible bool, needles ...string) []uintptr {
	winMu.Lock()
	defer winMu.Unlock()
	collectedHwnds = nil
	findTitleNeedles = needles
	requireVisibleHwnd = onlyVisible
	_, _, _ = procEnumWindows.Call(winCollectEnumCb, 0)
	out := make([]uintptr, len(collectedHwnds))
	copy(out, collectedHwnds)
	return out
}

func hasVisibleKioskWindow() bool {
	return len(findWindowsByTitles(true, kioskWindowTitleMatch)) > 0
}

type rmUniqueProcess struct {
	ProcessID        uint32
	ProcessStartTime syscall.Filetime
}

var explorerActionMu sync.Mutex

func isProcessAlive(pid uint32) bool {
	if pid == 0 {
		return false
	}
	hProc, _, _ := procOpenProcess.Call(uintptr(syscall.SYNCHRONIZE|processQueryInfo), 0, uintptr(pid))
	if hProc == 0 {
		hProc, _, _ = procOpenProcess.Call(processQueryInfo, 0, uintptr(pid))
		if hProc == 0 {
			return false
		}
		_, _, _ = procCloseHandle.Call(hProc)
		return true
	}
	defer procCloseHandle.Call(hProc)
	waitRes, _, _ := procWaitForSingleObject.Call(hProc, 0)
	return waitRes == syscall.WAIT_TIMEOUT
}

func anyProcessAlive(pids []uint32) bool {
	for _, pid := range pids {
		if isProcessAlive(pid) {
			return true
		}
	}
	return false
}

func isExplorerProcess(pid uint32) bool {
	if pid == 0 {
		return false
	}
	hasSync := true
	hProc, _, _ := procOpenProcess.Call(uintptr(syscall.SYNCHRONIZE|processQueryInfo), 0, uintptr(pid))
	if hProc == 0 {
		hasSync = false
		hProc, _, _ = procOpenProcess.Call(processQueryInfo, 0, uintptr(pid))
		if hProc == 0 {
			return false
		}
	}
	defer procCloseHandle.Call(hProc)

	if hasSync {
		waitRes, _, _ := procWaitForSingleObject.Call(hProc, 0)
		if waitRes != syscall.WAIT_TIMEOUT {
			return false
		}
	}

	var buf [512]uint16
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcImageW.Call(hProc, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 || size == 0 {
		return false
	}
	exe := syscall.UTF16ToString(buf[:size])
	return strings.EqualFold(filepath.Base(exe), "explorer.exe")
}

func isWindowVisible(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	vis, _, _ := procIsWindowVisible.Call(hwnd)
	return vis != 0
}

func isWindowOwnedByExplorer(hwnd uintptr) (uint32, bool) {
	if hwnd == 0 {
		return 0, false
	}
	if procIsWindow.Find() == nil {
		valid, _, _ := procIsWindow.Call(hwnd)
		if valid == 0 {
			return 0, false
		}
	}
	var pid uint32
	_, _, _ = procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 || !isExplorerProcess(pid) {
		return 0, false
	}
	return pid, true
}

func findExplorerShellWindows() (trayHwnds []uintptr, shellHwnds []uintptr, pids []uint32) {
	explorerMu.Lock()
	defer explorerMu.Unlock()
	collectedTrayHwnds = nil
	collectedShellHwnds = nil
	collectedExplorerPids = nil
	collectedExplorerSeen = make(map[uint32]struct{})
	_, _, _ = procEnumWindows.Call(explorerEnumCb, 0)

	trayHwnds = append([]uintptr(nil), collectedTrayHwnds...)
	shellHwnds = append([]uintptr(nil), collectedShellHwnds...)
	pids = append([]uint32(nil), collectedExplorerPids...)
	return trayHwnds, shellHwnds, pids
}

func findDesktopShellState() (primaryTrayHwnd uintptr, desktopShellHwnd uintptr, trayHwnds []uintptr, surfaceHwnds []uintptr, shellPids []uint32) {
	trayHwnds, surfaceHwnds, enumPids := findExplorerShellWindows()
	seenPids := make(map[uint32]struct{}, len(enumPids)+2)
	addPid := func(pid uint32) {
		if pid == 0 {
			return
		}
		if _, ok := seenPids[pid]; !ok {
			seenPids[pid] = struct{}{}
			shellPids = append(shellPids, pid)
		}
	}
	for _, pid := range enumPids {
		addPid(pid)
	}

	if trayClassPtr, err := syscall.UTF16PtrFromString("Shell_TrayWnd"); err == nil {
		tHwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(trayClassPtr)), 0)
		if tPid, ok := isWindowOwnedByExplorer(tHwnd); ok {
			primaryTrayHwnd = tHwnd
			addPid(tPid)
		}
	}
	if primaryTrayHwnd == 0 && len(trayHwnds) > 0 {
		for _, h := range trayHwnds {
			if _, ok := isWindowOwnedByExplorer(h); ok {
				primaryTrayHwnd = h
				break
			}
		}
	}

	sHwnd, _, _ := procGetShellWindow.Call()
	if sPid, ok := isWindowOwnedByExplorer(sHwnd); ok {
		desktopShellHwnd = sHwnd
		addPid(sPid)
	}

	return primaryTrayHwnd, desktopShellHwnd, trayHwnds, surfaceHwnds, shellPids
}

// isExplorerShellRunning verifica que la barra de tareas principal (Shell_TrayWnd)
// y el escritorio registrado (GetShellWindow) o la barra visible estén realmente activos
// bajo un proceso explorer.exe vivo (evita falsos positivos con Progman huérfano).
func isExplorerShellRunning() bool {
	trayHwnd, shellHwnd, _, _, _ := findDesktopShellState()
	if trayHwnd == 0 {
		return false
	}
	return shellHwnd != 0 || isWindowVisible(trayHwnd)
}

func isExplorerDesktopReady() bool {
	trayHwnd, shellHwnd, _, _, _ := findDesktopShellState()
	return trayHwnd != 0 && shellHwnd != 0
}

func hasShellSurfaceOrPidsAlive(trackedPids []uint32) bool {
	trayHwnd, shellHwnd, _, surfaceHwnds, currentPids := findDesktopShellState()
	if trayHwnd != 0 || shellHwnd != 0 || len(surfaceHwnds) > 0 || len(currentPids) > 0 {
		return true
	}
	return anyProcessAlive(trackedPids)
}

func listSessionExplorerPids() []uint32 {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer syscall.CloseHandle(snap)

	myPID := uint32(os.Getpid())
	if cur, _, _ := procGetCurrentProcessId.Call(); cur != 0 {
		myPID = uint32(cur)
	}
	var mySession uint32
	hasMySession := false
	if ok, _, _ := procProcessIdToSessionId.Call(uintptr(myPID), uintptr(unsafe.Pointer(&mySession))); ok != 0 {
		hasMySession = true
	}

	var pe syscall.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := syscall.Process32First(snap, &pe); err != nil {
		return nil
	}

	var out []uint32
	for {
		exeName := syscall.UTF16ToString(pe.ExeFile[:])
		if strings.EqualFold(exeName, "explorer.exe") && pe.ProcessID != 0 && pe.ProcessID != myPID {
			if isExplorerProcess(pe.ProcessID) {
				sameSession := true
				if hasMySession {
					var pidSession uint32
					if ok, _, _ := procProcessIdToSessionId.Call(uintptr(pe.ProcessID), uintptr(unsafe.Pointer(&pidSession))); ok != 0 && pidSession != mySession {
						sameSession = false
					}
				}
				if sameSession {
					out = append(out, pe.ProcessID)
				}
			}
		}
		if err := syscall.Process32Next(snap, &pe); err != nil {
			break
		}
	}
	return out
}

func shutdownProcessesViaRestartManager(pids []uint32) bool {
	if len(pids) == 0 ||
		procRmStartSession.Find() != nil ||
		procRmRegisterRes.Find() != nil ||
		procRmShutdown.Find() != nil ||
		procRmEndSession.Find() != nil {
		return false
	}

	var rmProcs []rmUniqueProcess
	for _, pid := range pids {
		hProc, _, _ := procOpenProcess.Call(processQueryInfo, 0, uintptr(pid))
		if hProc == 0 {
			continue
		}
		var ct, et, kt, ut syscall.Filetime
		ok, _, _ := procGetProcessTimes.Call(
			hProc,
			uintptr(unsafe.Pointer(&ct)),
			uintptr(unsafe.Pointer(&et)),
			uintptr(unsafe.Pointer(&kt)),
			uintptr(unsafe.Pointer(&ut)),
		)
		_, _, _ = procCloseHandle.Call(hProc)
		if ok != 0 {
			rmProcs = append(rmProcs, rmUniqueProcess{
				ProcessID:        pid,
				ProcessStartTime: ct,
			})
		}
	}
	if len(rmProcs) == 0 {
		return false
	}

	var session uint32
	var sessionKey [33]uint16
	r1, _, _ := procRmStartSession.Call(uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&sessionKey[0])))
	if r1 != 0 {
		return false
	}
	defer procRmEndSession.Call(uintptr(session))

	r2, _, _ := procRmRegisterRes.Call(
		uintptr(session),
		0, 0,
		uintptr(len(rmProcs)), uintptr(unsafe.Pointer(&rmProcs[0])),
		0, 0,
	)
	if r2 != 0 {
		return false
	}
	r3, _, _ := procRmShutdown.Call(uintptr(session), uintptr(rmForceShutdown), 0)
	return r3 == 0
}

func terminateExplorerPidsForced(pids []uint32) {
	myPID := uint32(os.Getpid())
	for _, pid := range pids {
		if pid == 0 || pid == myPID {
			continue
		}
		if hProc, _, _ := procOpenProcess.Call(processTerminate, 0, uintptr(pid)); hProc != 0 {
			_, _, _ = procTerminateProcess.Call(hProc, 1)
			_, _, _ = procCloseHandle.Call(hProc)
		}
	}
}

func resolveWindowsExplorerPaths() (winDir string, expExe string) {
	if procGetWindowsDirectoryW.Find() == nil {
		var buf [260]uint16
		n, _, _ := procGetWindowsDirectoryW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 && int(n) < len(buf) {
			winDir = strings.TrimSpace(syscall.UTF16ToString(buf[:n]))
		}
	}
	if winDir == "" {
		winDir = strings.TrimSpace(os.Getenv("SystemRoot"))
	}
	if winDir == "" {
		winDir = strings.TrimSpace(os.Getenv("WINDIR"))
	}
	if winDir == "" {
		winDir = `C:\Windows`
	}
	candidate := filepath.Join(winDir, "explorer.exe")
	if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
		return winDir, candidate
	}
	if looked, err := exec.LookPath("explorer.exe"); err == nil {
		return filepath.Dir(looked), looked
	}
	return winDir, candidate
}

func launchExplorerDirect(expExe, winDir string) (uint32, bool) {
	appPtr, err := syscall.UTF16PtrFromString(expExe)
	if err != nil {
		return 0, false
	}
	dirPtr, _ := syscall.UTF16PtrFromString(winDir)

	var si syscall.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = syscall.STARTF_USESHOWWINDOW
	si.ShowWindow = syscall.SW_SHOWNORMAL

	attempt := func(flags uint32) (uint32, error) {
		cmdBuf, err := syscall.UTF16FromString(`"` + expExe + `"`)
		if err != nil {
			return 0, err
		}
		var pi syscall.ProcessInformation
		callErr := syscall.CreateProcess(appPtr, &cmdBuf[0], nil, nil, false, flags, nil, dirPtr, &si, &pi)
		runtime.KeepAlive(cmdBuf)
		if callErr != nil {
			return 0, callErr
		}
		pid := pi.ProcessId
		if pi.Thread != 0 {
			_ = syscall.CloseHandle(pi.Thread)
		}
		if pi.Process != 0 {
			_ = syscall.CloseHandle(pi.Process)
		}
		return pid, nil
	}

	flags := uint32(syscall.CREATE_NEW_PROCESS_GROUP | createDefaultErrorMode | createBreakawayFromJob)
	pid, err := attempt(flags)
	if err != nil {
		// Reintentar sin CREATE_BREAKAWAY_FROM_JOB si el Job Object padre no permite breakaway
		fallbackFlags := uint32(syscall.CREATE_NEW_PROCESS_GROUP | createDefaultErrorMode)
		pid, err = attempt(fallbackFlags)
	}
	runtime.KeepAlive(appPtr)
	runtime.KeepAlive(dirPtr)
	if err != nil {
		return 0, false
	}
	return pid, true
}

func launchExplorerShellExecute(expExe, winDir string) bool {
	if procShellExecuteW.Find() != nil {
		return false
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	coInitSucceeded := false
	if procCoInitEx.Find() == nil {
		// COINIT_APARTMENTTHREADED (0x2) | COINIT_DISABLE_OLE1DDE (0x4)
		hr, _, _ := procCoInitEx.Call(0, 0x6)
		if hr == 0 || hr == 1 {
			coInitSucceeded = true
		}
	}
	if coInitSucceeded && procCoUninit.Find() == nil {
		defer procCoUninit.Call()
	}

	verbPtr, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return false
	}
	filePtr, err := syscall.UTF16PtrFromString(expExe)
	if err != nil {
		return false
	}
	dirPtr, _ := syscall.UTF16PtrFromString(winDir)
	ret, _, _ := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verbPtr)),
		uintptr(unsafe.Pointer(filePtr)),
		0, // nil lpParameters para iniciar shell de escritorio sin abrir carpetas
		uintptr(unsafe.Pointer(dirPtr)),
		uintptr(syscall.SW_SHOWNORMAL),
	)
	runtime.KeepAlive(verbPtr)
	runtime.KeepAlive(filePtr)
	runtime.KeepAlive(dirPtr)
	return ret > 32
}

func launchExplorerViaCmdStart(expExe, winDir string) bool {
	cmdExe := filepath.Join(winDir, "System32", "cmd.exe")
	if st, err := os.Stat(cmdExe); err != nil || st.IsDir() {
		cmdExe = "cmd.exe"
	}
	cmd := exec.Command(cmdExe, "/c", "start", "", "/D", winDir, expExe)
	cmd.Dir = winDir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:       true,
		NoInheritHandles: true,
		CreationFlags:    syscall.CREATE_NEW_PROCESS_GROUP | createBreakawayFromJob | createDefaultErrorMode,
	}
	if err := cmd.Start(); err != nil {
		cmd = exec.Command(cmdExe, "/c", "start", "", "/D", winDir, expExe)
		cmd.Dir = winDir
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:       true,
			NoInheritHandles: true,
			CreationFlags:    syscall.CREATE_NEW_PROCESS_GROUP | createDefaultErrorMode,
		}
		if err := cmd.Start(); err != nil {
			return false
		}
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return true
}

func waitForExplorerReady(timeout time.Duration, launchedPid uint32) bool {
	deadline := time.Now().Add(timeout)
	var trayOnlySince time.Time
	var deadSince time.Time
	for {
		if isExplorerDesktopReady() {
			return true
		}
		if isExplorerShellRunning() {
			if trayOnlySince.IsZero() {
				trayOnlySince = time.Now()
			} else if time.Since(trayOnlySince) >= 450*time.Millisecond {
				return true
			}
			deadSince = time.Time{}
		} else {
			trayOnlySince = time.Time{}
			if launchedPid != 0 && !isProcessAlive(launchedPid) {
				if deadSince.IsZero() {
					deadSince = time.Now()
				} else if time.Since(deadSince) >= 400*time.Millisecond {
					return false
				}
			} else {
				deadSince = time.Time{}
			}
		}
		if !time.Now().Before(deadline) {
			return isExplorerShellRunning()
		}
		time.Sleep(90 * time.Millisecond)
	}
}

func waitPidsExit(pids []uint32, timeout time.Duration) bool {
	if len(pids) == 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	for {
		if !anyProcessAlive(pids) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(60 * time.Millisecond)
	}
}

// suspendExplorerShell cierra limpiamente explorer.exe (sin activar AutoRestartShell).
func suspendExplorerShell() bool {
	explorerActionMu.Lock()
	defer explorerActionMu.Unlock()

	primaryTray, _, trayHwnds, surfaceHwnds, pids := findDesktopShellState()
	if primaryTray == 0 && len(surfaceHwnds) == 0 && len(pids) == 0 {
		return !isExplorerShellRunning()
	}

	signaledTray := make(map[uintptr]struct{}, len(trayHwnds)+1)
	if primaryTray != 0 {
		signaledTray[primaryTray] = struct{}{}
	}
	for _, h := range trayHwnds {
		if h != 0 {
			signaledTray[h] = struct{}{}
		}
	}

	// 1. Mensaje nativo WM_EXITEXPLORER (mismo que Ctrl+Shift+Clic derecho -> Salir del Explorador)
	for h := range signaledTray {
		_, _, _ = procPostMessageW.Call(h, wmExitExplorer, 0, 0)
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !hasShellSurfaceOrPidsAlive(pids) {
			return true
		}
		time.Sleep(60 * time.Millisecond)
	}

	// 2. Respaldo oficial vía Windows Restart Manager (cierre ordenado sin AutoRestartShell loop)
	_, _, _, _, remPids := findDesktopShellState()
	if len(remPids) == 0 {
		for _, pid := range pids {
			if isProcessAlive(pid) {
				remPids = append(remPids, pid)
			}
		}
	}
	if len(remPids) > 0 && shutdownProcessesViaRestartManager(remPids) {
		deadline2 := time.Now().Add(1800 * time.Millisecond)
		for time.Now().Before(deadline2) {
			if !hasShellSurfaceOrPidsAlive(remPids) {
				return true
			}
			time.Sleep(75 * time.Millisecond)
		}
	}
	return !isExplorerShellRunning()
}

// restoreExplorerShell vuelve a iniciar explorer.exe en etapas progresivas y desacopladas
// hasta que tanto la barra de tareas como el escritorio queden activos en la sesión.
func restoreExplorerShell() bool {
	explorerActionMu.Lock()
	defer explorerActionMu.Unlock()

	if isExplorerShellRunning() {
		return true
	}

	winDir, expExe := resolveWindowsExplorerPaths()

	// Etapa 1: CreateProcess directo con StartupInfo SW_SHOWNORMAL y breakaway de Job Object
	if pid, ok := launchExplorerDirect(expExe, winDir); ok {
		if waitForExplorerReady(2000*time.Millisecond, pid) {
			return true
		}
	}

	// Etapa 2: ShellExecuteW ("open", "C:\Windows\explorer.exe", nil, "C:\Windows", SW_SHOWNORMAL)
	if launchExplorerShellExecute(expExe, winDir) {
		if waitForExplorerReady(2000*time.Millisecond, 0) {
			return true
		}
	}

	// Etapa 3: Si el shell sigue caído y quedaron procesos explorer.exe huérfanos en la misma
	// sesión interceptando el arranque, limpiar esos procesos bloqueantes y relanzar.
	if !isExplorerShellRunning() {
		if orphanPids := listSessionExplorerPids(); len(orphanPids) > 0 {
			_ = shutdownProcessesViaRestartManager(orphanPids)
			if !waitPidsExit(orphanPids, 800*time.Millisecond) {
				terminateExplorerPidsForced(orphanPids)
				_ = waitPidsExit(orphanPids, 800*time.Millisecond)
			}
		}
		pid, launched := launchExplorerDirect(expExe, winDir)
		if !launched {
			launched = launchExplorerShellExecute(expExe, winDir)
			pid = 0
		}
		if launched && waitForExplorerReady(2800*time.Millisecond, pid) {
			return true
		}
	}

	// Etapa 4: Último respaldo vía cmd.exe /c start "" /D C:\Windows C:\Windows\explorer.exe
	if !isExplorerShellRunning() && launchExplorerViaCmdStart(expExe, winDir) {
		if waitForExplorerReady(2200*time.Millisecond, 0) {
			return true
		}
	}

	return isExplorerShellRunning()
}

func killProcessTreeWindows(pid uint32) {
	if pid == 0 || pid == uint32(os.Getpid()) {
		return
	}
	tk := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid))
	tk.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = tk.Run()
	if hProc, _, _ := procOpenProcess.Call(processTerminate, 0, uintptr(pid)); hProc != 0 {
		_, _, _ = procTerminateProcess.Call(hProc, 0)
		_, _, _ = procCloseHandle.Call(hProc)
	}
}

func prepareProfileDir(profileDir string) {
	if profileDir == "" {
		return
	}
	_ = os.MkdirAll(profileDir, 0755)
	// Evitar asistente/reinicio de First Run en Edge y Chrome
	firstRunPath := filepath.Join(profileDir, "First Run")
	if _, err := os.Stat(firstRunPath); err != nil {
		_ = os.WriteFile(firstRunPath, []byte{}, 0644)
	}
	// Desactivar diálogo de traducción automática desde el primer inicio
	_ = ensureNoTranslateBrowserPrefs(profileDir)
}

func cleanupStaleProfileDirs() {
	tmp := os.TempDir()
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "addiuva-ruleta-kiosk-") {
			continue
		}
		dir := filepath.Join(tmp, name)
		// Si un navegador todavía mantiene bloqueado Lockfile en Windows, no tocar ese perfil
		lockFile := filepath.Join(dir, "Lockfile")
		if _, err := os.Stat(lockFile); err == nil {
			if rmErr := os.Remove(lockFile); rmErr != nil {
				continue
			}
		}
		_ = os.RemoveAll(dir)
	}
}

func closeAppWindows(kmd *exec.Cmd, profileDir string) {
	pids := make(map[uint32]struct{})
	if kmd != nil && kmd.Process != nil && kmd.Process.Pid > 0 {
		pid := uint32(kmd.Process.Pid)
		if !isExplorerProcess(pid) {
			pids[pid] = struct{}{}
		}
	}

	// 1. Cierre ordenado por WM_CLOSE en la ventana Kiosco
	for _, h := range findWindowsByTitles(false, kioskWindowTitleMatch) {
		var pid uint32
		_, _, _ = procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 && pid != uint32(os.Getpid()) && !isExplorerProcess(pid) {
			pids[pid] = struct{}{}
			_, _, _ = procPostMessageW.Call(h, wmClose, 0, 0)
		}
	}

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if len(findWindowsByTitles(false, kioskWindowTitleMatch)) == 0 {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}

	// 2. Forzar cierre del árbol de procesos si la ventana (ej. Edge --kiosk) ignoró WM_CLOSE
	for _, h := range findWindowsByTitles(false, kioskWindowTitleMatch) {
		var pid uint32
		_, _, _ = procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 && pid != uint32(os.Getpid()) && !isExplorerProcess(pid) {
			pids[pid] = struct{}{}
		}
	}

	for pid := range pids {
		killProcessTreeWindows(pid)
	}
	if kmd != nil && kmd.Process != nil {
		_ = kmd.Process.Kill()
	}

	if profileDir != "" {
		time.Sleep(150 * time.Millisecond)
		_ = os.RemoveAll(profileDir)
		_ = os.RemoveAll(profileDir + "-app")
	}
}

func launchKioskBrowser(mode launchMode, url string, profileDir string) *exec.Cmd {
	if mode != launchModeFallback {
		if exe := findBrowserExe(); exe != "" {
			useProfile := profileDir
			if mode == launchModeApp {
				useProfile = profileDir + "-app"
			}
			args := buildBrowserLaunchArgs(mode, url, useProfile)
			prepareProfileDir(useProfile)
			cmd := exec.Command(exe, args...)
			if err := cmd.Start(); err == nil {
				return cmd
			}
		}
	}
	// Fallback al navegador predeterminado si no se encontró ruta directa o fallaron --kiosk / --app
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	_ = cmd.Start()
	return nil
}
