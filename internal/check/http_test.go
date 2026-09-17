package check

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func httpTarget(addr string) Target {
	return Target{
		Kind:          KindHTTP,
		Address:       addr,
		ExpectStatus:  "200-399",
		IntervalS:     60,
		TimeoutMs:     5000,
		FailThreshold: 3,
	}
}

func TestHTTP(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != "dmn-status" {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.Write([]byte("<h1>alles gut</h1>"))
	})
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/down", http.StatusFound)
	})
	mux.HandleFunc("/big-tail", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(big + "marker"))
	})
	mux.HandleFunc("/big-head", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("marker" + big))
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tests := []struct {
		name    string
		path    string
		expect  string
		keyword string
		timeout int
		ok      bool
		code    int
		err     string
	}{
		{name: "ok", path: "/ok", ok: true, code: 200},
		{name: "status ausserhalb", path: "/down", code: 503, err: "status 503"},
		{name: "liste", path: "/auth", expect: "200,401", ok: true, code: 401},
		{name: "liste ohne treffer", path: "/auth", expect: "200,403", code: 401, err: "status 401"},
		{name: "redirect zaehlt als 3xx", path: "/redirect", ok: true, code: 302},
		{name: "redirect nicht gefolgt", path: "/redirect", expect: "200-299", code: 302, err: "status 302"},
		{name: "stichwort da", path: "/ok", keyword: "alles gut", ok: true, code: 200},
		{name: "stichwort fehlt", path: "/ok", keyword: "kaputt", code: 200, err: "stichwort fehlt"},
		{name: "stichwort hinter 1 mib", path: "/big-tail", keyword: "marker", code: 200, err: "stichwort fehlt"},
		{name: "stichwort am anfang", path: "/big-head", keyword: "marker", ok: true, code: 200},
		{name: "zeitueberschreitung", path: "/slow", timeout: 500, err: "zeitüberschreitung"},
		{name: "statusangabe kaputt", path: "/ok", expect: "abc", err: "statusangabe ungültig"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := httpTarget(srv.URL + tt.path)
			tg.Keyword = tt.keyword
			if tt.expect != "" {
				tg.ExpectStatus = tt.expect
			}
			if tt.timeout != 0 {
				tg.TimeoutMs = tt.timeout
			}

			res := Checker{loopback: true}.Run(context.Background(), tg)
			if res.OK != tt.ok || res.StatusCode != tt.code || res.Err != tt.err {
				t.Fatalf("got ok=%v code=%d err=%q, want ok=%v code=%d err=%q",
					res.OK, res.StatusCode, res.Err, tt.ok, tt.code, tt.err)
			}
		})
	}
}

func TestHTTPTimeoutReturnsInTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	tg := httpTarget(srv.URL)
	tg.TimeoutMs = 500
	start := time.Now()
	Checker{loopback: true}.Run(context.Background(), tg)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("check lief %v trotz 500 ms timeout", d)
	}
}

func TestHTTPConnectTo(t *testing.T) {
	hosts := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts <- r.Host
	}))
	defer srv.Close()

	tg := httpTarget("http://status.example.invalid:8443/health")
	tg.ConnectTo = srv.Listener.Addr().String()
	res := Checker{loopback: true}.Run(context.Background(), tg)
	if !res.OK {
		t.Fatalf("connect_to: %q", res.Err)
	}
	if host := <-hosts; host != "status.example.invalid:8443" {
		t.Fatalf("host header %q, soll unveraendert bleiben", host)
	}
}

func TestHTTPConnectToStillBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	tg := httpTarget("https://example.com/")
	tg.ConnectTo = srv.Listener.Addr().String()
	res := Checker{}.Run(context.Background(), tg)
	if res.Err != "ziel gesperrt" {
		t.Fatalf("got %q, connect_to darf die sperre nicht umgehen", res.Err)
	}
}

func TestHTTPBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	res := Checker{}.Run(context.Background(), httpTarget(srv.URL))
	if res.OK || res.Err != "ziel gesperrt" {
		t.Fatalf("got ok=%v err=%q", res.OK, res.Err)
	}

	// localhost wird aufgeloest und landet ebenfalls auf loopback
	res = Checker{}.Run(context.Background(), httpTarget(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)))
	if res.Err != "ziel gesperrt" {
		t.Fatalf("localhost: got %q", res.Err)
	}
}

func TestHTTPBadCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	res := Checker{loopback: true}.Run(context.Background(), httpTarget(srv.URL))
	if res.Err != "zertifikat ungültig" {
		t.Fatalf("got %q", res.Err)
	}
}

func TestHTTPRefused(t *testing.T) {
	addr := closedAddr(t)
	res := Checker{loopback: true}.Run(context.Background(), httpTarget("http://"+addr+"/"))
	if res.Err != "verbindung abgelehnt" {
		t.Fatalf("got %q", res.Err)
	}
}

// closedAddr liefert einen port, der eben noch belegt war und jetzt frei ist
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestParseStatus(t *testing.T) {
	tests := []struct {
		in    string
		valid bool
		hit   []int
		miss  []int
	}{
		{in: "200-399", valid: true, hit: []int{200, 301, 399}, miss: []int{199, 400}},
		{in: "200,401", valid: true, hit: []int{200, 401}, miss: []int{201, 400}},
		{in: "200-299, 404", valid: true, hit: []int{250, 404}, miss: []int{300}},
		{in: "204", valid: true, hit: []int{204}, miss: []int{200}},
		{in: ""},
		{in: "abc"},
		{in: "399-200"},
		{in: "99"},
		{in: "200-600"},
		{in: "200,"},
		{in: "200--300"},
	}
	for _, tt := range tests {
		ranges, err := parseStatus(tt.in)
		if (err == nil) != tt.valid {
			t.Errorf("%q: err=%v, valid soll %v", tt.in, err, tt.valid)
			continue
		}
		for _, c := range tt.hit {
			if !statusOK(ranges, c) {
				t.Errorf("%q: %d soll passen", tt.in, c)
			}
		}
		for _, c := range tt.miss {
			if statusOK(ranges, c) {
				t.Errorf("%q: %d soll nicht passen", tt.in, c)
			}
		}
	}
}
