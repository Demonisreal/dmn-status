package store

import (
	"context"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
)

func hourlyTotal(t *testing.T, s *Store, id int64, h time.Time) int {
	t.Helper()
	return count(t, s, `select coalesce(sum(total), 0) from hourly where target_id = ? and hour = ?`, id, h.Unix())
}

func TestRollupHourBoundaries(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()
	id := newTarget(t, s, "Website", true)
	h13 := time.Date(2026, 3, 10, 13, 0, 0, 0, time.UTC)
	h12, h11 := h13.Add(-time.Hour), h13.Add(-2*time.Hour)

	for _, at := range []time.Time{
		h11.Add(-time.Second), // vor dem fenster
		h11,
		h12.Add(-time.Second),
		h12,
		h13.Add(-time.Second),
		h13, // laufende stunde
	} {
		addCheck(t, s, id, at, check.Result{OK: true, LatencyMs: 10})
	}

	clock.Set(h13)
	cells, err := s.Hours(ctx, id, 3)
	if err != nil {
		t.Fatal(err)
	}
	if cells[1].Total != 0 {
		t.Errorf("vorige stunde vor dem rollup %+v", cells[1])
	}

	if err := s.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		hour time.Time
		want int
	}{
		{h11.Add(-time.Hour), 0},
		{h11, 2},
		{h12, 2},
		{h13, 0},
	} {
		if got := hourlyTotal(t, s, id, tt.hour); got != tt.want {
			t.Errorf("stunde %s: %d checks, want %d", tt.hour.Format("15:04"), got, tt.want)
		}
	}
	if cells, _ := s.Hours(ctx, id, 3); cells[1].Total != 2 || cells[2].Total != 1 {
		t.Errorf("zellen nach dem rollup %+v", cells)
	}

	// ein check, der vor 13 uhr gestartet und erst danach geschrieben wurde
	addCheck(t, s, id, h13.Add(-2*time.Second), check.Result{LatencyMs: 10000})
	clock.Set(h13.Add(59*time.Minute + 59*time.Second))
	if err := s.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	if got := hourlyTotal(t, s, id, h12); got != 3 {
		t.Errorf("nachzuegler: %d checks in 12 uhr, want 3", got)
	}

	// zwei stunden spaeter faellt 12 uhr aus dem fenster und bleibt, wie es war
	addCheck(t, s, id, h12.Add(30*time.Minute), check.Result{OK: true})
	clock.Set(h13.Add(2 * time.Hour))
	if err := s.Rollup(ctx); err != nil {
		t.Fatal(err)
	}
	if got := hourlyTotal(t, s, id, h12); got != 3 {
		t.Errorf("12 uhr nach dem fenster neu gerechnet: %d", got)
	}
	if got := hourlyTotal(t, s, id, h13); got != 1 {
		t.Errorf("13 uhr: %d checks, want 1", got)
	}
}

func TestUptimeWithPauses(t *testing.T) {
	cur := hourStart(start)
	type row struct {
		ago       int64
		total, ok int
	}
	tests := []struct {
		name   string
		hourly []row
		raw    []bool // checks in der laufenden stunde
		want   Uptime
	}{
		{
			name:   "pause_ueber_den_ganzen_tag",
			hourly: []row{{30, 60, 60}, {100, 60, 30}},
			want:   Uptime{Day: -1, Week: 0.75, Month: 0.75},
		},
		{
			name:   "luecke_zaehlt_weder_auf_noch_ab",
			hourly: []row{{1, 10, 5}, {20, 10, 10}},
			want:   Uptime{Day: 0.75, Week: 0.75, Month: 0.75},
		},
		{
			name:   "nur_laufende_stunde_nach_langer_pause",
			hourly: []row{{800, 60, 0}},
			raw:    []bool{true, false, true, true},
			want:   Uptime{Day: 0.75, Week: 0.75, Month: 0.75},
		},
		{
			name:   "alles_aelter_als_30_tage",
			hourly: []row{{720, 60, 60}},
			want:   Uptime{Day: -1, Week: -1, Month: -1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newStore(t)
			ctx := context.Background()
			id := newTarget(t, s, "Website", true)
			for _, h := range tt.hourly {
				exec(t, s, `insert into hourly values (?, ?, ?, ?, 0, 0, 0)`, id, cur-h.ago*hour, h.total, h.ok)
			}
			for i, ok := range tt.raw {
				addCheck(t, s, id, start.Add(-time.Duration(i+1)*time.Minute), check.Result{OK: ok})
			}

			before, err := s.Uptime(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if !near(before.Day, tt.want.Day) || !near(before.Week, tt.want.Week) || !near(before.Month, tt.want.Month) {
				t.Fatalf("uptime %+v, want %+v", before, tt.want)
			}

			// das pausieren selbst aendert an der zahl nichts
			if err := s.SetPaused(ctx, id, true); err != nil {
				t.Fatal(err)
			}
			if after, _ := s.Uptime(ctx, id); after != before {
				t.Errorf("nach SetPaused %+v, vorher %+v", after, before)
			}
		})
	}
}
