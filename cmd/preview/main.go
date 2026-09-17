//go:build dev

// Vorschau der oberflaeche mit beispieldaten: go run -tags dev ./cmd/preview
package main

import (
	"log"
	"net/http"
	"time"

	"github.com/Demonisreal/dmn-status/internal/web"
)

func main() {
	log.Print("vorschau auf http://localhost:8081/")
	srv := &http.Server{Addr: "127.0.0.1:8081", Handler: web.Preview(), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
