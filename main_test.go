package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadStateFromDiskStripsLegacyPrizes(t *testing.T) {
	dir := t.TempDir()
	dataFilePath = filepath.Join(dir, "datos-sorteo-addiuva.json")
	backupFilePath = filepath.Join(dir, "datos-sorteo-addiuva.bak.json")
	csvFilePath = filepath.Join(dir, "inscriptos-addiuva.csv")

	legacyJSON := `{
	  "employees": [
	    {"dni":"35123456","nombre":"Sofía González","numero":1,"checkin":"2026-11-13T20:00:00Z","premio":null,"ausente":false}
	  ],
	  "roster": [],
	  "prizes": [
	    {"id":"p1","nombre":"Smart TV 50\"","ganador":null}
	  ],
	  "current": 0,
	  "seq": 1,
	  "dupes": 0,
	  "config": {"adminPass":"2026","removeWinners":true,"showNames":true,"rosterOnly":false,"sound":true}
	}`
	if err := os.WriteFile(dataFilePath, []byte(legacyJSON), 0644); err != nil {
		t.Fatal(err)
	}

	loadStateFromDisk()

	if len(currentState.Prizes) != 0 {
		t.Fatalf("expected 0 prizes after loading legacy state, got %d", len(currentState.Prizes))
	}
	if currentState.Draws == nil || len(currentState.Draws) != 0 {
		t.Fatalf("expected non-nil empty Draws slice, got %#v", currentState.Draws)
	}
	if len(currentState.Employees) != 1 || currentState.Employees[0].Nombre != "Sofía González" {
		t.Fatalf("expected participant to be preserved, got %#v", currentState.Employees)
	}

	mu.Lock()
	err := saveStateLocked(true)
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(backupFilePath); err != nil {
		t.Fatalf("expected backup json to be written, got err: %v", err)
	}

	csvBytes, err := os.ReadFile(csvFilePath)
	if err != nil {
		t.Fatal(err)
	}
	csvStr := string(csvBytes)
	if !strings.Contains(csvStr, "numero,dni,nombre_completo,hora_inscripcion,resultado") {
		t.Fatalf("unexpected CSV header: %q", csvStr)
	}
	if !strings.Contains(csvStr, "001,35123456,Sofía González") {
		t.Fatalf("expected employee row in CSV, got: %q", csvStr)
	}
}

func TestLoadStateRecoversFromBackupWhenCorrupt(t *testing.T) {
	dir := t.TempDir()
	dataFilePath = filepath.Join(dir, "datos-sorteo-addiuva.json")
	backupFilePath = filepath.Join(dir, "datos-sorteo-addiuva.bak.json")
	csvFilePath = filepath.Join(dir, "inscriptos-addiuva.csv")

	mu.Lock()
	currentState = defaultAppState()
	currentState.Employees = []Employee{
		{Dni: "30111222", Nombre: "Laura Pérez", Numero: 1, Checkin: "2026-11-16T20:00:00Z"},
	}
	currentState.Seq = 1
	if err := saveStateLocked(true); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	mu.Unlock()

	// Simular archivo principal truncado / corrupto
	if err := os.WriteFile(dataFilePath, []byte(`{"employees":[{"dni":`), 0644); err != nil {
		t.Fatal(err)
	}

	loadStateFromDisk()
	if len(currentState.Employees) != 1 || currentState.Employees[0].Dni != "30111222" {
		t.Fatalf("expected recovery from backup file, got %#v", currentState.Employees)
	}
}

