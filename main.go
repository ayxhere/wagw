// wagw — AyX WhatsApp gateway (Go + whatsmeow).
// TEMPORARY: login only (QR pairing + status). No messaging yet.
//
// Env:
//   WAGW_PORT      — HTTP port (default 18080)
//   WAGW_AUTH_DIR  — dir for whatsmeow.db (default ./auth)
//   WAGW_API_TOKEN — required, X-Api-Token auth (GET /health exempt)
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"
)

var (
	cli      *whatsmeow.Client
	qrCode   string
	qrMu     sync.RWMutex
	paired   bool
	pairMu   sync.RWMutex
	apiToken string
)

func main() {
	port := envOr("WAGW_PORT", "18080")
	authDir := envOr("WAGW_AUTH_DIR", "./auth")
	apiToken = os.Getenv("WAGW_API_TOKEN")
	if apiToken == "" {
		log.Fatal("WAGW_API_TOKEN is required")
	}
	if err := os.MkdirAll(authDir, 0700); err != nil {
		log.Fatal(err)
	}

	// Device name (same file/format as the Node gateway).
	if dn := readDeviceName(filepath.Dir(authDir)); dn != "" {
		// whatsmeow reads OS from the store; we set it at connect time.
		_ = dn
	}

	dbLog := waLog.Stdout("DB", "INFO", true)
	container, err := sqlstore.New("sqlite",
		fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", filepath.Join(authDir, "whatsmeow.db")), dbLog)
	if err != nil {
		log.Fatal(err)
	}
	device, err := container.GetFirstDevice()
	if err != nil {
		log.Fatal(err)
	}

	cli = whatsmeow.NewClient(device, waLog.Stdout("Client", "INFO", true))
	cli.AddEventHandler(eventHandler)

	if cli.Store.ID == nil {
		// Not paired — start QR flow.
		go pairQR()
	} else {
		pairMu.Lock()
		paired = true
		pairMu.Unlock()
		if err := cli.Connect(); err != nil {
			log.Printf("connect: %v", err)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth) // exempt from auth
	mux.HandleFunc("/qr.png", withAuth(handleQR))
	mux.HandleFunc("/status", withAuth(handleStatus))

	addr := "127.0.0.1:" + port
	log.Printf("wagw listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func pairQR() {
	qrChan, err := cli.GetQRChannel(context.Background())
	if err != nil {
		log.Printf("qr channel: %v", err)
		return
	}
	if err := cli.Connect(); err != nil {
		log.Printf("connect: %v", err)
		return
	}
	for evt := range qrChan {
		if evt.Event == "code" {
			qrMu.Lock()
			qrCode = evt.Code
			qrMu.Unlock()
			log.Printf("new QR code")
		} else {
			log.Printf("pair event: %s", evt.Event)
			if evt.Event == "success" {
				pairMu.Lock()
				paired = true
				pairMu.Unlock()
				qrMu.Lock()
				qrCode = ""
				qrMu.Unlock()
			}
		}
	}
}

func eventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Connected:
		log.Printf("connected")
	case *events.Disconnected:
		log.Printf("disconnected")
	case *events.LoggedOut:
		log.Printf("logged out")
		pairMu.Lock()
		paired = false
		pairMu.Unlock()
		go pairQR()
	_ = v
	}
}

// --- HTTP ---

func withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Api-Token")), []byte(apiToken)) != 1 {
			http.Error(w, `{"ok":false,"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "gateway": "wagw-go"})
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	pairMu.RLock()
	p := paired
	pairMu.RUnlock()
	qrMu.RLock()
	q := qrCode
	qrMu.RUnlock()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"paired":      p,
		"connected":   cli.IsConnected(),
		"loggedIn":    cli.IsLoggedIn(),
		"hasQR":       q != "",
		"device_name": readDeviceName(filepath.Dir(envOr("WAGW_AUTH_DIR", "./auth"))),
	})
}

func handleQR(w http.ResponseWriter, r *http.Request) {
	qrMu.RLock()
	q := qrCode
	qrMu.RUnlock()
	if q == "" {
		http.Error(w, `{"ok":false,"error":"no qr"}`, http.StatusNotFound)
		return
	}
	// Return the raw QR string; the app renders it (same as Node gateway).
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(q))
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// readDeviceName reads the custom device name file (same as Node gateway:
// <authDir>/../device_name, sanitized, empty if invalid/missing).
func readDeviceName(authDir string) string {
	// authDir here is already the parent; file is <authDir>/device_name
	// when called with filepath.Dir(WAGW_AUTH_DIR).
	b, err := os.ReadFile(filepath.Join(authDir, "device_name"))
	if err != nil {
		return ""
	}
	s := string(b)
	// trim + collapse whitespace
	out := make([]byte, 0, len(s))
	prevSpace := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if !prevSpace {
				out = append(out, ' ')
				prevSpace = true
			}
			continue
		}
		if !validDeviceChar(c) {
			return ""
		}
		out = append(out, c)
		prevSpace = false
	}
	// trim trailing space, check length
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	if len(out) == 0 || len(out) > 24 {
		return ""
	}
	return string(out)
}

func validDeviceChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
		c == ' ' || c == '.' || c == '_' || c == '-'
}
