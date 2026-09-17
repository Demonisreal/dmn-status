package monitor_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/alert"
	"github.com/Demonisreal/dmn-status/internal/alert/smtptest"
	"github.com/Demonisreal/dmn-status/internal/auth"
	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/config"
	"github.com/Demonisreal/dmn-status/internal/monitor"
	"github.com/Demonisreal/dmn-status/internal/store"
	"github.com/Demonisreal/dmn-status/internal/web"
)

// Der test liegt hier und nicht in internal/web, weil nur export_test.go an den checker und
// den takt des managers kommt. Der echte checker sperrt loopback, httptest lauscht aber dort.

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

type browser struct {
	t    *testing.T
	c    *http.Client
	base string
}

func (b *browser) get(path string) string {
	b.t.Helper()
	resp, err := b.c.Get(b.base + path)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		b.t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	return string(body)
}

// submit holt das csrf-token von der seite mit dem formular und schickt es ab
func (b *browser) submit(page, action string, form url.Values) *http.Response {
	b.t.Helper()
	m := csrfField.FindStringSubmatch(b.get(page))
	if m == nil {
		b.t.Fatalf("%s ohne csrf-token", page)
	}
	form.Set("csrf", m[1])
	resp, err := b.c.PostForm(b.base+action, form)
	if err != nil {
		b.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func serveOn(t *testing.T, addr string) *httptest.Server {
	t.Helper()
	var ln net.Listener
	var err error
	// windows gibt den port nach dem schliessen nicht immer sofort frei
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if ln, err = net.Listen("tcp", addr); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	return srv
}

func TestE2EDownUpMail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "status.db"), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.SetAdmin(ctx, "leon", auth.Hash("e2e passwort lang")); err != nil {
		t.Fatal(err)
	}

	smtp := smtptest.Start(t, "user", "pass")
	host, port, _ := net.SplitHostPort(smtp.Addr())
	p, _ := strconv.Atoi(port)
	mailer := &alert.Mailer{
		Host: host, Port: p, User: "user", Pass: "pass",
		From: "dmn-status <noreply@example.test>",
		To:   []string{"admin@example.test"},
		TLS:  &tls.Config{RootCAs: smtp.CertPool()},
		Now:  time.Now,
	}

	target := serveOn(t, "127.0.0.1:0")
	addr := target.Listener.Addr().String()

	mgr := monitor.New(st, check.Checker{}, mailer)
	hc := &http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
	// eine intervall-sekunde ist eine millisekunde, das kleinste formular-intervall sind also 30 ms
	monitor.SetCheck(mgr, time.Millisecond, func(ctx context.Context, tg check.Target) check.Result {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, tg.Address, nil)
		if err != nil {
			return check.Result{Err: "anfrage ungueltig"}
		}
		start := time.Now()
		resp, err := hc.Do(req)
		ms := int(time.Since(start).Milliseconds())
		if err != nil {
			return check.Result{LatencyMs: ms, Err: "verbindungsfehler"}
		}
		resp.Body.Close()
		return check.Result{OK: resp.StatusCode < 400, StatusCode: resp.StatusCode, LatencyMs: ms}
	})
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		mgr.Wait()
	})

	app := httptest.NewServer(web.New(web.Deps{Store: st, Monitor: mgr, Mailer: mailer, Config: config.Config{}, Version: "e2e", Now: time.Now}))
	t.Cleanup(app.Close)
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}, base: app.URL}

	resp := b.submit("/admin/login", "/admin/login", url.Values{"username": {"leon"}, "password": {"e2e passwort lang"}})
	if resp.Request.URL.Path != "/admin/" {
		t.Fatalf("login endet auf %s mit %d", resp.Request.URL.Path, resp.StatusCode)
	}

	resp = b.submit("/admin/ziel/neu", "/admin/ziel/neu", url.Values{
		"name":           {"Testziel"},
		"kind":           {"http"},
		"address":        {"http://" + addr + "/"},
		"expect_status":  {"200-399"},
		"interval_s":     {"30"},
		"timeout_ms":     {"500"},
		"fail_threshold": {"2"},
		"public":         {"1"},
	})
	if resp.Request.URL.RawQuery != "ok=gespeichert" {
		t.Fatalf("anlegen endet auf %s?%s", resp.Request.URL.Path, resp.Request.URL.RawQuery)
	}

	waitPage := func(want ...string) string {
		t.Helper()
		var body string
		// die statusseite kommt bis zu 5 s aus dem cache
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			body = b.get("/")
			if containsAll(body, want) {
				return body
			}
		}
		t.Fatalf("statusseite ohne %q:\n%s", want, body)
		return ""
	}

	waitPage(`data-state="up"`, ">"+web.T("de", "state.up")+"<")
	if n := len(smtp.Messages()); n != 0 {
		t.Fatalf("%d mails, obwohl das ziel erreichbar ist", n)
	}

	target.Close()
	msgs := smtp.Wait(t, 1, 5*time.Second)
	if !strings.Contains(string(msgs[0].Data), "Testziel ist nicht erreichbar") {
		t.Errorf("down-mail:\n%s", msgs[0].Data)
	}
	waitPage(`data-state="down"`, web.T("de", "inc.live"))
	// in der zeit laufen dutzende weitere checks, die mail darf trotzdem nur einmal kommen
	time.Sleep(300 * time.Millisecond)
	if n := len(smtp.Messages()); n != 1 {
		t.Fatalf("%d mails waehrend des ausfalls, wollte 1", n)
	}

	target = serveOn(t, addr)
	t.Cleanup(target.Close)
	msgs = smtp.Wait(t, 2, 5*time.Second)
	if !strings.Contains(string(msgs[1].Data), "Testziel ist wieder erreichbar") {
		t.Errorf("up-mail:\n%s", msgs[1].Data)
	}
	waitPage(`data-state="up"`, web.T("de", "inc.done"))
	time.Sleep(100 * time.Millisecond)
	if n := len(smtp.Messages()); n != 2 {
		t.Errorf("%d mails nach der wiederkehr, wollte 2", n)
	}
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
