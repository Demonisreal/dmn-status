package store

import (
	"context"
	"testing"
	"time"
)

func TestDevice(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()

	token, err := s.CreateDevice(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `select count(*) from devices where token_hash = ?`, token); n != 0 {
		t.Fatal("token steht im klartext in der datenbank")
	}
	for _, tt := range []struct {
		token string
		want  bool
	}{
		{token, true},
		{token + "x", false},
		{"", false},
	} {
		if got, err := s.KnownDevice(ctx, tt.token); err != nil || got != tt.want {
			t.Errorf("KnownDevice(%q) = %v, %v", tt.token, got, err)
		}
	}

	clock.Advance(DeviceMax - time.Second)
	later, _ := s.CreateDevice(ctx)
	if ok, _ := s.KnownDevice(ctx, token); !ok {
		t.Error("kurz vor ablauf schon unbekannt")
	}
	clock.Advance(time.Second)
	if ok, _ := s.KnownDevice(ctx, token); ok {
		t.Error("nach 90 tagen noch bekannt")
	}

	n, err := s.DeleteExpiredDevices(ctx)
	if err != nil || n != 1 {
		t.Fatalf("%d geloescht, %v", n, err)
	}
	if ok, _ := s.KnownDevice(ctx, later); !ok {
		t.Error("spaeteres geraet mit geloescht")
	}
}
