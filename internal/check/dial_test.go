package check

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestAllowed(t *testing.T) {
	tests := []struct {
		ip       string
		strict   bool // ohne alles
		loopback bool // mit test-hebel
		listed   bool // ip ist aufloesung eines PrivateAllow-eintrags
	}{
		{"93.184.215.14", true, true, true},
		{"2606:4700::6810:84e5", true, true, true},

		{"127.0.0.1", false, true, false},
		{"127.8.8.8", false, true, false},
		{"::1", false, true, false},
		{"::ffff:127.0.0.1", false, true, false},

		{"10.0.0.1", false, false, true},
		{"172.16.5.4", false, false, true},
		{"172.31.255.255", false, false, true},
		{"192.168.1.1", false, false, true},
		{"100.64.0.1", false, false, true},
		{"fd12:3456::1", false, false, true},
		{"64:ff9b::a00:1", false, false, true},   // nat64 auf 10.0.0.1
		{"2002:c0a8:101::1", false, false, true}, // 6to4 auf 192.168.1.1

		{"172.32.0.1", true, true, true},
		{"100.128.0.1", true, true, true},
		{"64:ff9b::808:808", true, true, true},
		{"198.20.0.1", true, true, true},

		{"0.0.0.0", false, false, false},
		{"0.1.2.3", false, false, false},
		{"::", false, false, false},
		{"169.254.169.254", false, false, false},
		{"::ffff:169.254.169.254", false, false, false},
		{"64:ff9b::a9fe:a9fe", false, false, false},
		{"64:ff9b:1::7f00:1", false, false, false}, // lokales nat64 ist immer zu
		{"64:ff9b:1::a00:1", false, false, false},
		{"64:ff9b:1::808:808", false, false, false},
		{"64:ff9b:1:abcd::a9fe:a9fe", false, false, false},
		{"fe80::1", false, false, false},
		{"fe80::1%eth0", false, false, false},
		{"224.0.0.1", false, false, false},
		{"ff02::1", false, false, false},
		{"255.255.255.255", false, false, false},
		{"240.0.0.1", false, false, false},
		{"::7f00:1", false, false, false},
		{"192.0.0.8", false, false, false},
		{"198.18.0.1", false, false, false},
		{"198.19.255.255", false, false, false},
		{"::ffff:0:a00:1", false, false, false},
		{"::ffff:0:808:808", false, false, false},
		{"2001::1", false, false, false},
		{"2001:0:4136:e378:8000:63bf:3fff:fdd2", false, false, false},
		{"fec0::1", false, false, false},
	}
	for _, tt := range tests {
		ip := netip.MustParseAddr(tt.ip)
		if got := allowed(ip, false, nil, nil); got != tt.strict {
			t.Errorf("%s strikt: %v, want %v", tt.ip, got, tt.strict)
		}
		if got := allowed(ip, true, nil, nil); got != tt.loopback {
			t.Errorf("%s mit loopback: %v, want %v", tt.ip, got, tt.loopback)
		}
		extra := []netip.Addr{ip.WithZone("").Unmap()}
		if got := allowed(ip, false, extra, nil); got != tt.listed {
			t.Errorf("%s gelistet: %v, want %v", tt.ip, got, tt.listed)
		}
	}

	// nur die aufgeloesten ips zaehlen, nicht das ganze netz oder die ipv4 hinter nat64
	extra := []netip.Addr{netip.MustParseAddr("10.0.0.5")}
	for _, s := range []string{"10.0.0.6", "64:ff9b::a00:5", "fd00::a00:5"} {
		if allowed(netip.MustParseAddr(s), false, extra, nil) {
			t.Errorf("%s trotz anderer aufloesung erlaubt", s)
		}
	}

	// eigene adressen sind auch oeffentlich, gelistet oder hinter nat64/6to4 zu
	block := []netip.Addr{
		netip.MustParseAddr("93.184.215.14"),
		netip.MustParseAddr("2606:4700::6810:84e5"),
		netip.MustParseAddr("10.0.0.5"),
	}
	for _, s := range []string{
		"93.184.215.14",
		"::ffff:93.184.215.14",
		"64:ff9b::5db8:d70e",
		"64:ff9b:1::5db8:d70e",
		"2002:5db8:d70e::1",
		"2606:4700::6810:84e5",
		"10.0.0.5",
	} {
		ip := netip.MustParseAddr(s)
		if allowed(ip, true, []netip.Addr{ip.Unmap()}, block) {
			t.Errorf("%s trotz block erlaubt", s)
		}
	}
	for _, s := range []string{"93.184.215.15", "2606:4700::6810:84e6"} {
		if !allowed(netip.MustParseAddr(s), false, nil, block) {
			t.Errorf("%s gesperrt, steht nicht im block", s)
		}
	}
}

