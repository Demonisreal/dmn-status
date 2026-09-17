package web

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/auth"
	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/config"
	"github.com/Demonisreal/dmn-status/internal/monitor"
	"github.com/Demonisreal/dmn-status/internal/store"
	"github.com/Demonisreal/dmn-status/internal/testutil"
)

var update = flag.Bool("update", false, "golden-dateien neu schreiben")

const password = "richtiges passwort"

type env struct {
	t     *testing.T
	st    *store.Store
	clock *testutil.Clock
	mgr   *monitor.Manager
	s     *server
	h     http.Handler
}

// newEnv baut den server mit echter datenbank und echtem manager. seed laeuft vor mgr.Start,
// damit der manager vorhandene checks schon in den snapshot uebernimmt.
func newEnv(t *testing.T, cfg config.Config, seed func(*store.Store)) *env {
	t.Helper()
	clock := testutil.NewClock(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "status.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if seed != nil {
		seed(st)
	}

	// der checker bleibt leer, die ziele im test zeigen auf loopback und werden sofort gesperrt
	mgr := monitor.New(st, check.Checker{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		mgr.Wait()
	})

	s := newServer(Deps{Store: st, Monitor: mgr, Config: cfg, Version: "t", Now: clock.Now})
	return &env{t: t, st: st, clock: clock, mgr: mgr, s: s, h: s.handler()}
}

func (e *env) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) get(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return e.serve(req)
}

func postReq(path string, form url.Values, cookies ...*http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req
}

func (e *env) post(path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return e.serve(postReq(path, form, cookies...))
}

