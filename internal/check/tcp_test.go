package check

import (
	"context"
	"net"
	"testing"
)

func TestTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	tests := []struct {
		name     string
		addr     string
		loopback bool
		ok       bool
		err      string
	}{
		{name: "offen", addr: ln.Addr().String(), loopback: true, ok: true},
		{name: "zu", addr: closedAddr(t), loopback: true, err: "verbindung abgelehnt"},
		{name: "gesperrt", addr: ln.Addr().String(), err: "ziel gesperrt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := Target{Kind: KindTCP, Address: tt.addr, TimeoutMs: 5000}
			res := Checker{loopback: tt.loopback}.Run(context.Background(), tg)
			if res.OK != tt.ok || res.Err != tt.err {
				t.Fatalf("got ok=%v err=%q", res.OK, res.Err)
			}
		})
	}
}
