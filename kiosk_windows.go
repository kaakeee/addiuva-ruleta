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

	monitorInfoPrimary = 1
	swRestore          = 9
	swpNoZorder        = 0x0004
	swpNoActivate      = 0x0010
	swpFrameChanged    = 0x0020
	swpShowWindow      = 0x0040
	vkF11              = 0x7A
	keyeventfKeyup     = 0x0002

	gwlStyle     = ^uintptr(15) // -16
	wsCaption    = 0x00C00000
	wsThickFrame = 0x00040000
	wsMinBox     = 0x00020000
	wsMaxBox     = 0x00010000
	wsSysMenu    = 0x00080000

	hdmiWindowTitlePrefix = "Addiuva · Pantalla HDMI Ruleta"
	kioskWindowTitleMatch = "Addiuva · Kiosco"
)

type HdmiDisplayInfo struct {
	Monitors     int   `json:"monitors"`
	HasSecondary bool  `json:"hasSecondary"`
	Width        int32 `json:"width"`
	Height       int32 `json:"height"`
}

type winRect struct {
	Left, Top, Right, Bottom int32
}

type winMonitorInfo struct {
	cbSize    uint32
	rcMonitor winRect
	rcWork    winRect
	dwFlags   uint32
}

type monitorRect struct {
	Left, Top, Right, Bottom int32
	Primary                  bool
}

func (m monitorRect) width() int32  { return m.Right - m.Left }
func (m monitorRect) height() int32 { return m.Bottom - m.Top }

var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procSetThreadExecState        = kernel32.NewProc("SetThreadExecutionState")
	procCreateMutexW              = kernel32.NewProc("CreateMutexW")
	procCloseHandle               = kernel32.NewProc("CloseHandle")

	user32                        = syscall.NewLazyDLL("user32.dll")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procEnumDisplayMonitors       = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW           = user32.NewProc("GetMonitorInfoW")
	procEnumWindows               = user32.NewProc("EnumWindows")
	procIsWindowVisible           = user32.NewProc("IsWindowVisible")
	procGetWindowTextW            = user32.NewProc("GetWindowTextW")
	procGetWindowRect             = user32.NewProc("GetWindowRect")
	procSetWindowPos              = user32.NewProc("SetWindowPos")
	procShowWindow                = user32.NewProc("ShowWindow")
	procSetForegroundWindow       = user32.NewProc("SetForegroundWindow")
	procKeybdEvent                = user32.NewProc("keybd_event")
	procGetWindowLongPtrW         = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW         = user32.NewProc("SetWindowLongPtrW")

	shcore                        = syscall.NewLazyDLL("shcore.dll")
	procSetProcessDpiAwareness    = shcore.NewProc("SetProcessDpiAwareness")

	winMu           sync.Mutex
	collectedMons   []monitorRect
	foundHwnd       uintptr
	findTitleNeedle string

	monEnumCb = syscall.NewCallback(func(hMon, hdc, lprc, lparam uintptr) uintptr {
		var mi winMonitorInfo
		mi.cbSize = uint32(unsafe.Sizeof(mi))
		ret, _, _ := procGetMonitorInfoW.Call(hMon, uintptr(unsafe.Pointer(&mi)))
		if ret != 0 {
			collectedMons = append(collectedMons, monitorRect{
				Left:    mi.rcMonitor.Left,
				Top:     mi.rcMonitor.Top,
				Right:   mi.rcMonitor.Right,
				Bottom:  mi.rcMonitor.Bottom,
				Primary: (mi.dwFlags & monitorInfoPrimary) != 0,
			})
		}
		return 1
	})

	winEnumCb = syscall.NewCallback(func(hwnd, lparam uintptr) uintptr {
		vis, _, _ := procIsWindowVisible.Call(hwnd)
		if vis == 0 {
			return 1
		}
		var buf [256]uint16
		n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n > 0 {
			title := syscall.UTF16ToString(buf[:n])
			if strings.Contains(title, findTitleNeedle) {
				foundHwnd = hwnd
				return 0
			}
		}
		return 1
	})
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

func listMonitors() []monitorRect {
	winMu.Lock()
	defer winMu.Unlock()
	collectedMons = nil
	_, _, _ = procEnumDisplayMonitors.Call(0, 0, monEnumCb, 0)
	out := make([]monitorRect, len(collectedMons))
	copy(out, collectedMons)
	return out
}

func pickHdmiMonitor() (monitorRect, HdmiDisplayInfo) {
	mons := listMonitors()
	if len(mons) == 0 {
		return monitorRect{Left: 0, Top: 0, Right: 1920, Bottom: 1080, Primary: true}, HdmiDisplayInfo{Monitors: 1, Width: 1920, Height: 1080}
	}
	primary := mons[0]
	for _, m := range mons {
		if m.Primary {
			primary = m
		}
	}
	for _, m := range mons {
		if !m.Primary {
			return m, HdmiDisplayInfo{
				Monitors:     len(mons),
				HasSecondary: true,
				Width:        m.width(),
				Height:       m.height(),
			}
		}
	}
	return primary, HdmiDisplayInfo{
		Monitors:     len(mons),
		HasSecondary: false,
		Width:        primary.width(),
		Height:       primary.height(),
	}
}

func getHdmiDisplayInfo() HdmiDisplayInfo {
	_, info := pickHdmiMonitor()
	return info
}

