package main

import (
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
	err := saveStateLocked()
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
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

func TestApiQuitAuthorization(t *testing.T) {
	mu.Lock()
	currentState = defaultAppState()
	authorizedQuit = false
	mu.Unlock()

	var quitCalled atomic.Bool
	mux := buildHTTPMux(nil, "", "", func(kmd, hmd *exec.Cmd) {
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
