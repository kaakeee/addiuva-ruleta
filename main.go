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
	mux := http.NewServeMux()

	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		markClientSeen()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
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
			fileSrv.ServeHTTP(w, r)
		})
	}
	return mux
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
			// Fallback al navegador predeterminado: mantener vivo el servidor y WakeLock
			cfg.waitExit()
			return
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

	mux := buildHTTPMux(subFS, profileDir, func(kmd *exec.Cmd) {
		time.Sleep(80 * time.Millisecond)
		closeAppWindows(kmd, profileDir)
		wakeStopOnce.Do(func() { close(wakeStop) })
		exitDoneOnce.Do(func() { close(exitDone) })
		os.Exit(0)
	})

	go func() {
		_ = http.Serve(listener, mux)
	}()

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
	})
}