func findWindowByTitle(needle string) uintptr {
	winMu.Lock()
	defer winMu.Unlock()
	foundHwnd = 0
	findTitleNeedle = needle
	_, _, _ = procEnumWindows.Call(winEnumCb, 0)
	return foundHwnd
}

func getWindowRect(hwnd uintptr) (winRect, bool) {
	var r winRect
	ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r, ret != 0
}

func matchesMonitorRect(r winRect, m monitorRect) bool {
	tol := int32(16)
	abs := func(v int32) int32 {
		if v < 0 {
			return -v
		}
		return v
	}
	return abs(r.Left-m.Left) <= tol && abs(r.Top-m.Top) <= tol &&
		abs(r.Right-m.Right) <= tol && abs(r.Bottom-m.Bottom) <= tol
}

func placeHdmiHwndOnSecondary(hwnd uintptr, target monitorRect) {
	if r, ok := getWindowRect(hwnd); ok && matchesMonitorRect(r, target) {
		return
	}
	_, _, _ = procShowWindow.Call(hwnd, swRestore)

	// Paso 1: mover la ventana dentro del monitor HDMI secundario
	w := target.width() - 80
	if w > 1280 {
		w = 1280
	}
	if w < 640 {
		w = target.width()
	}
	h := target.height() - 80
	if h > 720 {
		h = 720
	}
	if h < 480 {
		h = target.height()
	}
	_, _, _ = procSetWindowPos.Call(hwnd, 0, uintptr(target.Left+40), uintptr(target.Top+40), uintptr(w), uintptr(h), swpNoZorder|swpShowWindow)
	time.Sleep(200 * time.Millisecond)

	// Paso 2: activar fullscreen nativo de Edge/Chrome (F11) si aún no cubre el monitor HDMI
	if r, ok := getWindowRect(hwnd); !ok || !matchesMonitorRect(r, target) {
		_, _, _ = procSetForegroundWindow.Call(hwnd)
		time.Sleep(100 * time.Millisecond)
		_, _, _ = procKeybdEvent.Call(vkF11, 0, 0, 0)
		_, _, _ = procKeybdEvent.Call(vkF11, 0, keyeventfKeyup, 0)
		time.Sleep(360 * time.Millisecond)
	}

	// Paso 3: respaldo borderless exacto (por si Edge ignoró F11 o hubo cambio de DPI entre 1920x1200 y 16:9)
	if r, ok := getWindowRect(hwnd); !ok || !matchesMonitorRect(r, target) {
		style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlStyle)
		if style != 0 {
			clean := style &^ uintptr(wsCaption|wsThickFrame|wsMinBox|wsMaxBox|wsSysMenu)
			_, _, _ = procSetWindowLongPtrW.Call(hwnd, gwlStyle, clean)
		}
		_, _, _ = procSetWindowPos.Call(hwnd, 0, uintptr(target.Left), uintptr(target.Top), uintptr(target.width()), uintptr(target.height()), swpFrameChanged|swpShowWindow|swpNoActivate)
		time.Sleep(140 * time.Millisecond)
		_, _, _ = procSetWindowPos.Call(hwnd, 0, uintptr(target.Left), uintptr(target.Top), uintptr(target.width()), uintptr(target.height()), swpFrameChanged|swpShowWindow|swpNoActivate)
	}

	// Devolver el foco al Kiosco de la notebook
	if kHwnd := findWindowByTitle(kioskWindowTitleMatch); kHwnd != 0 {
		_, _, _ = procSetForegroundWindow.Call(kHwnd)
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

func launchHdmiWindow(url string, profileDir string) HdmiDisplayInfo {
	target, info := pickHdmiMonitor()

	// Si ya hay una ventana HDMI abierta y se conectó la TV recién, moverla directo a la TV
	if existing := findWindowByTitle(hdmiWindowTitlePrefix); existing != 0 {
		go func(h uintptr, m monitorRect, sec bool) {
			if sec {
				placeHdmiHwndOnSecondary(h, m)
			} else {
				_, _, _ = procShowWindow.Call(h, swRestore)
				_, _, _ = procSetForegroundWindow.Call(h)
			}
		}(existing, target, info.HasSecondary)
		return info
	}

	hdmiProfile := profileDir + "-hdmi"
	// Limpiar Preferences guardadas para que Edge no restaure la ventana HDMI en el monitor de la notebook
	_ = os.Remove(filepath.Join(hdmiProfile, "Default", "Preferences"))

	exe := findBrowserExe()
	if exe != "" {
		args := []string{
			"--app=" + url,
			"--new-window",
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-pinch",
			"--disable-features=TranslateUI,EdgeSidebar",
			"--user-data-dir=" + hdmiProfile,
		}
		if info.HasSecondary {
			args = append(args,
				fmt.Sprintf("--window-position=%d,%d", target.Left+40, target.Top+40),
				"--window-size=1280,720",
			)
		} else {
			args = append(args, "--window-size=1280,720")
		}
		_ = exec.Command(exe, args...).Start()

		if info.HasSecondary {
			go func(m monitorRect) {
				deadline := time.Now().Add(8 * time.Second)
				for time.Now().Before(deadline) {
					if hwnd := findWindowByTitle(hdmiWindowTitlePrefix); hwnd != 0 {
						time.Sleep(280 * time.Millisecond)
						placeHdmiHwndOnSecondary(hwnd, m)
						return
					}
					time.Sleep(150 * time.Millisecond)
				}
			}(target)
		}
		return info
	}

	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	return info
}

