package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/auth"
	"github.com/Demonisreal/dmn-status/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "status.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func verify(t *testing.T, st *store.Store, user, password string) {
	t.Helper()
	name, hash, err := st.Admin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ok, err := auth.Verify(password, hash)
	if name != user || !ok || err != nil {
		t.Fatalf("admin %q, passwort passt %v, err %v", name, ok, err)
	}
}

func TestSetPassword(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	tests := []struct {
		name, user, input string
		ok                bool
	}{
		{"zu_kurz", "leon", "elf zeichen\n", false},
		{"leerzeichen_im_namen", "le on", "lang genuges passwort\n", false},
		{"leerer_name", "", "lang genuges passwort\n", false},
		{"ohne_zeilenende", "leon", "lang genuges passwort", true},
		{"crlf", "leon", "  mit rand am ende  \r\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := setPassword(ctx, st, tt.user, strings.NewReader(tt.input))
			if (err == nil) != tt.ok {
				t.Fatalf("err %v, want ok %v", err, tt.ok)
			}
			if tt.ok {
				verify(t, st, tt.user, strings.TrimRight(tt.input, "\r\n"))
			}
		})
	}

	token, _, err := st.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := setPassword(ctx, st, "leon", strings.NewReader("noch ein passwort\nzweite zeile\n")); err != nil {
		t.Fatal(err)
	}
	verify(t, st, "leon", "noch ein passwort")
	if _, err := st.Session(ctx, token); err == nil {
		t.Error("sitzung ueberlebt den passwortwechsel")
	}
}

func TestBootstrap(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	if err := bootstrap(ctx, st, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Admin(ctx); err == nil {
		t.Fatal("admin ohne umgebung angelegt")
	}
	if err := bootstrap(ctx, st, "leon", "erstes passwort"); err != nil {
		t.Fatal(err)
	}
	verify(t, st, "leon", "erstes passwort")

	// bei jedem weiteren start steht die variable vielleicht noch drin, sie darf nichts aendern
	if err := bootstrap(ctx, st, "jemand", "anderes passwort"); err != nil {
		t.Fatal(err)
	}
	verify(t, st, "leon", "erstes passwort")
}

func TestProbe(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()

	if err := probe(srv.URL + "/healthz"); err != nil {
		t.Fatal(err)
	}
	status = http.StatusServiceUnavailable
	if err := probe(srv.URL + "/healthz"); err == nil {
		t.Fatal("503 als gesund gemeldet")
	}
	srv.Close()
	if err := probe(srv.URL + "/healthz"); err == nil {
		t.Fatal("ohne server gesund")
	}
}