func TestResolveDataDir(t *testing.T) {
	root := t.TempDir()
	normalExeDir := filepath.Join(root, "escritorio-addiuva")
	tempRoot := filepath.Join(root, "temp")
	zipExtractDir := filepath.Join(tempRoot, "Temp1_AddiuvaRuleta-Windows-x64.zip")
	localAppData := filepath.Join(root, "localappdata")
	readOnlyDir := filepath.Join(root, "readonly")

	for _, d := range []string{normalExeDir, zipExtractDir, localAppData, readOnlyDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	if got := resolveDataDir(normalExeDir, tempRoot, localAppData); got != normalExeDir {
		t.Fatalf("expected writable non-temp dir %q, got %q", normalExeDir, got)
	}

	expectedFallback := filepath.Join(localAppData, "AddiuvaRuleta")
	if got := resolveDataDir(zipExtractDir, tempRoot, localAppData); got != expectedFallback {
		t.Fatalf("expected temp zip dir to fallback to %q, got %q", expectedFallback, got)
	}

	if err := os.Chmod(readOnlyDir, 0555); err == nil {
		defer os.Chmod(readOnlyDir, 0755)
		// En root (uid 0) chmod 0555 sigue permitiendo escribir; solo verificar si realmente no es escribible
		if !isDirWritable(readOnlyDir) {
			if got := resolveDataDir(readOnlyDir, tempRoot, localAppData); got != expectedFallback {
				t.Fatalf("expected read-only dir to fallback to %q, got %q", expectedFallback, got)
			}
		}
	}
}

func TestApiStateRejectsAccidentalEmptyOverwriteAndAllowsExplicitReset(t *testing.T) {
	dir := t.TempDir()
	dataFilePath = filepath.Join(dir, "datos-sorteo-addiuva.json")
	backupFilePath = filepath.Join(dir, "datos-sorteo-addiuva.bak.json")
	csvFilePath = filepath.Join(dir, "inscriptos-addiuva.csv")

	mu.Lock()
	currentState = defaultAppState()
	currentState.Employees = []Employee{
		{Dni: "35123456", Nombre: "Sofía González", Numero: 1, Checkin: "2026-11-16T20:00:00Z"},
		{Dni: "36123456", Nombre: "Juan López", Numero: 2, Checkin: "2026-11-16T20:01:00Z"},
	}
	currentState.Seq = 2
	if err := saveStateLocked(true); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	mu.Unlock()

	mux := buildHTTPMux(nil, "", nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. Un cliente vacío sin allowEmpty=1 NO debe poder borrar participantes existentes
	emptyBody := `{"employees":[],"roster":[],"prizes":[],"draws":[],"seq":0,"dupes":0,"config":{"adminPass":"2026","removeWinners":true,"showNames":true,"rosterOnly":false,"sound":true}}`
	resConflict, err := http.Post(srv.URL+"/api/state", "application/json", strings.NewReader(emptyBody))
	if err != nil {
		t.Fatal(err)
	}
	resConflict.Body.Close()
	if resConflict.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when stale client posts empty state, got %d", resConflict.StatusCode)
	}
	mu.Lock()
	empLen := len(currentState.Employees)
	mu.Unlock()
	if empLen != 2 {
		t.Fatalf("expected 2 employees preserved after rejected overwrite, got %d", empLen)
	}

	// 2. El botón explícito "Borrar todo" (?allowEmpty=1) sí debe reiniciar y actualizar también el backup
	resReset, err := http.Post(srv.URL+"/api/state?allowEmpty=1", "application/json", strings.NewReader(emptyBody))
	if err != nil {
		t.Fatal(err)
	}
	resReset.Body.Close()
	if resReset.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for explicit ?allowEmpty=1 reset, got %d", resReset.StatusCode)
	}

	// Al reiniciar desde disco no deben resucitar los participantes borrados desde .bak.json
	loadStateFromDisk()
	if len(currentState.Employees) != 0 {
		t.Fatalf("expected 0 employees persisted after explicit reset, got %d", len(currentState.Employees))
	}
	bakRaw, err := os.ReadFile(backupFilePath)
	if err != nil {
		t.Fatal(err)
	}
	var bak AppState
	if err := json.Unmarshal(bakRaw, &bak); err != nil {
		t.Fatal(err)
	}
	if len(bak.Employees) != 0 {
		t.Fatalf("expected backup to also be cleared on explicit reset, got %d employees", len(bak.Employees))
	}
}