func (e *env) admin() {
	e.t.Helper()
	if err := e.st.SetAdmin(context.Background(), "leon", auth.Hash(password)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) session() (*http.Cookie, string) {
	e.t.Helper()
	token, sess, err := e.st.CreateSession(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: e.s.sid, Value: token}, mask(sess.CSRF) //nolint:gosec // cookie fuer den request, attribute wertet der server nicht aus
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func cookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (e *env) login(user, pass, remote string) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := e.get("/admin/login")
	c := cookie(rec, e.s.lcsrf)
	m := csrfField.FindStringSubmatch(rec.Body.String())
	if c == nil || m == nil {
		e.t.Fatalf("loginseite ohne cookie oder token: %s", rec.Body.String())
	}
	req := postReq("/admin/login", url.Values{"csrf": {m[1]}, "username": {user}, "password": {pass}}, c)
	req.RemoteAddr = remote
	return e.serve(req)
}

func target(t *testing.T, st *store.Store, name string, public bool) int64 {
	t.Helper()
	id, err := st.CreateTarget(context.Background(), check.Target{
		Name:          name,
		Kind:          check.KindHTTP,
		Address:       "http://127.0.0.1:9/geheim",
		ExpectStatus:  "200-399",
		IntervalS:     86400,
		TimeoutMs:     1000,
		FailThreshold: 3,
		Public:        public,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLogin(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	e.admin()

	for _, tt := range []struct{ name, user, pass string }{
		{"falsches_passwort", "leon", "falsches passwort"},
		{"falscher_name", "jemand", password},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.login(tt.user, tt.pass, "192.0.2.1:1000")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d", rec.Code)
			}
			if cookie(rec, e.s.sid) != nil {
				t.Error("sitzung trotz falscher daten")
			}
			if !strings.Contains(rec.Body.String(), T("de", "adm.login.bad")) {
				t.Error("fehlermeldung fehlt")
			}
		})
	}

	old, _ := e.session()
	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	rec := e.serve(req)
	m := csrfField.FindStringSubmatch(rec.Body.String())
	req = postReq("/admin/login", url.Values{"csrf": {m[1]}, "username": {"leon"}, "password": {password}}, cookie(rec, e.s.lcsrf), old)
	rec = e.serve(req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	c := cookie(rec, e.s.sid)
	if c == nil || c.Value == old.Value {
		t.Fatal("keine neue sitzung")
	}
	if _, err := e.st.Session(context.Background(), old.Value); !errors.Is(err, store.ErrNotFound) {
		t.Error("alte sitzung lebt nach dem login weiter")
	}
	if rec := e.get("/admin/", c); rec.Code != http.StatusOK {
		t.Errorf("admin mit neuer sitzung: %d", rec.Code)
	}
}

func TestLoginLimit(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	e.admin()

	for i := range 5 {
		if rec := e.login("leon", "falsch", "192.0.2.7:1000"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("versuch %d: status %d", i+1, rec.Code)
		}
	}
	rec := e.login("leon", password, "192.0.2.7:2000")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6. versuch: status %d", rec.Code)
	}
	if ra, _ := strconv.Atoi(rec.Header().Get("Retry-After")); ra < 1 || ra > 900 {
		t.Errorf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
	if rec := e.login("leon", password, "198.51.100.1:1000"); rec.Code != http.StatusSeeOther {
		t.Errorf("andere ip gesperrt: %d", rec.Code)
	}

	e.clock.Advance(loginWindow)
	if rec := e.login("leon", password, "192.0.2.7:1000"); rec.Code != http.StatusSeeOther {
		t.Errorf("sperre nach 15 minuten nicht aufgehoben: %d", rec.Code)
	}
}

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	l := newLimiter()
	for i := range loginGlobal {
		if wait, _ := l.take("ip"+strconv.Itoa(i%10), now, false); wait != 0 {
			t.Fatalf("versuch %d gesperrt", i)
		}
	}
	if wait, first := l.take("neu", now, false); wait == 0 || !first {
		t.Errorf("globales limit greift nicht: %v, erste abweisung %v", wait, first)
	}
	if _, first := l.take("neu2", now, false); first {
		t.Error("zweite abweisung im fenster wieder als erste gemeldet")
	}
	l.forgive("ip0")
	if wait, _ := l.take("neu", now, false); wait != 0 {
		t.Error("erfolgreicher login zaehlt global weiter")
	}
	if wait, _ := l.take("neu", now.Add(loginWindow), false); wait != 0 || len(l.ips) != 1 {
		t.Errorf("nach dem fenster nicht aufgeraeumt, %d eintraege", len(l.ips))
	}

	// ein bekanntes geraet kommt am globalen limit vorbei, nicht am limit seiner ip
	l = newLimiter()
	l.global = window{start: now, n: loginGlobal}
	for i := range loginPerIP {
		if wait, _ := l.take("geraet", now, true); wait != 0 {
			t.Fatalf("bekanntes geraet, versuch %d gesperrt", i+1)
		}
	}
	if wait, _ := l.take("geraet", now, true); wait == 0 {
		t.Error("bekanntes geraet umgeht das ip-limit")
	}
}

func TestLoginBusy(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	e.admin()
	e.s.verify <- struct{}{}
	e.s.verify <- struct{}{}

	// bei vollem semaphor wartet der login, bis der client aufgibt
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	rec := e.serve(e.loginReq(password, "192.0.2.1:1000").WithContext(ctx))
	if rec.Code == http.StatusSeeOther || cookie(rec, e.s.sid) != nil {
		t.Fatalf("login ohne freien platz: %d", rec.Code)
	}

	done := make(chan int)
	go func() { done <- e.serve(e.loginReq(password, "192.0.2.1:1000")).Code }()
	<-e.s.verify
	if code := <-done; code != http.StatusSeeOther {
		t.Errorf("nach freiem platz: %d", code)
	}
}

func TestLoginFloodKeepsSemaphore(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	e.admin()
	e.s.verify = make(chan struct{}, 1)
	e.burn("192.0.2.66:1000")

	flood := make([]*http.Request, 200)
	for i := range flood {
		flood[i] = e.loginReq("falsch", "192.0.2.66:1000")
	}
	legit := e.loginReq(password, "198.51.100.7:1000")

	// solange der platz belegt ist, darf die gesperrte ip nur am limit haengen bleiben: eine
	// Retry-After von einer sekunde hiesse, sie war schon am semaphor
	e.s.verify <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if rec := e.serve(e.loginReq("falsch", "192.0.2.66:1000").WithContext(ctx)); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "1" {
		t.Fatalf("gesperrte ip bei belegtem semaphor: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	<-e.s.verify

	type result struct {
		code  int
		retry string
	}
	results := make(chan result, len(flood))
	var wg sync.WaitGroup
	for _, req := range flood {
		wg.Go(func() {
			rec := e.serve(req)
			results <- result{rec.Code, rec.Header().Get("Retry-After")}
		})
	}
	rec := e.serve(legit)
	wg.Wait()
	close(results)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("login waehrend der flut: %d", rec.Code)
	}
	for res := range results {
		if res.code != http.StatusTooManyRequests || res.retry == "1" {
			t.Fatalf("gesperrte ip bekommt %d, Retry-After %q", res.code, res.retry)
		}
	}
	if len(e.s.verify) != 0 {
		t.Error("semaphor nach der flut nicht frei")
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	sid, token := e.session()

	_, foreign := e.session()
	for _, tt := range []struct {
		name string
		csrf []string
	}{
		{"ohne", nil},
		{"leer", []string{""}},
		{"kaputt", []string{"%%%"}},
		{"von_anderer_sitzung", []string{foreign}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := e.post("/admin/testmail", url.Values{"csrf": tt.csrf}, sid)
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/?ok=csrf" {
				t.Errorf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
			}
		})
	}
	if body := e.get("/admin/?ok=csrf", sid).Body.String(); !strings.Contains(body, T("de", "adm.csrf")) {
		t.Error("liste ohne csrf-hinweis")
	}

	rec := e.post("/admin/testmail", url.Values{"csrf": {token}}, sid)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/?ok=keinmail" {
		t.Errorf("mit token: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// login ohne das double-submit-cookie: die seite kommt mit hinweis und neuem token zurueck
	e.admin()
	page := e.get("/admin/login")
	m := csrfField.FindStringSubmatch(page.Body.String())
	rec = e.post("/admin/login", url.Values{"csrf": {m[1]}, "username": {"leon"}, "password": {password}})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), T("de", "adm.csrf")) {
		t.Fatalf("login ohne cookie: %d", rec.Code)
	}
	if cookie(rec, e.s.sid) != nil {
		t.Error("sitzung trotz fehlendem cookie")
	}
	m, c := csrfField.FindStringSubmatch(rec.Body.String()), cookie(rec, e.s.lcsrf)
	if m == nil || c == nil {
		t.Fatal("kein neues token")
	}
	rec = e.post("/admin/login", url.Values{"csrf": {m[1]}, "username": {"leon"}, "password": {password}}, c)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("login mit neuem token: %d", rec.Code)
	}
}

