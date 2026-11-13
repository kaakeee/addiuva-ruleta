package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
