package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/Demonisreal/dmn-status/internal/auth"
	"github.com/Demonisreal/dmn-status/internal/config"
	"github.com/Demonisreal/dmn-status/internal/store"
)

var dockerNet = netip.MustParsePrefix("172.18.0.0/16")

func (e *env) loginReq(pass, remote string) *http.Request {
	e.t.Helper()
	page := e.get("/admin/login")
	m := csrfField.FindStringSubmatch(page.Body.String())
	c := cookie(page, e.s.lcsrf)
	if m == nil || c == nil {
		e.t.Fatal("loginseite ohne token")
	}
	req := postReq("/admin/login", url.Values{"csrf": {m[1]}, "username": {"leon"}, "password": {pass}}, c)
	req.RemoteAddr = remote
	return req
}

// loginFrom schickt einen login mit frei waehlbaren X-Forwarded-For-Headern
func (e *env) loginFrom(pass, remote string, xff ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := e.loginReq(pass, remote)
	for _, v := range xff {
		req.Header.Add("X-Forwarded-For", v)
	}
	return e.serve(req)
}

func (e *env) burn(remote string, xff ...string) {
	e.t.Helper()
	for i := range loginPerIP {
		if rec := e.loginFrom("falsch", remote, xff...); rec.Code != http.StatusUnauthorized {
			e.t.Fatalf("versuch %d: status %d", i+1, rec.Code)
		}
	}
}

func TestLoginLimitIgnoresSpoofedXFF(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	e.admin()

	// ohne TRUSTED_PROXY darf ein wechselnder header keine neuen versuche freischalten
	for i, xff := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4", "198.51.100.5"} {
		if rec := e.loginFrom("falsch", "203.0.113.9:1000", xff); rec.Code != http.StatusUnauthorized {
			t.Fatalf("versuch %d: status %d", i+1, rec.Code)
		}
	}
	if rec := e.loginFrom(password, "203.0.113.9:1001", "198.51.100.6"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("6. versuch mit neuem header: status %d", rec.Code)
	}
}

func TestLoginLimitBehindProxy(t *testing.T) {
	e := newEnv(t, config.Config{TrustedProxy: dockerNet}, nil)
	e.admin()
	const proxy = "172.18.0.2:4000"

	// der client kann nur eintraege vor dem letzten faelschen, der letzte kommt vom proxy
	e.burn(proxy, "10.9.9.9, 198.51.100.7")
	if rec := e.loginFrom(password, proxy, "1.1.1.1, 198.51.100.7"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("gefaelschter vorderer eintrag: status %d", rec.Code)
	}
	if rec := e.loginFrom(password, proxy, "198.51.100.8"); rec.Code != http.StatusSeeOther {
		t.Errorf("anderer client hinter dem proxy gesperrt: %d", rec.Code)
	}
	// ohne header zaehlt der proxy selbst, der ist noch frei
	if rec := e.loginFrom(password, proxy); rec.Code != http.StatusSeeOther {
		t.Errorf("proxy ohne header: %d", rec.Code)
	}
}

