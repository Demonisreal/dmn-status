package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSession(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()

	token, sess, err := s.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 26 || len(sess.CSRF) < 26 || token == sess.CSRF {
		t.Fatalf("token %q csrf %q", token, sess.CSRF)
	}
	if n := count(t, s, `select count(*) from sessions where token_hash = ? or csrf = ?`, token, token); n != 0 {
		t.Fatal("token steht im klartext in der datenbank")
	}

	got, err := s.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if got != sess {
		t.Fatalf("Session %+v, want %+v", got, sess)
	}
	if _, err := s.Session(ctx, token+"x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("falsches token: %v", err)
	}

	clock.Advance(11 * time.Hour)
	if err := s.TouchSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Session(ctx, token)
	if want := start.Add(23 * time.Hour); !got.ExpiresAt.Equal(want) || !got.LastSeen.Equal(start.Add(11*time.Hour)) {
		t.Fatalf("nach touch %+v", got)
	}

	clock.Advance(12*time.Hour + time.Second)
	if _, err := s.Session(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nach 12 h leerlauf: %v", err)
	}
	if err := s.TouchSession(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("touch nach ablauf: %v", err)
	}
}

func TestSessionMaxAge(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()

	token, _, err := s.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// alle 6 stunden aktiv, trotzdem ist nach 7 tagen schluss
	for elapsed := 6 * time.Hour; elapsed < SessionMax; elapsed += 6 * time.Hour {
		clock.Set(start.Add(elapsed))
		if err := s.TouchSession(ctx, token); err != nil {
			t.Fatalf("nach %v: %v", elapsed, err)
		}
	}
	got, err := s.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpiresAt.Equal(start.Add(SessionMax)) {
		t.Fatalf("ablauf %v, want %v", got.ExpiresAt, start.Add(SessionMax))
	}

	clock.Set(start.Add(SessionMax))
	if _, err := s.Session(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nach 7 tagen: %v", err)
	}
}

func TestSessionDelete(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()

	a, _, _ := s.CreateSession(ctx)
	clock.Advance(6 * time.Hour)
	b, _, _ := s.CreateSession(ctx)

	if err := s.DeleteSession(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nach logout: %v", err)
	}

	c, _, _ := s.CreateSession(ctx)
	clock.Advance(7 * time.Hour)
	n, err := s.DeleteExpiredSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d abgelaufene geloescht, want 1", n)
	}
	if _, err := s.Session(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Errorf("a: %v", err)
	}
	if _, err := s.Session(ctx, c); err != nil {
		t.Errorf("c soll noch gelten: %v", err)
	}
}
