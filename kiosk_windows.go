//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	kioskWindowTitleMatch     = "Addiuva · Kiosco"
	explorerShellSupported    = true
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

	user32                        = syscall.NewLazyDLL("user32.dll")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procEnumWindows               = user32.NewProc("EnumWindows")
	procGetWindowTextW            = user32.NewProc("GetWindowTextW")
	procGetClassNameW             = user32.NewProc("GetClassNameW")
	procIsWindowVisible           = user32.NewProc("IsWindowVisible")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procGetWindowThreadProcessId  = user32.NewProc("GetWindowThreadProcessId")
	procSetForegroundWindow       = user32.NewProc("SetForegroundWindow")

	shcore                     = syscall.NewLazyDLL("shcore.dll")
	procSetProcessDpiAwareness = shcore.NewProc("SetProcessDpiAwareness")

	rstrtmgr              = syscall.NewLazyDLL("rstrtmgr.dll")
	procRmStartSession    = rstrtmgr.NewProc("RmStartSession")
	procRmRegisterRes     = rstrtmgr.NewProc("RmRegisterResources")
	procRmShutdown        = rstrtmgr.NewProc("RmShutdown")
	procRmEndSession      = rstrtmgr.NewProc("RmEndSession")

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

	explorerMu             sync.Mutex
	collectedTrayHwnds     []uintptr
	collectedShellHwnds    []uintptr
	collectedExplorerPids  []uint32
	collectedExplorerSeen  map[uint32]struct{}

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

func isExplorerProcess(pid uint32) bool {
	if pid == 0 {
		return false
	}
	hProc, _, _ := procOpenProcess.Call(processQueryInfo, 0, uintptr(pid))
	if hProc == 0 {
		return false
	}
	defer procCloseHandle.Call(hProc)

	var buf [512]uint16
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcImageW.Call(hProc, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 || size == 0 {
		return false
	}
	exe := syscall.UTF16ToString(buf[:size])
	return strings.EqualFold(filepath.Base(exe), "explorer.exe")
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

func isExplorerShellRunning() bool {
	trayHwnds, shellHwnds, _ := findExplorerShellWindows()
	return len(trayHwnds) > 0 || len(shellHwnds) > 0
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

// suspendExplorerShell cierra limpiamente explorer.exe (sin activar AutoRestartShell).
func suspendExplorerShell() bool {
	explorerActionMu.Lock()
	defer explorerActionMu.Unlock()

	trayHwnds, _, pids := findExplorerShellWindows()
	if len(trayHwnds) == 0 && len(pids) == 0 {
		return !isExplorerShellRunning()
	}

	// 1. Mensaje nativo WM_EXITEXPLORER (mismo que Ctrl+Shift+Clic derecho -> Salir del Explorador)
	for _, h := range trayHwnds {
		_, _, _ = procPostMessageW.Call(h, wmExitExplorer, 0, 0)
	}
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !isExplorerShellRunning() {
			return true
		}
		time.Sleep(60 * time.Millisecond)
	}

	// 2. Respaldo oficial vía Windows Restart Manager (cierre ordenado sin AutoRestartShell loop)
	_, _, pids = findExplorerShellWindows()
	if len(pids) > 0 && shutdownProcessesViaRestartManager(pids) {
		deadline2 := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline2) {
			if !isExplorerShellRunning() {
				return true
			}
			time.Sleep(80 * time.Millisecond)
		}
	}
	return !isExplorerShellRunning()
}

// restoreExplorerShell vuelve a iniciar explorer.exe solo si el shell no está activo,
// evitando abrir ventanas de carpetas duplicadas.
func restoreExplorerShell() bool {
	explorerActionMu.Lock()
	defer explorerActionMu.Unlock()

	if isExplorerShellRunning() {
		return true
	}

	winDir := strings.TrimSpace(os.Getenv("SystemRoot"))
	if winDir == "" {
		winDir = strings.TrimSpace(os.Getenv("WINDIR"))
	}
	if winDir == "" {
		winDir = `C:\Windows`
	}
	expExe := filepath.Join(winDir, "explorer.exe")
	if _, err := os.Stat(expExe); err != nil {
		expExe = "explorer.exe"
	}

	cmd := exec.Command(expExe)
	if err := cmd.Start(); err != nil {
		return false
	}
	go func() {
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(2000 * time.Millisecond)
	for time.Now().Before(deadline) {
		if isExplorerShellRunning() {
			return true
		}
		time.Sleep(80 * time.Millisecond)
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
		pids[uint32(kmd.Process.Pid)] = struct{}{}
	}

	// 1. Cierre ordenado por WM_CLOSE en la ventana Kiosco
	for _, h := range findWindowsByTitles(false, kioskWindowTitleMatch) {
		var pid uint32
		_, _, _ = procGetWindowThreadProcessId.Call(h, uintptr(unsafe.Pointer(&pid)))
		if pid != 0 && pid != uint32(os.Getpid()) {
			pids[pid] = struct{}{}
		}
		_, _, _ = procPostMessageW.Call(h, wmClose, 0, 0)
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
		if pid != 0 && pid != uint32(os.Getpid()) {
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
			var args []string
			if mode == launchModeApp {
				useProfile = profileDir + "-app"
				args = []string{
					"--app=" + url,
					"--start-fullscreen",
					"--no-first-run",
					"--no-default-browser-check",
					"--disable-background-mode",
					"--disable-pinch",
					"--overscroll-history-navigation=0",
					"--disable-features=TranslateUI,EdgeSidebar",
					"--user-data-dir=" + useProfile,
				}
			} else {
				args = []string{
					"--kiosk", url,
					"--edge-kiosk-type=fullscreen",
					"--no-first-run",
					"--no-default-browser-check",
					"--disable-background-mode",
					"--disable-pinch",
					"--overscroll-history-navigation=0",
					"--disable-infobars",
					"--disable-session-crashed-bubble",
					"--disable-features=TranslateUI,EdgeSidebar",
					"--user-data-dir=" + useProfile,
				}
			}
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