func TestMask(t *testing.T) {
	a, b := mask("token-abc"), mask("token-abc")
	if a == b {
		t.Error("maskiertes token gleich in zwei antworten")
	}
	if !validToken(a, "token-abc") || !validToken(b, "token-abc") || validToken(a, "token-abd") || validToken(a, "") {
		t.Error("demaskieren passt nicht")
	}
}

func TestCrossOrigin(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	sid, token := e.session()

	for _, h := range []struct{ key, value string }{
		{"Origin", "https://fremd.example"},
		{"Sec-Fetch-Site", "cross-site"},
	} {
		for _, path := range []string{"/admin/login", "/admin/testmail"} {
			req := postReq(path, url.Values{"csrf": {token}}, sid)
			req.Host = "status.example"
			req.Header.Set(h.key, h.value)
			if rec := e.serve(req); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: status %d", h.key, path, rec.Code)
			}
		}
	}
}

func TestCookieAttributes(t *testing.T) {
	for _, secure := range []bool{true, false} {
		t.Run(strconv.FormatBool(secure), func(t *testing.T) {
			e := newEnv(t, config.Config{CookieSecure: secure}, nil)
			e.admin()
			rec := e.login("leon", password, "192.0.2.1:1000")

			name := "sid"
			if secure {
				name = "__Host-sid"
			}
			var raw string
			for _, v := range rec.Header().Values("Set-Cookie") {
				if strings.HasPrefix(v, name+"=") {
					raw = v
				}
			}
			if raw == "" {
				t.Fatalf("kein cookie %s: %v", name, rec.Header().Values("Set-Cookie"))
			}
			for _, want := range []string{"Path=/", "HttpOnly", "SameSite=Lax"} {
				if !strings.Contains(raw, want) {
					t.Errorf("%q fehlt in %q", want, raw)
				}
			}
			if strings.Contains(raw, "Secure") != secure || strings.Contains(raw, "Domain") {
				t.Errorf("cookie %q", raw)
			}

			dev := cookie(rec, e.s.dev)
			if dev == nil {
				t.Fatal("kein geraete-cookie")
			}
			if want := map[bool]string{true: "__Host-dev", false: "dev"}[secure]; dev.Name != want {
				t.Errorf("geraete-cookie heisst %q", dev.Name)
			}
			if dev.Path != "/" || !dev.HttpOnly || dev.Secure != secure || dev.SameSite != http.SameSiteStrictMode ||
				dev.MaxAge != 90*24*60*60 || dev.Domain != "" {
				t.Errorf("geraete-cookie %+v", dev)
			}
		})
	}
}