func TestPrivateAllow(t *testing.T) {
	hosts := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts <- r.Host
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	lookup := func(ip string) func(context.Context, string) ([]netip.Addr, error) {
		return func(_ context.Context, host string) ([]netip.Addr, error) {
			if !strings.EqualFold(host, "proxy") {
				return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
			}
			return []netip.Addr{netip.MustParseAddr(ip)}, nil
		}
	}

	tests := []struct {
		name      string
		c         Checker
		kind      string
		address   string
		connectTo string
		err       string
	}{
		{
			name:      "gelistet",
			c:         Checker{PrivateAllow: []string{"proxy:" + port}, lookup: lookup("127.0.0.1"), loopback: true},
			connectTo: "proxy:" + port,
		},
		{
			name:      "gelistet gross geschrieben",
			c:         Checker{PrivateAllow: []string{"proxy:" + port}, lookup: lookup("127.0.0.1"), loopback: true},
			connectTo: "PROXY:" + port,
		},
		{
			name:      "block trotz eintrag",
			c:         Checker{PrivateAllow: []string{"proxy:" + port}, lookup: lookup("127.0.0.1"), loopback: true, Block: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
			connectTo: "proxy:" + port,
			err:       "ziel gesperrt",
		},
		{
			name:    "http address im block",
			c:       Checker{Block: []netip.Addr{netip.MustParseAddr("93.184.215.14")}},
			address: "http://93.184.215.14/",
			err:     "ziel gesperrt",
		},
		{
			name:      "connect_to im block",
			c:         Checker{Block: []netip.Addr{netip.MustParseAddr("93.184.215.14")}},
			connectTo: "93.184.215.14:443",
			err:       "ziel gesperrt",
		},
		{
			name:    "tcp address im block",
			c:       Checker{Block: []netip.Addr{netip.MustParseAddr("93.184.215.14")}},
			kind:    KindTCP,
			address: "93.184.215.14:443",
			err:     "ziel gesperrt",
		},
		{
			name:      "loopback trotz eintrag",
			c:         Checker{PrivateAllow: []string{"proxy:" + port}, lookup: lookup("127.0.0.1")},
			connectTo: "proxy:" + port,
			err:       "ziel gesperrt",
		},
		{
			name:      "nicht gelistet",
			c:         Checker{PrivateAllow: []string{"proxy:443"}, lookup: lookup("10.0.0.5")},
			connectTo: "10.0.0.6:443",
			err:       "ziel gesperrt",
		},
		{
			// derselbe host ueber einen anderen namen bekommt keine ausnahme
			name:      "ip des eintrags direkt",
			c:         Checker{PrivateAllow: []string{"proxy:443"}, lookup: lookup("10.0.0.5")},
			connectTo: "10.0.0.5:443",
			err:       "ziel gesperrt",
		},
		{
			name:      "anderer port",
			c:         Checker{PrivateAllow: []string{"10.0.0.5:443"}},
			connectTo: "10.0.0.5:444",
			err:       "ziel gesperrt",
		},
		{
			name:    "address privat ohne connect_to",
			c:       Checker{PrivateAllow: []string{"10.0.0.5:443"}},
			address: "http://10.0.0.5:443/",
			err:     "ziel gesperrt",
		},
		{
			name:    "fivem address privat",
			c:       Checker{PrivateAllow: []string{"10.0.0.5:30120"}},
			kind:    KindFiveM,
			address: "10.0.0.5:30120",
			err:     "ziel gesperrt",
		},
		{
			name:    "tcp address privat",
			c:       Checker{PrivateAllow: []string{"10.0.0.5:22"}},
			kind:    KindTCP,
			address: "10.0.0.5:22",
			err:     "ziel gesperrt",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := httpTarget("http://status.example.invalid/")
			if tt.kind != "" {
				tg.Kind = tt.kind
			}
			if tt.address != "" {
				tg.Address = tt.address
			}
			tg.ConnectTo = tt.connectTo
			res := tt.c.Run(context.Background(), tg)
			if res.Err != tt.err || res.OK != (tt.err == "") {
				t.Fatalf("got ok=%v err=%q, want err=%q", res.OK, res.Err, tt.err)
			}
			if res.OK {
				if host := <-hosts; host != "status.example.invalid" {
					t.Fatalf("host header %q", host)
				}
			}
		})
	}
}