func TestLoginLimitIPv6Prefix(t *testing.T) {
	tests := []struct {
		name    string
		trust   bool
		remote  string
		xff     string
		next    string
		nextXFF string
		want    int
	}{
		{"gleiches_64", false, "[2001:db8:1:2::1]:1000", "", "[2001:db8:1:2:ffff:ffff:ffff:ffff]:1000", "", http.StatusTooManyRequests},
		{"anderes_64", false, "[2001:db8:1:2::1]:1000", "", "[2001:db8:1:3::1]:1000", "", http.StatusSeeOther},
		{"zone_egal", false, "[2001:db8:1:2::1%eth0]:1000", "", "[2001:db8:1:2::2]:1000", "", http.StatusTooManyRequests},
		{"ipv4_gemappt", false, "[::ffff:192.0.2.7]:1000", "", "192.0.2.7:2000", "", http.StatusTooManyRequests},
		{"proxy_ipv6", true, "172.18.0.2:4000", "2001:db8:5:6::1", "172.18.0.2:4000", "2001:db8:5:6:abcd::9", http.StatusTooManyRequests},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{}
			if tt.trust {
				cfg.TrustedProxy = dockerNet
			}
			e := newEnv(t, cfg, nil)
			e.admin()
			var xff, nextXFF []string
			if tt.xff != "" {
				xff = []string{tt.xff}
			}
			if tt.nextXFF != "" {
				nextXFF = []string{tt.nextXFF}
			}
			e.burn(tt.remote, xff...)
			if rec := e.loginFrom(password, tt.next, nextXFF...); rec.Code != tt.want {
				t.Errorf("status %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestKnownDevice(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	e.admin()

	rec := e.loginFrom(password, "192.0.2.1:1000")
	dev := cookie(rec, e.s.dev)
	if rec.Code != http.StatusSeeOther || dev == nil {
		t.Fatalf("login: %d, geraete-cookie %v", rec.Code, dev)
	}
	full := func() { e.s.limit.global = window{start: e.clock.Now(), n: loginGlobal} }
	with := func(c *http.Cookie, pass, remote string) *httptest.ResponseRecorder {
		req := e.loginReq(pass, remote)
		if c != nil {
			req.AddCookie(c)
		}
		return e.serve(req)
	}

	full()
	rec = with(dev, password, "198.51.100.1:1000")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("bekanntes geraet bei vollem globalem limit: %d", rec.Code)
	}
	if cookie(rec, e.s.dev) != nil {
		t.Error("bekanntes geraet bekommt ein neues cookie")
	}

	for _, tt := range []struct {
		name, cookie, value string
	}{
		{"ohne_cookie", "", ""},
		{"fremdes_token", e.s.dev, "AAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"falscher_name", e.s.sid, dev.Value},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var c *http.Cookie
			if tt.cookie != "" {
				c = &http.Cookie{Name: tt.cookie, Value: tt.value} //nolint:gosec // cookie fuer den request, attribute wertet der server nicht aus
			}
			if rec := with(c, password, "198.51.100.2:1000"); rec.Code != http.StatusTooManyRequests {
				t.Errorf("status %d", rec.Code)
			}
		})
	}

	// das ip-limit gilt auch fuer ein bekanntes geraet
	for i := range loginPerIP {
		if rec := with(dev, "falsch", "198.51.100.3:1000"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("versuch %d: %d", i+1, rec.Code)
		}
	}
	if rec := with(dev, password, "198.51.100.3:1000"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("ip-limit mit geraete-cookie: %d", rec.Code)
	}

	t.Run("abgelaufen", func(t *testing.T) {
		e.clock.Advance(store.DeviceMax)
		full()
		if rec := with(dev, password, "198.51.100.4:1000"); rec.Code != http.StatusTooManyRequests {
			t.Errorf("status %d", rec.Code)
		}
	})

	t.Run("passwortwechsel", func(t *testing.T) {
		e.s.limit = newLimiter()
		rec := e.loginFrom(password, "192.0.2.1:1000")
		fresh := cookie(rec, e.s.dev)
		if fresh == nil {
			t.Fatal("kein geraete-cookie")
		}
		if err := e.st.SetAdmin(context.Background(), "leon", auth.Hash(password)); err != nil {
			t.Fatal(err)
		}
		full()
		if rec := with(fresh, password, "198.51.100.5:1000"); rec.Code != http.StatusTooManyRequests {
			t.Errorf("geraet nach passwortwechsel noch bekannt: %d", rec.Code)
		}
	})
}

func TestLoginLog(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	e := newEnv(t, config.Config{}, nil)
	e.admin()
	e.loginFrom("falsches passwort", "192.0.2.1:1000")
	e.loginFrom(password, "192.0.2.2:1000")
	e.burn("192.0.2.3:1000")
	for range 3 {
		e.loginFrom(password, "192.0.2.3:1000")
	}

	out := buf.String()
	if n := strings.Count(out, "login gesperrt"); n != 1 {
		t.Errorf("sperre %d mal geloggt", n)
	}
	for _, want := range []string{
		"login fehlgeschlagen\" ip=192.0.2.1",
		"login erfolgreich\" ip=192.0.2.2",
		"login gesperrt\" ip=192.0.2.3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("%q fehlt in\n%s", want, out)
		}
	}
	for _, secret := range []string{"leon", "passwort"} {
		if strings.Contains(out, secret) {
			t.Errorf("log enthaelt %q", secret)
		}
	}
}
