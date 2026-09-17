package check

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPv6Literals(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("kein ipv6-loopback: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	v6 := ln.Addr().String()
	_, port, _ := net.SplitHostPort(v6)

	v4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer v4.Close()
	_, port4, _ := net.SplitHostPort(v4.Listener.Addr().String())

	tests := []struct {
		name      string
		loopback  bool
		kind      string
		address   string
		connectTo string
		err       string
	}{
		{name: "http_loopback_erlaubt", loopback: true, address: "http://" + v6 + "/"},
		{name: "http_gesperrt", address: "http://" + v6 + "/", err: "ziel gesperrt"},
		{name: "http_ausgeschrieben", address: "http://[0:0:0:0:0:0:0:1]:" + port + "/", err: "ziel gesperrt"},
		{name: "http_ipv4_gemappt", address: "http://[::ffff:127.0.0.1]:" + port4 + "/", err: "ziel gesperrt"},
		{name: "http_link_local_mit_zone", address: "http://[fe80::1%25eth0]:" + port + "/", err: "ziel gesperrt"},
		{name: "http_connect_to", address: "http://status.example.invalid/", connectTo: v6, err: "ziel gesperrt"},
		{name: "tcp_loopback_erlaubt", loopback: true, kind: KindTCP, address: v6},
		{name: "tcp_gesperrt", kind: KindTCP, address: v6, err: "ziel gesperrt"},
		{name: "fivem_gesperrt", kind: KindFiveM, address: v6, err: "ziel gesperrt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := httpTarget(tt.address)
			if tt.kind != "" {
				tg.Kind = tt.kind
			}
			tg.ConnectTo = tt.connectTo
			res := Checker{loopback: tt.loopback}.Run(context.Background(), tg)
			if res.Err != tt.err || res.OK != (tt.err == "") {
				t.Fatalf("got ok=%v err=%q, want err=%q", res.OK, res.Err, tt.err)
			}
		})
	}
}