func TestSession(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	ctx := context.Background()

	if rec := e.get("/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Errorf("ohne cookie: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	sid, token := e.session()
	before, _ := e.st.Session(ctx, sid.Value)
	e.clock.Advance(30 * time.Second)
	e.get("/admin/", sid)
	if s, _ := e.st.Session(ctx, sid.Value); !s.LastSeen.Equal(before.LastSeen) {
		t.Error("sitzung nach 30 sekunden schon wieder verlaengert")
	}
	e.clock.Advance(time.Minute)
	e.get("/admin/", sid)
	if s, _ := e.st.Session(ctx, sid.Value); !s.LastSeen.After(before.LastSeen) {
		t.Error("sitzung nach einer minute nicht verlaengert")
	}

	rec := e.post("/admin/logout", url.Values{"csrf": {token}}, sid)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("logout: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if c := cookie(rec, e.s.sid); c == nil || c.MaxAge >= 0 {
		t.Error("cookie nicht geloescht")
	}
	if _, err := e.st.Session(ctx, sid.Value); !errors.Is(err, store.ErrNotFound) {
		t.Error("sitzung nach logout noch da")
	}

	expired, _ := e.session()
	e.clock.Advance(store.SessionIdle + time.Minute)
	if rec := e.get("/admin/ziel/neu", expired); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Errorf("abgelaufen: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestPublicPages(t *testing.T) {
	var pub, hidden int64
	e := newEnv(t, config.Config{}, func(st *store.Store) {
		pub = target(t, st, "Website", true)
		hidden = target(t, st, "Intern", false)
		at := time.Date(2026, 9, 17, 11, 50, 0, 0, time.UTC)
		for _, id := range []int64{pub, hidden} {
			if err := st.InsertCheck(context.Background(), id, at, check.Result{StatusCode: 502, Err: "status 502", LatencyMs: 40}); err != nil {
				t.Fatal(err)
			}
		}
	})

	for _, path := range []string{"/", "/en/", "/ziel/1", "/en/ziel/1?r=7d", "/ziel/1?r=30d", "/ziel/1?r=quatsch"} {
		rec := e.get(path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		for _, secret := range []string{"127.0.0.1", "geheim", "status 502", "Intern"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s enthaelt %q", path, secret)
			}
		}
	}
	if body := e.get("/").Body.String(); !strings.Contains(body, "nicht erreichbar") || !strings.Contains(body, `href="/en/"`) {
		t.Error("statusseite ohne zustand oder sprachlink")
	}
	if body := e.get("/en/ziel/1?r=7d").Body.String(); !strings.Contains(body, `href="/ziel/1?r=7d"`) {
		t.Error("sprachlink der detailseite ohne zeitraum")
	}

	h := strconv.FormatInt(hidden, 10)
	for _, path := range []string{"/ziel/" + h, "/en/ziel/" + h, "/ziel/abc", "/ziel/0", "/ziel/-1", "/ziel/+1", "/ziel/01", "/ziel/999", "/gibtsnicht", "/admin/logout"} {
		rec := e.get(path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", path, rec.Code)
		}
		if strings.Contains(path, "/en/") && !strings.Contains(rec.Body.String(), `lang="en"`) {
			t.Errorf("%s: 404 nicht englisch", path)
		}
	}
	if rec := e.get("/admin"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/admin/" {
		t.Errorf("/admin: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAPI(t *testing.T) {
	e := newEnv(t, config.Config{}, func(st *store.Store) {
		id := target(t, st, "Website", true)
		target(t, st, "Intern", false)
		if err := st.InsertCheck(context.Background(), id, time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), check.Result{Err: "status 502"}); err != nil {
			t.Fatal(err)
		}
	})

	rec := e.get("/api/status.json")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, secret := range []string{"127.0.0.1", "status 502", "Intern"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("api enthaelt %q", secret)
		}
	}
	var out struct {
		Targets []struct {
			Name      string              `json:"name"`
			Kind      string              `json:"kind"`
			State     string              `json:"state"`
			Uptime    map[string]*float64 `json:"uptime"`
			CheckedAt *time.Time          `json:"checked_at"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Targets) != 1 {
		t.Fatalf("%d ziele", len(out.Targets))
	}
	got := out.Targets[0]
	if got.Name != "Website" || got.Kind != "http" || got.State != "down" || got.CheckedAt == nil ||
		got.Uptime["24h"] == nil || *got.Uptime["24h"] != 0 {
		t.Errorf("%s", rec.Body.String())
	}
}

var wantHeaders = map[string]string{
	"Content-Security-Policy":    csp,
	"X-Frame-Options":            "DENY",
	"X-Content-Type-Options":     "nosniff",
	"Referrer-Policy":            "same-origin",
	"Cross-Origin-Opener-Policy": "same-origin",
}

func TestHeaders(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	sid, _ := e.session()
	boom := e.s.headers(e.s.recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("geheimes detail")
	})))

	tests := []struct {
		path   string
		h      http.Handler
		status int
		cache  string
		ctype  string
	}{
		{"/", e.h, 200, "no-cache", "text/html; charset=utf-8"},
		{"/api/status.json", e.h, 200, "no-cache", "application/json"},
		{"/gibtsnicht", e.h, 404, "no-cache", "text/html; charset=utf-8"},
		{"/admin/login", e.h, 200, "no-store", "text/html; charset=utf-8"},
		{"/admin/", e.h, 200, "no-store", "text/html; charset=utf-8"},
		{"/admin/gibtsnicht", e.h, 404, "no-store", "text/html; charset=utf-8"},
		{"/healthz", e.h, 200, "no-store", "text/plain; charset=utf-8"},
		{"/static/app.css", e.h, 200, "no-cache", "text/css; charset=utf-8"},
		{"/static/app.css?v=t", e.h, 200, "public, max-age=31536000, immutable", "text/css; charset=utf-8"},
		{"/static/refresh.js?v=t", e.h, 200, "public, max-age=31536000, immutable", "text/javascript; charset=utf-8"},
		{"/static/icon.svg?v=t", e.h, 200, "public, max-age=31536000, immutable", "image/svg+xml"},
		{"/static/", e.h, 404, "no-cache", "text/html; charset=utf-8"},
		{"/static/gibtsnicht.css", e.h, 404, "no-cache", "text/html; charset=utf-8"},
		{"/static/gibtsnicht.css?v=t", e.h, 404, "no-cache", "text/html; charset=utf-8"},
		{"/en/kaputt", boom, 500, "no-cache", "text/html; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.AddCookie(sid)
			rec := httptest.NewRecorder()
			tt.h.ServeHTTP(rec, req)

			if rec.Code != tt.status {
				t.Fatalf("status %d", rec.Code)
			}
			for k, v := range wantHeaders {
				if got := rec.Header().Get(k); got != v {
					t.Errorf("%s: %q", k, got)
				}
			}
			if _, ok := rec.Header()["Permissions-Policy"]; !ok {
				t.Error("Permissions-Policy fehlt")
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Errorf("Cache-Control %q", got)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.ctype {
				t.Errorf("Content-Type %q", got)
			}
			if strings.Contains(rec.Body.String(), "geheimes detail") {
				t.Error("panic-text an den client")
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	if rec := e.get("/healthz"); rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
	e.st.Close()
	if rec := e.get("/healthz"); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "sql") {
		t.Errorf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestMetrics(t *testing.T) {
	if rec := newEnv(t, config.Config{}, nil).get("/metrics"); rec.Code != http.StatusNotFound {
		t.Errorf("ohne METRICS_TOKEN: %d", rec.Code)
	}

	token := strings.Repeat("m", 40)
	e := newEnv(t, config.Config{MetricsToken: token}, func(st *store.Store) {
		ctx := context.Background()
		at := time.Date(2026, 9, 17, 11, 50, 0, 0, time.UTC)
		up := target(t, st, `Web "eins" \ zwei`, true)
		down := target(t, st, "Intern", false)
		target(t, st, "Neu", true)
		if err := st.InsertCheck(ctx, up, at, check.Result{OK: true, LatencyMs: 142}); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertCheck(ctx, down, at, check.Result{Err: "status 502", LatencyMs: 12}); err != nil {
			t.Fatal(err)
		}
	})

	for _, auth := range []string{"", "Bearer ", "Bearer falsch", "Basic " + token, token} {
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if rec := e.serve(req); rec.Code != http.StatusUnauthorized {
			t.Errorf("%q: status %d", auth, rec.Code)
		}
	}

	// die runner uebernehmen den letzten check erst beim anlaufen in den snapshot
	for deadline := time.Now().Add(5 * time.Second); len(e.mgr.Snapshot()) < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("snapshot bleibt leer")
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := e.serve(req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	golden := filepath.Join("testdata", "metrics.golden")
	if *update {
		if err := os.WriteFile(golden, rec.Body.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.Body.String(); got != string(want) {
		t.Errorf("metrics weicht ab:\n%s", got)
	}
}

func TestAdminForm(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	sid, token := e.session()
	ctx := context.Background()

	valid := url.Values{
		"csrf":           {token},
		"name":           {"  Datenbank "},
		"kind":           {"tcp"},
		"address":        {"127.0.0.1:9"},
		"interval_s":     {"86400"},
		"timeout_ms":     {"1000"},
		"fail_threshold": {"3"},
		"public":         {"1"},
	}

	bad := url.Values{}
	for k, v := range valid {
		bad[k] = v
	}
	bad.Set("name", "")
	bad.Set("interval_s", "abc")
	bad.Set("keyword", "nur http")
	rec := e.post("/admin/ziel/neu", bad, sid)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ungueltig: status %d", rec.Code)
	}
	for _, want := range []string{"Ganze Zahl erwartet", "1 bis 60 Zeichen", "Nur bei HTTP", `aria-invalid="true"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%q fehlt", want)
		}
	}
	if list, _ := e.st.Targets(ctx); len(list) != 0 {
		t.Fatal("ungueltiges ziel gespeichert")
	}

	rec = e.post("/admin/ziel/neu", valid, sid)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/?ok=gespeichert" {
		t.Fatalf("anlegen: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	list, _ := e.st.Targets(ctx)
	if len(list) != 1 || list[0].Name != "Datenbank" || !list[0].Public {
		t.Fatalf("gespeichert: %+v", list)
	}
	id := list[0].ID
	path := "/admin/ziel/" + strconv.FormatInt(id, 10)
	// CheckNow findet nur einen runner, wenn Reload ihn gestartet hat
	if !e.mgr.CheckNow(id) {
		t.Error("kein runner nach dem anlegen")
	}

	if body := e.get("/admin/?ok=gespeichert", sid).Body.String(); !strings.Contains(body, T("de", "adm.saved")) || !strings.Contains(body, "127.0.0.1:9") {
		t.Error("liste ohne flash oder adresse")
	}
	if body := e.get("/admin/?ok=%3Cb%3Ex", sid).Body.String(); strings.Contains(body, `role="status"`) {
		t.Error("flash aus beliebigem parameter")
	}

	// die sperrfrist greift erst, wenn ein anstoss beim runner angekommen ist
	deadline := time.Now().Add(2 * time.Second)
	for {
		loc := e.post(path+"/pruefen", url.Values{"csrf": {token}}, sid).Header().Get("Location")
		if loc == "/admin/?ok=zufrueh" {
			break
		}
		if loc != "/admin/?ok=geprueft" || time.Now().After(deadline) {
			t.Fatalf("pruefen ohne sperrfrist: %q", loc)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if rec := e.post(path+"/pause", url.Values{"csrf": {token}}, sid); rec.Code != http.StatusSeeOther {
		t.Fatalf("pause: %d", rec.Code)
	}
	if e.mgr.CheckNow(id) {
		t.Error("runner laeuft trotz pause")
	}
	if rec := e.post(path+"/pruefen", url.Values{"csrf": {token}}, sid); rec.Header().Get("Location") != "/admin/?ok=pausiert" {
		t.Errorf("pruefen pausiert: %q", rec.Header().Get("Location"))
	}

	edit := url.Values{}
	for k, v := range valid {
		edit[k] = v
	}
	edit.Set("name", "Datenbank neu")
	if rec := e.post(path, edit, sid); rec.Code != http.StatusSeeOther {
		t.Fatalf("bearbeiten: %d", rec.Code)
	}
	if got, _ := e.st.Target(ctx, id); got.Name != "Datenbank neu" || got.Paused {
		t.Errorf("nach bearbeiten: %+v", got)
	}
	if !e.mgr.CheckNow(id) {
		t.Error("kein runner nach dem bearbeiten")
	}
	if rec := e.post("/admin/ziel/999", edit, sid); rec.Code != http.StatusNotFound {
		t.Errorf("unbekanntes ziel bearbeiten: %d", rec.Code)
	}
	if rec := e.get("/admin/ziel/999", sid); rec.Code != http.StatusNotFound {
		t.Errorf("unbekanntes ziel: %d", rec.Code)
	}

	if rec := e.post(path+"/loeschen", url.Values{"csrf": {token}}, sid); rec.Header().Get("Location") != "/admin/?ok=geloescht" {
		t.Fatalf("loeschen: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := e.st.Target(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Error("ziel nach loeschen noch da")
	}
	if e.mgr.CheckNow(id) {
		t.Error("runner nach loeschen noch da")
	}
}

func TestPauseEndsIncident(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	sid, token := e.session()
	ctx := context.Background()

	form := url.Values{
		"csrf":           {token},
		"name":           {"Datenbank"},
		"kind":           {"tcp"},
		"address":        {"127.0.0.1:9"},
		"interval_s":     {"86400"},
		"timeout_ms":     {"1000"},
		"fail_threshold": {"3"},
		"public":         {"1"},
	}
	if rec := e.post("/admin/ziel/neu", form, sid); rec.Code != http.StatusSeeOther {
		t.Fatalf("anlegen: %d", rec.Code)
	}
	list, _ := e.st.Targets(ctx)
	id := list[0].ID
	path := "/admin/ziel/" + strconv.FormatInt(id, 10)

	paused := url.Values{}
	for k, v := range form {
		paused[k] = v
	}
	paused.Set("paused", "1")

	for name, pause := range map[string]func() *httptest.ResponseRecorder{
		"knopf":    func() *httptest.ResponseRecorder { return e.post(path+"/pause", url.Values{"csrf": {token}}, sid) },
		"formular": func() *httptest.ResponseRecorder { return e.post(path, paused, sid) },
	} {
		// fortsetzen ueber das formular ohne paused
		if rec := e.post(path, form, sid); rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: fortsetzen %d", name, rec.Code)
		}
		inc, err := e.st.OpenIncident(ctx, id, e.clock.Now(), "verbindung abgelehnt")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		e.clock.Advance(5 * time.Minute)
		if rec := pause(); rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: pause %d", name, rec.Code)
		}

		incs, err := e.st.RecentIncidents(ctx, 10, e.clock.Now().Add(-time.Hour), true)
		if err != nil || len(incs) == 0 {
			t.Fatalf("%s: %+v %v", name, incs, err)
		}
		got := incs[0]
		if got.ID != inc || !got.EndedAt.Equal(e.clock.Now()) || !got.MailedDown || !got.MailedUp {
			t.Errorf("%s: vorfall %+v", name, got)
		}
		e.clock.Advance(time.Minute)
	}
}

func TestFormTooLarge(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	sid, token := e.session()
	rec := e.post("/admin/ziel/neu", url.Values{"csrf": {token}, "name": {strings.Repeat("x", maxForm)}}, sid)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name   string
		trust  bool
		remote string
		xff    []string
		want   string
	}{
		{"direkt", false, "203.0.113.5:4000", nil, "203.0.113.5"},
		{"xff_ohne_trust", false, "172.18.0.2:4000", []string{"198.51.100.9"}, "172.18.0.2"},
		{"proxy", true, "172.18.0.2:4000", []string{"1.1.1.1, 198.51.100.9"}, "198.51.100.9"},
		{"mehrere_header", true, "172.18.0.2:4000", []string{"1.1.1.1", "198.51.100.9"}, "198.51.100.9"},
		{"oeffentlicher_absender", true, "203.0.113.5:4000", []string{"198.51.100.9"}, "203.0.113.5"},
		{"privat_ausserhalb_des_netzes", true, "10.0.0.5:4000", []string{"198.51.100.9"}, "10.0.0.5"},
		{"muell", true, "172.18.0.2:4000", []string{"kein ip"}, "172.18.0.2"},
		{"ipv6_64", false, "[2001:db8:1:2:3:4:5:6]:4000", nil, "2001:db8:1:2::/64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{}
			if tt.trust {
				cfg.TrustedProxy = dockerNet
			}
			s := &server{Deps: Deps{Config: cfg}}
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if got := s.clientIP(req); got != tt.want {
				t.Errorf("%q, wollte %q", got, tt.want)
			}
		})
	}
}

func TestSlots(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 5, 0, 0, time.UTC)
	points := []store.Point{
		{At: now.Add(-48 * time.Hour), Avg: 1},
		{At: time.Date(2026, 9, 16, 12, 10, 0, 0, time.UTC), Avg: 10},
		{At: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC), Avg: 20},
	}
	ms := slots(points, now, 24*time.Hour, 144)
	if ms[0] != 10 || ms[143] != 20 || ms[1] != -1 {
		t.Errorf("erster %d, letzter %d, zweiter %d", ms[0], ms[143], ms[1])
	}
}

