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
	Current   int          `json:"current"`
	Seq       int          `json:"seq"`
	Dupes     int          `json:"dupes"`
	Config    AppConfig    `json:"config"`
}

var (
	mu             sync.Mutex
	currentState   AppState
	dataFilePath   string
	csvFilePath    string
	authorizedQuit bool
	activeKioskCmd *exec.Cmd
	wakeStop       = make(chan struct{})
)

func defaultAppState() AppState {
	prizes := []string{
		"Kit Corporativo Addiuva",
		"Auriculares Inalámbricos",
		"Día Libre Extra",
		"Cena para Dos Personas",
		"Parlante Bluetooth",
		"Smartwatch",
		"Tablet 10\"",
		"Smart TV 50\"",
		"Estadía Fin de Semana",
	}
	pList := make([]Prize, len(prizes))
	for i, name := range prizes {
		pList[i] = Prize{ID: fmt.Sprintf("p%d", i+1), Nombre: name, Ganador: nil}
	}
	return AppState{
		Employees: []Employee{},
		Roster:    []RosterItem{},
		Prizes:    pList,
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

func loadStateFromDisk() {
	mu.Lock()
	defer mu.Unlock()
	raw, err := os.ReadFile(dataFilePath)
	if err != nil {
		currentState = defaultAppState()
		_ = saveStateLocked()
		return
	}
	var st AppState
	if err := json.Unmarshal(raw, &st); err != nil {
		currentState = defaultAppState()
		return
	}
	if strings.TrimSpace(st.Config.AdminPass) == "" {
		st.Config.AdminPass = "2026"
	}
	currentState = st
}

func saveStateLocked() error {
	raw, err := json.MarshalIndent(currentState, "", "  ")
	if err != nil {
		return err
	}
	tmp := dataFilePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	_ = os.Rename(tmp, dataFilePath)
	_ = writeCsvBackupLocked()
	return nil
}

func writeCsvBackupLocked() error {
	var buf bytes.Buffer
	buf.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM para Excel en Windows
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"numero", "dni", "nombre_completo", "hora_inscripcion", "premio_ganado"})
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
	return os.WriteFile(csvFilePath, buf.Bytes(), 0644)
}

func main() {
	exePath, err := os.Executable()
	baseDir := "."
	if err == nil {
		baseDir = filepath.Dir(exePath)
	}
	dataFilePath = filepath.Join(baseDir, "datos-sorteo-addiuva.json")
	csvFilePath = filepath.Join(baseDir, "inscriptos-addiuva.csv")

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
	hdmiURL := fmt.Sprintf("http://127.0.0.1:%d/?hdmi=1#pantalla", port)
	profileDir := filepath.Join(os.TempDir(), "addiuva-ruleta-kiosk-profile")

	mux := http.NewServeMux()

	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet {
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(currentState)
			return
		}
		if r.Method == http.MethodPost {
			var incoming AppState
			if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
				http.Error(w, "invalid json", http.StatusBadRequest)
				return
			}
			mu.Lock()
			if strings.TrimSpace(incoming.Config.AdminPass) == "" {
				incoming.Config.AdminPass = currentState.Config.AdminPass
			}
			currentState = incoming
			_ = saveStateLocked()
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	mux.HandleFunc("/api/open-hdmi", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		go launchHdmiWindow(hdmiURL, profileDir)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
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
		authorizedQuit = true
		kmd := activeKioskCmd
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))

		go func() {
			time.Sleep(250 * time.Millisecond)
			close(wakeStop)
			if kmd != nil && kmd.Process != nil {
				_ = kmd.Process.Kill()
			}
			os.Exit(0)
		}()
	})

	mux.Handle("/", http.FileServer(http.FS(subFS)))

	go func() {
		_ = http.Serve(listener, mux)
	}()

	// Watchdog del Modo Kiosco: si alguien cierra la ventana con Alt+F4 sin usar la contraseña
	// en el botón "Salir", la vuelve a lanzar automáticamente en pantalla completa.
	for {
		mu.Lock()
		if authorizedQuit {
			mu.Unlock()
			return
		}
		cmd := launchKioskBrowser(kioskURL, profileDir)
		activeKioskCmd = cmd
		mu.Unlock()

		if cmd == nil {
			// Si usó el navegador predeterminado (fallback), mantener vivo el servidor y el WakeLock
			select {}
		}
		_ = cmd.Wait()
		time.Sleep(400 * time.Millisecond)
	}
}
