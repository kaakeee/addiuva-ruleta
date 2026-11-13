//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

const (
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
	esAwayMode        = 0x00000040
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procSetThreadExecState = kernel32.NewProc("SetThreadExecutionState")
	procCreateMutexW       = kernel32.NewProc("CreateMutexW")
	procCloseHandle        = kernel32.NewProc("CloseHandle")
)

func acquireSingleInstance() bool {
	namePtr, err := syscall.UTF16PtrFromString("Local\\AddiuvaRuletaSingleInstance")
	if err != nil {
		return true
	}
	h, _, lastErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if h != 0 && lastErr == syscall.Errno(183) { // ERROR_ALREADY_EXISTS
		_, _, _ = procCloseHandle.Call(h)
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

func launchKioskBrowser(url string, profileDir string) *exec.Cmd {
	exe := findBrowserExe()
	if exe != "" {
		args := []string{
			"--kiosk", url,
			"--edge-kiosk-type=fullscreen",
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-pinch",
			"--overscroll-history-navigation=0",
			"--disable-infobars",
			"--disable-session-crashed-bubble",
			"--disable-features=TranslateUI,EdgeSidebar",
			"--user-data-dir=" + profileDir,
		}
		cmd := exec.Command(exe, args...)
		if err := cmd.Start(); err == nil {
			return cmd
		}
	}
	// Fallback al navegador predeterminado si no se encontró ruta directa
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	_ = cmd.Start()
	return nil
}

func launchHdmiWindow(url string, profileDir string) {
	exe := findBrowserExe()
	if exe != "" {
		args := []string{
			"--app=" + url,
			"--new-window",
			"--no-first-run",
			"--disable-pinch",
			"--user-data-dir=" + profileDir + "-hdmi",
		}
		_ = exec.Command(exe, args...).Start()
		return
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
