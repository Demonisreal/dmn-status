// fakefx spielt einen FXServer mit /info.json und /players.json nach, damit die
// Statusseite ohne echten FiveM-Server ausprobiert werden kann.
//
//	go run ./internal/check/fakefx -addr 127.0.0.1:30120 -players 12
//
// info.json und players.json sind Kopien aus ../testdata, embed kommt nicht ueber ../ hinaus.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"maps"
	"net/http"
	"time"
)

var (
	//go:embed info.json
	info []byte
	//go:embed players.json
	players []byte
)

func main() {
	addr := flag.String("addr", "127.0.0.1:30120", "listen-adresse")
	n := flag.Int("players", 3, "anzahl spieler in /players.json")
	down := flag.Bool("down", false, "auf alles mit 503 antworten")
	slow := flag.Duration("slow", 0, "verzoegerung vor jeder antwort")
	flag.Parse()

	list, err := playerList(*n)
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /info.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, info)
	})
	mux.HandleFunc("GET /players.json", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, list)
	})

	h := http.Handler(mux)
	if *down {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		})
	}

	log.Printf("fakefx auf http://%s (spieler %d, down %v, slow %v)", *addr, *n, *down, *slow)
	srv := &http.Server{Addr: *addr, Handler: delay(h, *slow), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func delay(h http.Handler, d time.Duration) http.Handler {
	if d <= 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(d):
			h.ServeHTTP(w, r)
		case <-r.Context().Done():
		}
	})
}

// playerList wiederholt die drei Vorlagen, bis n Spieler zusammen sind
func playerList(n int) ([]byte, error) {
	var tmpl []map[string]any
	if err := json.Unmarshal(players, &tmpl); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, max(n, 0))
	for i := range max(n, 0) {
		p := maps.Clone(tmpl[i%len(tmpl)])
		p["id"] = i + 1
		if i >= len(tmpl) {
			p["name"] = fmt.Sprintf("%s %d", p["name"], i+1)
		}
		out = append(out, p)
	}
	return json.Marshal(out)
}

func writeJSON(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}