func TestPublicCache(t *testing.T) {
	e := newEnv(t, config.Config{}, func(st *store.Store) {
		target(t, st, "Website", true)
	})

	for i, path := range []string{"/", "/en/", "/api/status.json"} {
		t.Run(path, func(t *testing.T) {
			first := e.get(path)
			name := "Neu" + strconv.Itoa(i)
			target(t, e.st, name, true)

			e.clock.Advance(cacheTTL - time.Second)
			second := e.get(path)
			if second.Body.String() != first.Body.String() || second.Header().Get("Cache-Control") != "no-cache" {
				t.Errorf("zweiter aufruf nicht aus dem cache, Cache-Control %q", second.Header().Get("Cache-Control"))
			}
			if second.Header().Get("Content-Type") != first.Header().Get("Content-Type") {
				t.Errorf("Content-Type %q, vorher %q", second.Header().Get("Content-Type"), first.Header().Get("Content-Type"))
			}

			e.clock.Advance(time.Second)
			if body := e.get(path).Body.String(); !strings.Contains(body, name) {
				t.Errorf("nach %v noch der alte stand", cacheTTL)
			}
		})
	}
}

func TestCacheClearedOnChange(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	sid, token := e.session()
	ctx := context.Background()

	form := url.Values{
		"csrf":           {token},
		"name":           {"Geheimprojekt"},
		"kind":           {"tcp"},
		"address":        {"127.0.0.1:9"},
		"interval_s":     {"86400"},
		"timeout_ms":     {"1000"},
		"fail_threshold": {"3"},
		"public":         {"1"},
	}
	if rec := e.post("/admin/ziel/neu", form, sid); rec.Code != http.StatusSeeOther {
		t.Fatalf("anlegen: %d", rec.Code)
	}
	list, _ := e.st.Targets(ctx)
	path := "/admin/ziel/" + strconv.FormatInt(list[0].ID, 10)

	visible := func() bool {
		return strings.Contains(e.get("/").Body.String(), "Geheimprojekt") &&
			strings.Contains(e.get("/api/status.json").Body.String(), "Geheimprojekt")
	}
	if !visible() {
		t.Fatal("oeffentliches ziel fehlt")
	}

	form.Del("public")
	if rec := e.post(path, form, sid); rec.Code != http.StatusSeeOther {
		t.Fatalf("intern stellen: %d", rec.Code)
	}
	if visible() {
		t.Error("intern gestelltes ziel kommt noch aus dem cache")
	}

	form.Set("public", "1")
	if rec := e.post(path, form, sid); rec.Code != http.StatusSeeOther {
		t.Fatalf("oeffentlich stellen: %d", rec.Code)
	}
	before := e.get("/").Body.String()
	if rec := e.post(path+"/pause", url.Values{"csrf": {token}}, sid); rec.Code != http.StatusSeeOther {
		t.Fatalf("pause: %d", rec.Code)
	}
	if e.get("/").Body.String() == before {
		t.Error("pause kommt nicht auf der seite an")
	}

	if rec := e.post(path+"/loeschen", url.Values{"csrf": {token}}, sid); rec.Code != http.StatusSeeOther {
		t.Fatalf("loeschen: %d", rec.Code)
	}
	if strings.Contains(e.get("/").Body.String(), "Geheimprojekt") {
		t.Error("geloeschtes ziel kommt noch aus dem cache")
	}
}