func TestApiQuitAuthorization(t *testing.T) {
	mu.Lock()
	currentState = defaultAppState()
	authorizedQuit = false
	mu.Unlock()

	var quitCalled atomic.Bool
	mux := buildHTTPMux(nil, "", func(kmd *exec.Cmd) {
		quitCalled.Store(true)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Contraseña incorrecta -> 403 Forbidden
	resBad, err := http.Post(srv.URL+"/api/quit", "application/json", strings.NewReader(`{"password":"wrong"}`))
	if err != nil {
		t.Fatal(err)
	}
	resBad.Body.Close()
	if resBad.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 on wrong password, got %d", resBad.StatusCode)
	}
	if quitCalled.Load() {
		t.Fatalf("quit callback must not trigger on wrong password")
	}

	// Contraseña correcta -> 200 OK y callback ejecutado
	resOk, err := http.Post(srv.URL+"/api/quit", "application/json", strings.NewReader(`{"password":"2026"}`))
	if err != nil {
		t.Fatal(err)
	}
	resOk.Body.Close()
	if resOk.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on valid password, got %d", resOk.StatusCode)
	}

	deadline := time.Now().Add(time.Second)
	for !quitCalled.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !quitCalled.Load() {
		t.Fatalf("expected quit callback to run on valid password")
	}
	mu.Lock()
	aq := authorizedQuit
	mu.Unlock()
	if !aq {
		t.Fatalf("expected authorizedQuit to be true")
	}
}

type fakeBrowserProc struct {
	exitAfter time.Duration
}

func (f fakeBrowserProc) Wait() error {
	if f.exitAfter > 0 {
		time.Sleep(f.exitAfter)
	}
	return nil
}

func TestKioskWatchdogNoReloadOnEdgeStubHandoff(t *testing.T) {
	var launches atomic.Int32
	var quit atomic.Bool
	var pollTicks atomic.Int32
	clientAlive := atomic.Bool{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runKioskWatchdog(kioskWatchdogConfig{
			handoffGrace:     80 * time.Millisecond,
			clientStaleAfter: 60 * time.Millisecond,
			closeDebounce:    40 * time.Millisecond,
			pollInterval:     5 * time.Millisecond,
			launch: func(mode launchMode) browserProcess {
				launches.Add(1)
				// Simula Edge cerrando el PID launcher inmediatamente tras entregar la URL al proceso hijo
				go func() {
					time.Sleep(15 * time.Millisecond)
					clientAlive.Store(true)
				}()
				return fakeBrowserProc{exitAfter: 2 * time.Millisecond}
			},
			isQuit: func() bool {
				return quit.Load()
			},
			waitExit: func() {},
			clientSeenSince: func(ts time.Time) bool {
				return clientAlive.Load()
			},
			clientSeenWithin: func(d time.Duration) bool {
				if clientAlive.Load() {
					if pollTicks.Add(1) >= 20 {
						quit.Store(true)
					}
					return true
				}
				return false
			},
			hasVisibleWindow: func() bool {
				return clientAlive.Load()
			},
		})
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not exit cleanly after quit")
	}

	if got := launches.Load(); got != 1 {
		t.Fatalf("expected Edge stub handoff to launch browser exactly ONCE (no reload loop), got %d launches", got)
	}
}

func TestKioskWatchdogRelaunchesAfterRealAltF4Close(t *testing.T) {
	var launches atomic.Int32
	var quit atomic.Bool
	clientAlive := atomic.Bool{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runKioskWatchdog(kioskWatchdogConfig{
			handoffGrace:     80 * time.Millisecond,
			clientStaleAfter: 20 * time.Millisecond,
			closeDebounce:    30 * time.Millisecond,
			pollInterval:     5 * time.Millisecond,
			launch: func(mode launchMode) browserProcess {
				n := launches.Add(1)
				clientAlive.Store(true)
				if n == 1 {
					// Simular que a los 25ms el usuario presiona Alt+F4 sin contraseña
					go func() {
						time.Sleep(25 * time.Millisecond)
						clientAlive.Store(false)
					}()
				} else if n >= 2 {
					go func() {
						time.Sleep(20 * time.Millisecond)
						quit.Store(true)
					}()
				}
				return fakeBrowserProc{exitAfter: 2 * time.Millisecond}
			},
			isQuit:           func() bool { return quit.Load() },
			waitExit:         func() {},
			clientSeenSince:  func(ts time.Time) bool { return true },
			clientSeenWithin: func(d time.Duration) bool { return clientAlive.Load() },
			hasVisibleWindow: func() bool { return clientAlive.Load() },
		})
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog did not relaunch & exit in time")
	}

	if got := launches.Load(); got != 2 {
		t.Fatalf("expected relaunch once after genuine Alt+F4 close, got %d launches", got)
	}
}

