package check

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fxServer(info string, infoCode int, players string, playersCode int) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(infoCode)
		w.Write([]byte(info))
	})
	mux.HandleFunc("GET /players.json", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(playersCode)
		w.Write([]byte(players))
	})
	return httptest.NewServer(mux)
}

func TestFiveM(t *testing.T) {
	info := fixture(t, "info.json")
	players := fixture(t, "players.json")

	tests := []struct {
		name        string
		info        string
		infoCode    int
		players     string
		playersCode int
		ok          bool
		err         string
		count       int // -1 fuer nil
		max         int // -1 fuer nil
	}{
		{name: "ok", info: info, infoCode: 200, players: players, playersCode: 200, ok: true, count: 3, max: 64},
		{name: "maxclients als zahl", info: fixture(t, "info_maxclients_number.json"), infoCode: 200,
			players: fixture(t, "players_empty.json"), playersCode: 200, ok: true, count: 0, max: 32},
		{name: "info 404", info: "nope", infoCode: 404, playersCode: 404, err: "status 404", count: -1, max: -1},
		{name: "info kaputt", info: `{"server": "FXServer`, infoCode: 200, playersCode: 200, err: "antwort ungültig", count: -1, max: -1},
		{name: "kein fxserver", info: `{"hello": "world"}`, infoCode: 200, playersCode: 200, err: "antwort ungültig", count: -1, max: -1},
		{name: "players gesperrt", info: info, infoCode: 200, players: "forbidden", playersCode: 403, ok: true, count: -1, max: 64},
		{name: "players kaputt", info: info, infoCode: 200, players: "[{", playersCode: 200, ok: true, count: -1, max: 64},
		{name: "maxclients unsinn", info: `{"server": "FXServer", "vars": {"sv_maxClients": "viele"}}`, infoCode: 200,
			players: "[]", playersCode: 200, ok: true, count: 0, max: -1},
		{name: "maxclients 2048", info: `{"server": "FXServer", "vars": {"sv_maxClients": "2048"}}`, infoCode: 200,
			players: "[]", playersCode: 200, ok: true, count: 0, max: 2048},
		{name: "maxclients zu gross", info: `{"server": "FXServer", "vars": {"sv_maxClients": 2049}}`, infoCode: 200,
			players: "[]", playersCode: 200, ok: true, count: 0, max: -1},
		{name: "maxclients negativ", info: `{"server": "FXServer", "vars": {"sv_maxClients": "-1"}}`, infoCode: 200,
			players: "[]", playersCode: 200, ok: true, count: 0, max: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := fxServer(tt.info, tt.infoCode, tt.players, tt.playersCode)
			defer srv.Close()

			tg := Target{Kind: KindFiveM, Address: strings.TrimPrefix(srv.URL, "http://"), TimeoutMs: 5000}
			res := Checker{loopback: true}.Run(context.Background(), tg)
			if res.OK != tt.ok || res.Err != tt.err {
				t.Fatalf("got ok=%v err=%q", res.OK, res.Err)
			}
			if got := deref(res.Players); got != tt.count {
				t.Errorf("players %d, want %d", got, tt.count)
			}
			if got := deref(res.MaxPlayers); got != tt.max {
				t.Errorf("max %d, want %d", got, tt.max)
			}
		})
	}
}

func TestFiveMVersion(t *testing.T) {
	srv := fxServer(fixture(t, "info.json"), 200, "[]", 200)
	defer srv.Close()

	tg := Target{Kind: KindFiveM, Address: strings.TrimPrefix(srv.URL, "http://"), TimeoutMs: 5000}
	res := Checker{loopback: true}.Run(context.Background(), tg)
	if res.Version != "FXServer-master SERVER v1.0.0.7290 linux" {
		t.Fatalf("version %q", res.Version)
	}

	long := "FXServer\n" + strings.Repeat("v", 500)
	if v := version(long); len(v) != maxVersion || strings.Contains(v, "\n") {
		t.Fatalf("version nicht gekuerzt oder bereinigt: %d %q", len(v), v[:20])
	}
	if v := version("FX\u202EServer\u200B v1\u2066.0\u2069\uFEFF"); v != "FXServer v1.0" {
		t.Fatalf("formatzeichen nicht entfernt: %q", v)
	}
	if v := version(strings.Repeat("ü", 100)); utf8.RuneCountInString(v) != maxVersion {
		t.Fatalf("nicht nach runen gekuerzt: %d", utf8.RuneCountInString(v))
	}
}

func TestFiveMBodyLimit(t *testing.T) {
	huge := `{"server": "FXServer", "pad": "` + strings.Repeat("a", fivemLimit) + `"}`
	srv := fxServer(huge, 200, "[]", 200)
	defer srv.Close()

	tg := Target{Kind: KindFiveM, Address: strings.TrimPrefix(srv.URL, "http://"), TimeoutMs: 5000}
	res := Checker{loopback: true}.Run(context.Background(), tg)
	if res.OK || res.Err != "antwort ungültig" {
		t.Fatalf("got ok=%v err=%q", res.OK, res.Err)
	}
}

func deref(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
