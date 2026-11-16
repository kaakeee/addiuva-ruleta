package main

import (
	"bytes"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed prototipo
var embeddedFiles embed.FS

type Employee struct {
	Dni     string  `json:"dni"`
	Nombre  string  `json:"nombre"`
	Numero  int     `json:"numero"`
	Checkin string  `json:"checkin"`
	Premio  *string `json:"premio"`
	Ausente bool    `json:"ausente"`
}

type RosterItem struct {
	Dni    string `json:"dni"`
	Nombre string `json:"nombre"`
}

type WinnerInfo struct {
	Dni    string `json:"dni"`
	Nombre string `json:"nombre"`
	Numero int    `json:"numero"`
	At     string `json:"at"`
}

type Prize struct {
	ID      string      `json:"id"`
	Nombre  string      `json:"nombre"`
	Ganador *WinnerInfo `json:"ganador"`
}

type DrawInfo struct {
	ID     string `json:"id"`
	Round  int    `json:"round"`
	Dni    string `json:"dni"`
	Nombre string `json:"nombre"`
	Numero int    `json:"numero"`
	At     string `json:"at"`
}

type AppConfig struct {
	AdminPass     string `json:"adminPass"`
	RemoveWinners bool   `json:"removeWinners"`
	ShowNames     bool   `json:"showNames"`
	RosterOnly    bool   `json:"rosterOnly"`
	Sound         bool   `json:"sound"`
	LockExplorer  *bool  `json:"lockExplorer,omitempty"`
}

func boolPtr(v bool) *bool {
	return &v
}

func (c AppConfig) shouldLockExplorer() bool {
	return c.LockExplorer == nil || *c.LockExplorer
}

type AppState struct {
	Employees []Employee   `json:"employees"`
	Roster    []RosterItem `json:"roster"`
	Prizes    []Prize      `json:"prizes"`
	Draws     []DrawInfo   `json:"draws"`
	Current   int          `json:"current"`
	Seq       int          `json:"seq"`
	Dupes     int          `json:"dupes"`
	Config    AppConfig    `json:"config"`
}

type LiveEvent struct {
	ID           int64   `json:"id"`
	Ts           int64   `json:"ts,omitempty"`
	Action       string  `json:"action"`
	Dni          string  `json:"dni,omitempty"`
	StartRot     float64 `json:"startRot,omitempty"`
	EndRot       float64 `json:"endRot,omitempty"`
	Duration     float64 `json:"duration,omitempty"`
	PrizeID      string  `json:"prizeId,omitempty"`
	StateVersion int64   `json:"stateVersion,omitempty"`
}

type shellStatusPayload struct {
	Supported       bool `json:"supported"`
	LockExplorer    bool `json:"lockExplorer"`
	ExplorerRunning bool `json:"explorerRunning"`
	ClosedByApp     bool `json:"closedByApp"`
}

type shellController struct {
	supported         bool
	isExplorerRunning func() bool
	suspendExplorer   func() bool
	restoreExplorer   func() bool
	opMu              sync.Mutex
	mu                sync.Mutex
	closedByApp       bool
}

func newDefaultShellController() *shellController {
	return &shellController{
		supported:         explorerShellSupported,
		isExplorerRunning: isExplorerShellRunning,
		suspendExplorer:   suspendExplorerShell,
		restoreExplorer:   restoreExplorerShell,
	}
}

func (s *shellController) status(lockPref bool) shellStatusPayload {
	if s == nil || !s.supported {
		return shellStatusPayload{
			Supported:       false,
			LockExplorer:    lockPref,
			ExplorerRunning: true,
			ClosedByApp:     false,
		}
	}
	running := true
	if s.isExplorerRunning != nil {
		running = s.isExplorerRunning()
	}
	s.mu.Lock()
	closed := s.closedByApp
	s.mu.Unlock()
	return shellStatusPayload{
		Supported:       true,
		LockExplorer:    lockPref,
		ExplorerRunning: running,
		ClosedByApp:     closed,
	}
}

func (s *shellController) suspend(lockPref bool) shellStatusPayload {
	if s == nil || !s.supported {
		return s.status(lockPref)
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.suspendExplorer != nil {
		closed := s.suspendExplorer()
		running := s.isExplorerRunning != nil && s.isExplorerRunning()
		s.mu.Lock()
		if closed || !running {
			s.closedByApp = true
		}
		s.mu.Unlock()
	}
	return s.status(lockPref)
}

func (s *shellController) restoreUnlocked() {
	if s.restoreExplorer == nil {
		return
	}
	restored := s.restoreExplorer()
	running := s.isExplorerRunning != nil && s.isExplorerRunning()
	if restored || running {
		s.mu.Lock()
		s.closedByApp = false
		s.mu.Unlock()
	}
}

func (s *shellController) restore(lockPref bool) shellStatusPayload {
	if s == nil || !s.supported {
		return s.status(lockPref)
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.restoreUnlocked()
	return s.status(lockPref)
}

func (s *shellController) restoreIfClosed() {
	if s == nil || !s.supported {
		return
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	wasClosed := s.closedByApp
	s.mu.Unlock()
	if !wasClosed {
		return
	}
	s.restoreUnlocked()
}

var (
	mu               sync.Mutex
	currentState     AppState
	stateVersion     int64 = 1
	liveEvent        LiveEvent
	dataFilePath     string
	backupFilePath   string
	csvFilePath      string
	authorizedQuit   bool
	activeKioskCmd   *exec.Cmd
	lastClientSeenNs atomic.Int64
	wakeStop         = make(chan struct{})
	wakeStopOnce     sync.Once
	exitDone         = make(chan struct{})
	exitDoneOnce     sync.Once
)

func defaultAppState() AppState {
	return AppState{
		Employees: []Employee{},
		Roster:    []RosterItem{},
		Prizes:    []Prize{},
		Draws:     []DrawInfo{},
		Current:   0,
		Seq:       0,
		Dupes:     0,
		Config: AppConfig{
			AdminPass:     "2026",
			RemoveWinners: true,
			ShowNames:     true,
			RosterOnly:    false,
			Sound:         true,
			LockExplorer:  boolPtr(true),
		},
	}
}

func hasStateData(st AppState) bool {
	return len(st.Employees) > 0 || len(st.Draws) > 0 || len(st.Roster) > 0
}

func normalizeLoadedState(st AppState) AppState {
	if strings.TrimSpace(st.Config.AdminPass) == "" {
		st.Config.AdminPass = "2026"
	}
	if st.Config.LockExplorer == nil {
		st.Config.LockExplorer = boolPtr(true)
	}
	if st.Employees == nil {
		st.Employees = []Employee{}
	}
	if st.Roster == nil {
		st.Roster = []RosterItem{}
	}
	st.Prizes = []Prize{}
	if st.Draws == nil {
		st.Draws = []DrawInfo{}
	}
	return st
}

func readStateJSONFile(path string) (AppState, bool) {
	if path == "" {
		return AppState{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return AppState{}, false
	}
	var st AppState
	if err := json.Unmarshal(raw, &st); err != nil {
		return AppState{}, false
	}
	return normalizeLoadedState(st), true
}

func isDirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	probe := filepath.Join(dir, fmt.Sprintf(".addiuva-write-check-%d", os.Getpid()))
	if err := os.WriteFile(probe, []byte("ok"), 0644); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

func isSubPath(child, parent string) bool {
	if child == "" || parent == "" {
		return false
	}
	cClean := strings.ToLower(filepath.Clean(child))
	pClean := strings.ToLower(filepath.Clean(parent))
	if cClean == pClean {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(pClean, sep) {
		pClean += sep
	}
	return strings.HasPrefix(cClean, pClean)
}

// resolveDataDir guarda los archivos junto al .exe cuando se ejecuta desde una carpeta normal escribible.
// Si se abrió directamente desde dentro de un .zip (carpeta Temp) o en una ruta de solo lectura,
// usa %LocalAppData%\AddiuvaRuleta para no perder inscriptos entre ejecuciones.
func resolveDataDir(exeDir, tempDir, localAppData string) string {
	cleanExe := filepath.Clean(exeDir)
	if !isSubPath(cleanExe, tempDir) && isDirWritable(cleanExe) {
		return cleanExe
	}
	fallbackBase := strings.TrimSpace(localAppData)
	if fallbackBase == "" {
		if cfgDir, err := os.UserConfigDir(); err == nil {
			fallbackBase = cfgDir
		}
	}
	if fallbackBase == "" {
		fallbackBase = tempDir
	}
	if fallbackBase != "" {
		target := filepath.Join(fallbackBase, "AddiuvaRuleta")
		if err := os.MkdirAll(target, 0755); err == nil && isDirWritable(target) {
			return target
		}
	}
	return cleanExe
}

func loadStateFromDisk() {
	mu.Lock()
	defer mu.Unlock()
	if st, ok := readStateJSONFile(dataFilePath); ok {
		currentState = st
		return
	}
	if bak, ok := readStateJSONFile(backupFilePath); ok {
		currentState = bak
		_ = saveStateLocked(false)
		return
	}
	currentState = defaultAppState()
	_ = saveStateLocked(false)
}

func writeFileAtomic(targetPath string, raw []byte) error {
	if targetPath == "" {
		return nil
	}
	tmp := targetPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, targetPath); err != nil {
		_ = os.Remove(tmp)
		return os.WriteFile(targetPath, raw, 0644)
	}
	return nil
}

func saveStateLocked(syncBackup bool) error {
	raw, err := json.MarshalIndent(currentState, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(dataFilePath, raw); err != nil {
		return err
	}
	if backupFilePath != "" && (syncBackup || hasStateData(currentState)) {
		_ = writeFileAtomic(backupFilePath, raw)
	}
	if csvFilePath != "" {
		_ = writeCsvBackupLocked()
	}
	return nil
}

func writeCsvBackupLocked() error {
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM para Excel en Windows
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"numero", "dni", "nombre_completo", "hora_inscripcion", "resultado"})
	for _, e := range currentState.Employees {
		premio := ""
		if e.Premio != nil {
			premio = *e.Premio
		}
		_ = w.Write([]string{
			fmt.Sprintf("%03d", e.Numero),
			e.Dni,
			e.Nombre,
			e.Checkin,
			premio,
		})
	}
	w.Flush()
	return writeFileAtomic(csvFilePath, buf.Bytes())
}

func markClientSeen() {
	lastClientSeenNs.Store(time.Now().UnixNano())
}

func clientSeenSince(t time.Time) bool {
	ns := lastClientSeenNs.Load()
	return ns > 0 && ns >= t.UnixNano()
}

func clientSeenWithin(d time.Duration) bool {
	ns := lastClientSeenNs.Load()
	if ns <= 0 {
		return false
	}
	return time.Since(time.Unix(0, ns)) <= d
}

func buildHTTPMux(subFS fs.FS, profileDir string, onQuit func(kmd *exec.Cmd)) *http.ServeMux {
	return buildHTTPMuxWithShell(subFS, profileDir, nil, onQuit)
}

func buildHTTPMuxWithShell(subFS fs.FS, profileDir string, shell *shellController, onQuit func(kmd *exec.Cmd)) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		markClientSeen()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if shell != nil && shell.supported {
			w.Header().Set("X-Shell-Supported", "true")
		} else {
			w.Header().Set("X-Shell-Supported", "false")
		}
		if r.Method == http.MethodGet {
			mu.Lock()
			w.Header().Set("X-State-Version", fmt.Sprintf("%d", stateVersion))
			_ = json.NewEncoder(w).Encode(currentState)
			mu.Unlock()
			return
		}
		if r.Method == http.MethodPost {
			var incoming AppState
			if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			allowEmpty := r.URL.Query().Get("allowEmpty") == "1" || r.URL.Query().Get("reset") == "1"

			mu.Lock()
			if incoming.Config.LockExplorer == nil && currentState.Config.LockExplorer != nil {
				v := *currentState.Config.LockExplorer
				incoming.Config.LockExplorer = &v
			}
			incoming = normalizeLoadedState(incoming)
			if strings.TrimSpace(incoming.Config.AdminPass) == "" {
				incoming.Config.AdminPass = currentState.Config.AdminPass
			}
			if hasStateData(currentState) && !hasStateData(incoming) && !allowEmpty {
				w.Header().Set("X-State-Version", fmt.Sprintf("%d", stateVersion))
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(struct {
					Ok    bool     `json:"ok"`
					Error string   `json:"error"`
					State AppState `json:"state"`
				}{
					Ok:    false,
					Error: "stale_empty_rejected",
					State: currentState,
				})
				mu.Unlock()
				return
			}
			currentState = incoming
			stateVersion++
			ver := stateVersion
			_ = saveStateLocked(allowEmpty)
			mu.Unlock()
			w.Header().Set("X-State-Version", fmt.Sprintf("%d", ver))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	mux.HandleFunc("/api/live", func(w http.ResponseWriter, r *http.Request) {
		markClientSeen()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet {
			mu.Lock()
			payload := struct {
				StateVersion int64     `json:"stateVersion"`
				Live         LiveEvent `json:"live"`
			}{
				StateVersion: stateVersion,
				Live:         liveEvent,
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		if r.Method == http.MethodPost {
			var ev LiveEvent
			if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			mu.Lock()
			liveEvent = ev
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	mux.HandleFunc("/api/shell", func(w http.ResponseWriter, r *http.Request) {
		markClientSeen()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet {
			mu.Lock()
			lockPref := currentState.Config.shouldLockExplorer()
			mu.Unlock()
			payload := shellStatusPayload{
				Supported:       false,
				LockExplorer:    lockPref,
				ExplorerRunning: true,
			}
			if shell != nil {
				payload = shell.status(lockPref)
			}
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Action string `json:"action"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			action := strings.ToLower(strings.TrimSpace(req.Action))
			mu.Lock()
			lockPref := currentState.Config.shouldLockExplorer()
			mu.Unlock()

			if shell == nil || !shell.supported {
				_ = json.NewEncoder(w).Encode(shellStatusPayload{
					Supported:       false,
					LockExplorer:    lockPref,
					ExplorerRunning: true,
					ClosedByApp:     false,
				})
				return
			}

			var payload shellStatusPayload
			switch action {
			case "close", "suspend":
				payload = shell.suspend(lockPref)
			case "restore":
				payload = shell.restore(lockPref)
			default:
				http.Error(w, "invalid action", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var payload struct {
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		expected := strings.TrimSpace(currentState.Config.AdminPass)
		if expected == "" {
			expected = "2026"
		}
		if strings.TrimSpace(payload.Password) != expected {
			mu.Unlock()
			http.Error(w, "unauthorized", http.StatusForbidden)
			return
		}
		alreadyClosing := authorizedQuit
		authorizedQuit = true
		kmd := activeKioskCmd
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}

		if !alreadyClosing && onQuit != nil {
			go onQuit(kmd)
		}
	})

	if subFS != nil {
		fileSrv := http.FileServer(http.FS(subFS))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			markClientSeen()
			if r.URL.Path == "/" || strings.HasSuffix(strings.ToLower(r.URL.Path), ".html") {
				w.Header().Set("Content-Language", "es")
			}
			fileSrv.ServeHTTP(w, r)
		})
	}
	return mux
}

func mergeUniqueStrings(raw any, required []string) []string {
	out := make([]string, 0, len(required))
	seen := make(map[string]struct{}, len(required))
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	switch xs := raw.(type) {
	case []string:
		for _, v := range xs {
			add(v)
		}
	case []any:
		for _, item := range xs {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	}
	for _, r := range required {
		add(r)
	}
	return out
}

// ensureNoTranslateBrowserPrefs preconfigura Default/Preferences en el perfil temporal
// de Edge/Chrome para desactivar el cartel "¿Traducir página del español?" de fábrica.
func ensureNoTranslateBrowserPrefs(profileDir string) error {
	if strings.TrimSpace(profileDir) == "" {
		return nil
	}
	defaultDir := filepath.Join(profileDir, "Default")
	if err := os.MkdirAll(defaultDir, 0755); err != nil {
		return err
	}
	prefsPath := filepath.Join(defaultDir, "Preferences")

	prefs := make(map[string]any)
	if raw, err := os.ReadFile(prefsPath); err == nil && len(bytes.TrimSpace(raw)) > 0 {
		var parsed map[string]any
		if json.Unmarshal(raw, &parsed) == nil && parsed != nil {
			prefs = parsed
		}
	}

	translateObj, _ := prefs["translate"].(map[string]any)
	if translateObj == nil {
		translateObj = make(map[string]any)
	}
	translateObj["enabled"] = false
	prefs["translate"] = translateObj

	prefs["translate_blocked_languages"] = mergeUniqueStrings(prefs["translate_blocked_languages"], []string{"es", "en"})

	intlObj, _ := prefs["intl"].(map[string]any)
	if intlObj == nil {
		intlObj = make(map[string]any)
	}
	intlObj["accept_languages"] = "es-AR,es,en-US,en"
	prefs["intl"] = intlObj

	out, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(prefsPath, out)
}

func buildBrowserLaunchArgs(mode launchMode, url string, useProfile string) []string {
	if mode == launchModeApp {
		return []string{
			"--app=" + url,
			"--start-fullscreen",
			"--no-first-run",
			"--no-default-browser-check",
			"--lang=es",
			"--disable-background-mode",
			"--disable-pinch",
			"--overscroll-history-navigation=0",
			"--disable-features=Translate,TranslateUI,EdgeSidebar",
			"--user-data-dir=" + useProfile,
		}
	}
	return []string{
		"--kiosk", url,
		"--edge-kiosk-type=fullscreen",
		"--no-first-run",
		"--no-default-browser-check",
		"--lang=es",
		"--disable-background-mode",
		"--disable-pinch",
		"--overscroll-history-navigation=0",
		"--disable-infobars",
		"--disable-session-crashed-bubble",
		"--disable-features=Translate,TranslateUI,EdgeSidebar",
		"--user-data-dir=" + useProfile,
	}
}

type browserProcess interface {
	Wait() error
}

type kioskWatchdogConfig struct {
	launch           func(mode launchMode) browserProcess
	isQuit           func() bool
	waitExit         func()
	clientSeenSince  func(t time.Time) bool
	clientSeenWithin func(d time.Duration) bool
	hasVisibleWindow func() bool
	onKioskConnected func()
	onFallbackMode   func()
	now              func() time.Time
	sleep            func(d time.Duration)
	handoffGrace     time.Duration
	clientStaleAfter time.Duration
	closeDebounce    time.Duration
	pollInterval     time.Duration
}

func runKioskWatchdog(cfg kioskWatchdogConfig) {
	if cfg.handoffGrace <= 0 {
		cfg.handoffGrace = 6 * time.Second
	}
	if cfg.clientStaleAfter <= 0 {
		cfg.clientStaleAfter = 2500 * time.Millisecond
	}
	if cfg.closeDebounce <= 0 {
		cfg.closeDebounce = 1200 * time.Millisecond
	}
	if cfg.pollInterval <= 0 {
		cfg.pollInterval = 250 * time.Millisecond
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.sleep == nil {
		cfg.sleep = time.Sleep
	}

	mode := launchModeKiosk
	quickFails := 0

	for {
		if cfg.isQuit() {
			cfg.waitExit()
			return
		}

		launchAt := cfg.now()
		proc := cfg.launch(mode)
		if proc == nil {
			if cfg.onFallbackMode != nil {
				cfg.onFallbackMode()
			}
			// Fallback al navegador predeterminado: mantener vivo el servidor y WakeLock
			cfg.waitExit()
			return
		}

		connectedNotified := false
		notifyConnected := func() {
			if connectedNotified || mode == launchModeFallback {
				return
			}
			connectedNotified = true
			if cfg.onKioskConnected != nil {
				go cfg.onKioskConnected()
			}
		}

		waitDone := make(chan error, 1)
		go func(p browserProcess) {
			waitDone <- p.Wait()
		}(proc)

	waitProcExit:
		for {
			if cfg.isQuit() {
				cfg.waitExit()
				return
			}
			if !connectedNotified && mode != launchModeFallback && cfg.clientSeenSince(launchAt) {
				notifyConnected()
			}
			select {
			case <-waitDone:
				break waitProcExit
			default:
				cfg.sleep(cfg.pollInterval)
			}
		}

		if cfg.isQuit() {
			cfg.waitExit()
			return
		}

		// En Windows, Edge suele inicializar la ventana en un proceso hijo y cerrar el PID padre enseguida.
		// Esperamos una ventana de gracia hasta ver actividad HTTP real de nuestro servidor local.
		sawClient := cfg.clientSeenSince(launchAt)
		graceDeadline := cfg.now().Add(cfg.handoffGrace)
		for !sawClient && cfg.now().Before(graceDeadline) {
			if cfg.isQuit() {
				cfg.waitExit()
				return
			}
			cfg.sleep(cfg.pollInterval)
			if cfg.clientSeenSince(launchAt) {
				sawClient = true
				break
			}
		}

		isAlive := func() bool {
			return cfg.clientSeenWithin(cfg.clientStaleAfter) || cfg.hasVisibleWindow()
		}

		if sawClient {
			notifyConnected()
			quickFails = 0
			// Mientras la app siga abierta (o en segundo plano tras handoff de Edge), NO relanzar nunca.
			absentSince := time.Time{}
			for {
				if cfg.isQuit() {
					cfg.waitExit()
					return
				}
				if isAlive() {
					absentSince = time.Time{}
				} else {
					if absentSince.IsZero() {
						absentSince = cfg.now()
					} else if cfg.now().Sub(absentSince) >= cfg.closeDebounce {
						// Confirmado: cerraron la ventana sin contraseña (ej. Alt+F4). Relanzar una vez.
						break
					}
				}
				cfg.sleep(cfg.pollInterval)
			}
			cfg.sleep(400 * time.Millisecond)
			continue
		}

		// El navegador salió rápido sin llegar a cargar ni una vez nuestro servidor local -> Circuit Breaker.
		quickFails++
		if quickFails == 1 && mode == launchModeKiosk {
			mode = launchModeApp
			cfg.sleep(600 * time.Millisecond)
			continue
		}
		mode = launchModeFallback
		if cfg.onFallbackMode != nil {
			cfg.onFallbackMode()
		}
		cfg.sleep(600 * time.Millisecond)
	}
}

func main() {
	initDpiAwareness()
	if !acquireSingleInstance() {
		return
	}
	exePath, err := os.Executable()
	exeDir := "."
	if err == nil {
		exeDir = filepath.Dir(exePath)
	}
	dataDir := resolveDataDir(exeDir, os.TempDir(), os.Getenv("LocalAppData"))
	dataFilePath = filepath.Join(dataDir, "datos-sorteo-addiuva.json")
	backupFilePath = filepath.Join(dataDir, "datos-sorteo-addiuva.bak.json")
	csvFilePath = filepath.Join(dataDir, "inscriptos-addiuva.csv")

	loadStateFromDisk()

	shellCtrl := newDefaultShellController()
	defer shellCtrl.restoreIfClosed()

	// Mantener la PC y pantalla de Windows siempre encendidas
	go keepSystemAwake(wakeStop)

	subFS, err := fs.Sub(embeddedFiles, "prototipo")
	if err != nil {
		return
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return
	}
	port := listener.Addr().(*net.TCPAddr).Port
	kioskURL := fmt.Sprintf("http://127.0.0.1:%d/#kiosco", port)
	cleanupStaleProfileDirs()
	profileDir := filepath.Join(os.TempDir(), fmt.Sprintf("addiuva-ruleta-kiosk-%d", os.Getpid()))

	mux := buildHTTPMuxWithShell(subFS, profileDir, shellCtrl, func(kmd *exec.Cmd) {
		time.Sleep(80 * time.Millisecond)
		closeAppWindows(kmd, profileDir)
		shellCtrl.restoreIfClosed()
		wakeStopOnce.Do(func() { close(wakeStop) })
		exitDoneOnce.Do(func() { close(exitDone) })
		os.Exit(0)
	})

	go func() {
		_ = http.Serve(listener, mux)
	}()

	var shellBootOnce sync.Once
	runKioskWatchdog(kioskWatchdogConfig{
		launch: func(mode launchMode) browserProcess {
			cmd := launchKioskBrowser(mode, kioskURL, profileDir)
			mu.Lock()
			activeKioskCmd = cmd
			mu.Unlock()
			if cmd == nil {
				return nil
			}
			return cmd
		},
		isQuit: func() bool {
			mu.Lock()
			q := authorizedQuit
			mu.Unlock()
			return q
		},
		waitExit:         func() { <-exitDone },
		clientSeenSince:  clientSeenSince,
		clientSeenWithin: clientSeenWithin,
		hasVisibleWindow: hasVisibleKioskWindow,
		onKioskConnected: func() {
			shellBootOnce.Do(func() {
				mu.Lock()
				shouldLock := currentState.Config.shouldLockExplorer()
				quit := authorizedQuit
				mu.Unlock()
				if quit || !shouldLock {
					return
				}
				st := shellCtrl.suspend(true)
				mu.Lock()
				quitNow := authorizedQuit
				mu.Unlock()
				if quitNow && st.ClosedByApp {
					shellCtrl.restoreIfClosed()
				}
			})
		},
		onFallbackMode: func() {
			shellCtrl.restoreIfClosed()
		},
	})
}

