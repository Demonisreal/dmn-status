package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIncidents(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()
	pub := newTarget(t, s, "Website", true)
	priv := newTarget(t, s, "Intern", false)

	if _, err := s.OpenIncidentFor(ctx, pub); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ohne vorfall: %v", err)
	}

	id, err := s.OpenIncident(ctx, pub, start, "status 503")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenIncident(ctx, pub, start.Add(time.Minute), "zeitueberschreitung"); !errors.Is(err, ErrIncidentOpen) {
		t.Fatalf("zweiter offener vorfall: %v", err)
	}
	if _, err := s.OpenIncident(ctx, priv, start, "verbindung abgelehnt"); err != nil {
		t.Fatal(err)
	}

	open, err := s.OpenIncidentFor(ctx, pub)
	if err != nil {
		t.Fatal(err)
	}
	if open.ID != id || !open.Open() || open.TargetName != "Website" || open.Cause != "status 503" {
		t.Fatalf("OpenIncidentFor %+v", open)
	}

	recent, err := s.RecentIncidents(ctx, 10, start.Add(-30*24*time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].TargetID != pub {
		t.Fatalf("oeffentlich %+v", recent)
	}
	all, err := s.RecentIncidents(ctx, 10, start.Add(-30*24*time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("alle %+v", all)
	}

	clock.Advance(10 * time.Minute)
	end := start.Add(10 * time.Minute)
	if err := s.CloseIncident(ctx, id, end); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseIncident(ctx, id, end); !errors.Is(err, ErrNotFound) {
		t.Errorf("doppelt geschlossen: %v", err)
	}
	if _, err := s.OpenIncident(ctx, pub, end.Add(time.Minute), "status 502"); err != nil {
		t.Fatalf("nach dem schliessen: %v", err)
	}

	list, err := s.TargetIncidents(ctx, pub, 10, start.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || !list[0].Open() || list[1].Open() || !list[1].EndedAt.Equal(end) {
		t.Fatalf("TargetIncidents %+v", list)
	}

	// beendet vor since faellt raus, offen bleibt drin
	list, err = s.TargetIncidents(ctx, pub, 10, end.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Open() {
		t.Fatalf("since: %+v", list)
	}
	if list, _ = s.TargetIncidents(ctx, pub, 1, start); len(list) != 1 {
		t.Errorf("limit: %d", len(list))
	}
}

func TestUnmailed(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	id := newTarget(t, s, "Website", true)

	old, err := s.OpenIncident(ctx, id, start.Add(-3*24*time.Hour), "status 503")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CloseIncident(ctx, old, start.Add(-2*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	cur, err := s.OpenIncident(ctx, id, start.Add(-time.Hour), "status 503")
	if err != nil {
		t.Fatal(err)
	}

	since := start.Add(-24 * time.Hour)
	pending := func() []Incident {
		t.Helper()
		list, err := s.UnmailedIncidents(ctx, since)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	if list := pending(); len(list) != 1 || list[0].ID != cur {
		t.Fatalf("offen, ohne mail: %+v", list)
	}
	if err := s.MarkMailed(ctx, cur, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("wiederkehr-mail fuer offenen vorfall: %v", err)
	}
	if err := s.MarkMailed(ctx, cur, false); err != nil {
		t.Fatal(err)
	}
	if list := pending(); len(list) != 0 {
		t.Fatalf("nach ausfall-mail: %+v", list)
	}

	if err := s.CloseIncident(ctx, cur, start); err != nil {
		t.Fatal(err)
	}
	if list := pending(); len(list) != 1 || !list[0].MailedDown || list[0].MailedUp {
		t.Fatalf("beendet, wiederkehr-mail fehlt: %+v", list)
	}
	if err := s.MarkMailed(ctx, cur, true); err != nil {
		t.Fatal(err)
	}
	if list := pending(); len(list) != 0 {
		t.Fatalf("alles verschickt: %+v", list)
	}
}
