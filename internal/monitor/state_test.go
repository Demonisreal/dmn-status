package monitor

import (
	"strings"
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	base := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	// checks: x fehler, . erfolg, einer pro minute. events: - nichts, d down, u up
	tests := []struct {
		name      string
		threshold int
		checks    string
		events    string
		started   int // minute des ersten fehlers beim letzten down
		dur       time.Duration
	}{
		{"einzelfehler_unter_schwelle", 3, "x.x.xx.", "-------", 0, 0},
		{"schwelle_3", 3, "xxx", "--d", 0, 0},
		{"down_nur_einmal", 3, "xxxxxx", "--d---", 0, 0},
		{"flattern_ohne_vorfall", 3, "xx.xx.xx.", "---------", 0, 0},
		{"schwelle_1", 1, "x.x", "dud", 2, time.Minute},
		{"erfolg_ohne_vorfall", 1, "...", "---", 0, 0},
		{"dauer_ab_erstem_fehler", 2, ".xxxx.", "--d--u", 1, 4 * time.Minute},
		{"neue_serie_nach_wiederkehr", 2, "xx.xx", "-du-d", 3, 2 * time.Minute},
	}
	names := map[event]byte{none: '-', down: 'd', up: 'u'}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s state
			var got []byte
			for i, c := range tt.checks {
				at := base.Add(time.Duration(i) * time.Minute)
				var ev event
				s, ev = next(s, c == '.', "status 503", at, tt.threshold)
				got = append(got, names[ev])
				if ev == up && at.Sub(s.startedAt) != tt.dur {
					t.Errorf("dauer %v, want %v", at.Sub(s.startedAt), tt.dur)
				}
			}
			if string(got) != tt.events {
				t.Fatalf("events %s, want %s", got, tt.events)
			}
			if want := base.Add(time.Duration(tt.started) * time.Minute); strings.Contains(tt.events, "d") && !s.startedAt.Equal(want) {
				t.Errorf("startedAt %v, want %v", s.startedAt, want)
			}
		})
	}
}

func TestNextCause(t *testing.T) {
	at := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, _ := next(state{}, false, "zeitueberschreitung", at, 2)
	s, ev := next(s, false, "verbindung abgelehnt", at.Add(time.Minute), 2)
	if ev != down || s.cause != "verbindung abgelehnt" || !s.startedAt.Equal(at) {
		t.Fatalf("ev %v, state %+v", ev, s)
	}
	s, _ = next(s, true, "", at.Add(2*time.Minute), 2)
	if s.fails != 0 || s.cause != "" || s.open {
		t.Errorf("nach erfolg %+v", s)
	}
}