func TestKioskWatchdogCircuitBreakerOnLaunchCrash(t *testing.T) {
	var modes []launchMode
	var fallbackWaitCalled atomic.Bool

	done := make(chan struct{})
	go func() {
		defer close(done)
		runKioskWatchdog(kioskWatchdogConfig{
			handoffGrace:     20 * time.Millisecond,
			clientStaleAfter: 20 * time.Millisecond,
			closeDebounce:    20 * time.Millisecond,
			pollInterval:     5 * time.Millisecond,
			launch: func(mode launchMode) browserProcess {
				modes = append(modes, mode)
				if mode == launchModeFallback {
					return nil
				}
				return fakeBrowserProc{exitAfter: 2 * time.Millisecond}
			},
			isQuit:           func() bool { return false },
			waitExit:         func() { fallbackWaitCalled.Store(true) },
			clientSeenSince:  func(ts time.Time) bool { return false },
			clientSeenWithin: func(d time.Duration) bool { return false },
			hasVisibleWindow: func() bool { return false },
			sleep:            func(d time.Duration) { time.Sleep(2 * time.Millisecond) },
		})
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watchdog circuit breaker stuck in loop")
	}

	if !fallbackWaitCalled.Load() {
		t.Fatal("expected fallback waitExit to be invoked")
	}
	if len(modes) != 3 || modes[0] != launchModeKiosk || modes[1] != launchModeApp || modes[2] != launchModeFallback {
		t.Fatalf("expected escalation [kiosk, app, fallback], got %#v", modes)
	}
}

func TestLoadStateLockExplorerDefaultAndPreserve(t *testing.T) {
	dir := t.TempDir()
	dataFilePath = filepath.Join(dir, "datos-sorteo-addiuva.json")
	backupFilePath = filepath.Join(dir, "datos-sorteo-addiuva.bak.json")
	csvFilePath = filepath.Join(dir, "inscriptos-addiuva.csv")

	legacyJSON := `{"employees":[],"roster":[],"prizes":[],"draws":[],"seq":0,"dupes":0,"config":{"adminPass":"2026","removeWinners":true,"showNames":true,"rosterOnly":false,"sound":true}}`
	if err := os.WriteFile(dataFilePath, []byte(legacyJSON), 0644); err != nil {
		t.Fatal(err)
	}
	loadStateFromDisk()
	if !currentState.Config.shouldLockExplorer() {
		t.Fatalf("expected legacy state without lockExplorer to default to true")
	}

	mux := buildHTTPMuxWithShell(nil, "", nil, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Disable lockExplorer explicitly -> should persist as false
	disabledJSON := `{"employees":[],"roster":[],"prizes":[],"draws":[],"seq":0,"dupes":0,"config":{"adminPass":"2026","removeWinners":true,"showNames":true,"rosterOnly":false,"sound":true,"lockExplorer":false}}`
	res, err := http.Post(srv.URL+"/api/state?allowEmpty=1", "application/json", strings.NewReader(disabledJSON))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK saving lockExplorer=false, got %d", res.StatusCode)
	}

	loadStateFromDisk()
	if currentState.Config.shouldLockExplorer() {
		t.Fatalf("expected lockExplorer=false to stay false after reload")
	}

	// Incomplete payload omitting lockExplorer should preserve server value (false)
	res2, err := http.Post(srv.URL+"/api/state?allowEmpty=1", "application/json", strings.NewReader(legacyJSON))
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if currentState.Config.shouldLockExplorer() {
		t.Fatalf("expected omitted lockExplorer in POST to preserve existing false setting")
	}
}