func TestAdminFormBadNumber(t *testing.T) {
	var id int64
	e := newEnv(t, config.Config{}, func(st *store.Store) {
		id = target(t, st, "Website", true)
	})
	sid, token := e.session()
	form := url.Values{
		"csrf":           {token},
		"name":           {"Website"},
		"kind":           {"http"},
		"address":        {"http://127.0.0.1:9/geheim"},
		"expect_status":  {"200-399"},
		"interval_s":     {"abc"},
		"timeout_ms":     {"1000"},
		"fail_threshold": {"3"},
	}

	for _, tt := range []struct{ path, want string }{
		{"/admin/ziel/" + strconv.FormatInt(id, 10), `name="interval_s"[^>]*value="86400"`},
		{"/admin/ziel/neu", `name="interval_s"[^>]*value="60"`},
	} {
		t.Run(tt.path, func(t *testing.T) {
			rec := e.post(tt.path, form, sid)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status %d", rec.Code)
			}
			body := rec.Body.String()
			if !regexp.MustCompile(tt.want).MatchString(body) {
				t.Errorf("%s fehlt", tt.want)
			}
			if !strings.Contains(body, "Ganze Zahl erwartet") || strings.Contains(body, `f-timeout-err`) {
				t.Error("falsche fehlermeldungen")
			}
		})
	}
}

func TestFlashTexts(t *testing.T) {
	for ok, f := range flashes {
		if _, found := texts["de"][f.key]; !found {
			t.Errorf("?ok=%s: text %s fehlt", ok, f.key)
		}
	}
}
