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
	processTerminate = 0x0001

	kioskWindowTitleMatch = "Addiuva · Kiosco"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procSetThreadExecState = kernel32.NewProc("SetThreadExecutionState")
	procCreateMutexW       = kernel32.NewProc("CreateMutexW")
	procCloseHandle        = kernel32.NewProc("CloseHandle")
	procOpenProcess        = kernel32.NewProc("OpenProcess")
	procTerminateProcess   = kernel32.NewProc("TerminateProcess")

	user32                        = syscall.NewLazyDLL("user32.dll")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procEnumWindows               = user32.NewProc("EnumWindows")
	procGetWindowTextW            = user32.NewProc("GetWindowTextW")
	procIsWindowVisible           = user32.NewProc("IsWindowVisible")
	procPostMessageW              = user32.NewProc("PostMessageW")
	procGetWindowThreadProcessId  = user32.NewProc("GetWindowThreadProcessId")
	procSetForegroundWindow       = user32.NewProc("SetForegroundWindow")

	shcore                     = syscall.NewLazyDLL("shcore.dll")
	procSetProcessDpiAwareness = shcore.NewProc("SetProcessDpiAwareness")

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