func TestApiShellEndpointAndControllerState(t *testing.T) {
	mu.Lock()
	currentState = defaultAppState()
	mu.Unlock()

	var running atomic.Bool
	running.Store(true)
	var suspendCalls, restoreCalls atomic.Int32

	ctrl := &shellController{
		supported: true,
		isExplorerRunning: func() bool {
			return running.Load()
		},
		suspendExplorer: func() bool {
			suspendCalls.Add(1)
			time.Sleep(25 * time.Millisecond)
			running.Store(false)
			return true
		},
		restoreExplorer: func() bool {
			restoreCalls.Add(1)
			running.Store(true)
			return true
		},
	}

	mux := buildHTTPMuxWithShell(nil, "", ctrl, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resState, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	resState.Body.Close()
	if got := resState.Header.Get("X-Shell-Supported"); got != "true" {
		t.Fatalf("expected X-Shell-Supported: true header, got %q", got)
	}

	// GET initial status
	resGet, err := http.Get(srv.URL + "/api/shell")
	if err != nil {
		t.Fatal(err)
	}
	var st shellStatusPayload
	if err := json.NewDecoder(resGet.Body).Decode(&st); err != nil {
		resGet.Body.Close()
		t.Fatal(err)
	}
	resGet.Body.Close()
	if !st.Supported || !st.LockExplorer || !st.ExplorerRunning || st.ClosedByApp {
		t.Fatalf("unexpected initial GET /api/shell status: %+v", st)
	}

	// POST close
	resClose, err := http.Post(srv.URL+"/api/shell", "application/json", strings.NewReader(`{"action":"close"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(resClose.Body).Decode(&st); err != nil {
		resClose.Body.Close()
		t.Fatal(err)
	}
	resClose.Body.Close()
	if st.ExplorerRunning || !st.ClosedByApp || suspendCalls.Load() != 1 {
		t.Fatalf("expected closedByApp=true & explorerRunning=false after close, got %+v", st)
	}

	// POST invalid action -> 400
	resBad, err := http.Post(srv.URL+"/api/shell", "application/json", strings.NewReader(`{"action":"invalid"}`))
	if err != nil {
		t.Fatal(err)
	}
	resBad.Body.Close()
	if resBad.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid action, got %d", resBad.StatusCode)
	}

	// POST restore
	resRestore, err := http.Post(srv.URL+"/api/shell", "application/json", strings.NewReader(`{"action":"restore"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(resRestore.Body).Decode(&st); err != nil {
		resRestore.Body.Close()
		t.Fatal(err)
	}
	resRestore.Body.Close()
	if !st.ExplorerRunning || st.ClosedByApp || restoreCalls.Load() != 1 {
		t.Fatalf("expected restored shell, got %+v", st)
	}

	// Second restoreIfClosed while already open should be a no-op
	ctrl.restoreIfClosed()
	if restoreCalls.Load() != 1 {
		t.Fatalf("expected restoreIfClosed when not closed to be a no-op, got %d calls", restoreCalls.Load())
	}

	// Concurrency: suspend in-flight while restoreIfClosed runs
	doneSuspend := make(chan struct{})
	go func() {
		ctrl.suspend(true)
		close(doneSuspend)
	}()
	time.Sleep(5 * time.Millisecond)
	ctrl.restoreIfClosed()
	<-doneSuspend
	if !running.Load() || ctrl.status(true).ClosedByApp {
		t.Fatalf("expected restoreIfClosed serialized after in-flight suspend to leave Explorer running")
	}
}

func TestKioskWatchdogSuspendsAndRestoresExplorer(t *testing.T) {
	// 1. Long-lived browser PID connects without exiting first -> onKioskConnected still fires cleanly
	{
		var quit, connected atomic.Bool
		clientAlive := atomic.Bool{}
		procDone := make(chan struct{})

		done := make(chan struct{})
		go func() {
			defer close(done)
			runKioskWatchdog(kioskWatchdogConfig{
				handoffGrace:     80 * time.Millisecond,
				clientStaleAfter: 60 * time.Millisecond,
				closeDebounce:    40 * time.Millisecond,
				pollInterval:     5 * time.Millisecond,
				launch: func(mode launchMode) browserProcess {
					go func() {
						time.Sleep(12 * time.Millisecond)
						clientAlive.Store(true)
					}()
					return chanBrowserProc{done: procDone}
				},
				isQuit:           func() bool { return quit.Load() },
				waitExit:         func() {},
				clientSeenSince:  func(t time.Time) bool { return clientAlive.Load() },
				clientSeenWithin: func(d time.Duration) bool { return clientAlive.Load() },
				hasVisibleWindow: func() bool { return false },
				onKioskConnected: func() {
					connected.Store(true)
					quit.Store(true)
					close(procDone)
				},
			})
		}()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("watchdog long-lived PID test timed out")
		}
		if !connected.Load() {
			t.Fatal("expected onKioskConnected to fire for long-lived browser PID without waiting for exit")
		}
	}

	// 2. Stub handoff (launcher PID exits in 2ms, child tab connects at 15ms) -> onKioskConnected fires once
	{
		var quit atomic.Bool
		var connectCalls atomic.Int32
		clientAlive := atomic.Bool{}

		done := make(chan struct{})
		go func() {
			defer close(done)
			runKioskWatchdog(kioskWatchdogConfig{
				handoffGrace:     80 * time.Millisecond,
				clientStaleAfter: 60 * time.Millisecond,
				closeDebounce:    40 * time.Millisecond,
				pollInterval:     5 * time.Millisecond,
				launch: func(mode launchMode) browserProcess {
					go func() {
						time.Sleep(15 * time.Millisecond)
						clientAlive.Store(true)
					}()
					return fakeBrowserProc{exitAfter: 2 * time.Millisecond}
				},
				isQuit:           func() bool { return quit.Load() },
				waitExit:         func() {},
				clientSeenSince:  func(t time.Time) bool { return clientAlive.Load() },
				clientSeenWithin: func(d time.Duration) bool { return clientAlive.Load() },
				hasVisibleWindow: func() bool { return clientAlive.Load() },
				onKioskConnected: func() {
					connectCalls.Add(1)
					time.Sleep(15 * time.Millisecond)
					quit.Store(true)
				},
			})
		}()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("watchdog stub handoff explorer test timed out")
		}
		if connectCalls.Load() != 1 {
			t.Fatalf("expected onKioskConnected to fire once on stub handoff, got %d", connectCalls.Load())
		}
	}

	// 2. Circuit breaker to fallback -> onKioskConnected NOT called, onFallbackMode IS called
	{
		var connected, fallback atomic.Bool
		done := make(chan struct{})
		go func() {
			defer close(done)
			runKioskWatchdog(kioskWatchdogConfig{
				handoffGrace:     15 * time.Millisecond,
				clientStaleAfter: 15 * time.Millisecond,
				closeDebounce:    15 * time.Millisecond,
				pollInterval:     5 * time.Millisecond,
				launch: func(mode launchMode) browserProcess {
					if mode == launchModeFallback {
						return nil
					}
					return fakeBrowserProc{exitAfter: 2 * time.Millisecond}
				},
				isQuit:           func() bool { return false },
				waitExit:         func() {},
				clientSeenSince:  func(t time.Time) bool { return false },
				clientSeenWithin: func(d time.Duration) bool { return false },
				hasVisibleWindow: func() bool { return false },
				onKioskConnected: func() { connected.Store(true) },
				onFallbackMode:   func() { fallback.Store(true) },
				sleep:            func(d time.Duration) { time.Sleep(2 * time.Millisecond) },
			})
		}()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("watchdog fallback explorer test timed out")
		}
		if connected.Load() {
			t.Fatal("onKioskConnected must not run when browser never connected")
		}
		if !fallback.Load() {
			t.Fatal("expected onFallbackMode to fire on fallback")
		}
	}
}

type chanBrowserProc struct {
	done <-chan struct{}
}

func (c chanBrowserProc) Wait() error {
	<-c.done
	return nil
}

