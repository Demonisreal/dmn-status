package store

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
)

func TestRollup(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	id := newTarget(t, s, "Website", true)
	cur := time.Unix(hourStart(start), 0).UTC()

	// vorige stunde: 20 erfolgreiche mit 1..20 ms und 5 fehlschlaege mit 10 s
	prev := cur.Add(-time.Hour)
	for i := 1; i <= 20; i++ {
		addCheck(t, s, id, prev.Add(time.Duration(i)*time.Minute), check.Result{OK: true, LatencyMs: i})
	}
	for i := range 5 {
		addCheck(t, s, id, prev.Add(time.Duration(40+i)*time.Minute), check.Result{LatencyMs: 10000})
	}
	// vorvorige stunde nur fehlschlaege, dritte stunde zurueck und laufende bleiben draussen
	addCheck(t, s, id, cur.Add(-90*time.Minute), check.Result{LatencyMs: 10000})
	addCheck(t, s, id, cur.Add(-150*time.Minute), check.Result{OK: true, LatencyMs: 5})
	addCheck(t, s, id, cur.Add(time.Minute), check.Result{OK: true, LatencyMs: 5})

	for range 2 {
		if err := s.Rollup(ctx); err != nil {
			t.Fatal(err)
		}
	}

	type row struct{ total, ok, avg, p95, max int }
	read := func(h time.Time) row {
		var r row
		err := s.r.QueryRowContext(ctx, `select total, ok_count, lat_avg, lat_p95, lat_max from hourly
			where target_id = ? and hour = ?`, id, h.Unix()).Scan(&r.total, &r.ok, &r.avg, &r.p95, &r.max)
		if err != nil {
			t.Fatalf("hourly %v: %v", h, err)
		}
		return r
	}

	// avg (1+..+20)/20 = 10 (ganzzahlig), p95 nearest rank = 19. wert
	if got, want := read(prev), (row{25, 20, 10, 19, 20}); got != want {
		t.Errorf("vorige stunde %+v, want %+v", got, want)
	}
	if got, want := read(cur.Add(-2*time.Hour)), (row{1, 0, 0, 0, 0}); got != want {
		t.Errorf("vorvorige stunde %+v, want %+v", got, want)
	}
	if n := count(t, s, `select count(*) from hourly`); n != 2 {
		t.Errorf("%d zeilen in hourly, want 2", n)
	}
}

func TestLatencyStats(t *testing.T) {
	tests := []struct {
		lat           []int
		avg, p95, max int
	}{
		{nil, 0, 0, 0},
		{[]int{7}, 7, 7, 7},
		{[]int{30, 10, 20}, 20, 30, 30},
		{[]int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 5, 10, 10},
	}
	for _, tt := range tests {
		st := stats{lat: tt.lat}
		avg, p95, maxMs := st.latency()
		if avg != tt.avg || p95 != tt.p95 || maxMs != tt.max {
			t.Errorf("%v: %d %d %d, want %d %d %d", tt.lat, avg, p95, maxMs, tt.avg, tt.p95, tt.max)
		}
	}
}

func TestUptime(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()
	id := newTarget(t, s, "Website", true)
	cur := hourStart(start)

	u, err := s.Uptime(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if u != (Uptime{-1, -1, -1}) {
		t.Fatalf("ohne daten %+v", u)
	}

	hourly := []struct {
		ago       int64
		total, ok int
	}{
		{2, 10, 5},     // 24h
		{23, 10, 10},   // aelteste stunde, die in 24h noch zaehlt
		{24, 100, 0},   // nur 7d
		{167, 10, 10},  // aelteste in 7d
		{500, 20, 0},   // nur 30d
		{720, 1000, 0}, // ausserhalb
	}
	for _, h := range hourly {
		exec(t, s, `insert into hourly values (?, ?, ?, ?, 0, 0, 0)`, id, cur-h.ago*hour, h.total, h.ok)
	}
	addCheck(t, s, id, start.Add(-10*time.Minute), check.Result{OK: true})
	addCheck(t, s, id, start.Add(-5*time.Minute), check.Result{})

	u, err = s.Uptime(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := Uptime{
		Day:   float64(5+10+1) / float64(10+10+2),
		Week:  float64(5+10+10+1) / float64(10+10+100+10+2),
		Month: float64(5+10+10+1) / float64(10+10+100+10+20+2),
	}
	if !near(u.Day, want.Day) || !near(u.Week, want.Week) || !near(u.Month, want.Month) {
		t.Fatalf("uptime %+v, want %+v", u, want)
	}

	// eine stunde spaeter faellt die aelteste 24h-stunde raus, die rohen checks stehen jetzt
	// in der vorigen stunde und sind ohne rollup nicht mehr gezaehlt
	clock.Advance(time.Hour)
	u, err = s.Uptime(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !near(u.Day, 0.5) {
		t.Fatalf("24h nach einer stunde %v, want 0.5", u.Day)
	}
}

func near(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestHours(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	id := newTarget(t, s, "Website", true)
	cur := hourStart(start)

	exec(t, s, `insert into hourly values (?, ?, 60, 59, 0, 0, 0)`, id, cur-hour)
	exec(t, s, `insert into hourly values (?, ?, 60, 0, 0, 0, 0)`, id, cur-89*hour)
	exec(t, s, `insert into hourly values (?, ?, 60, 60, 0, 0, 0)`, id, cur-90*hour)
	addCheck(t, s, id, start, check.Result{OK: true})

	cells, err := s.Hours(ctx, id, 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 90 {
		t.Fatalf("%d zellen", len(cells))
	}
	if first := cells[0]; first.Start.Unix() != cur-89*hour || first.Total != 60 || first.OK != 0 {
		t.Errorf("erste zelle %+v", first)
	}
	if c := cells[88]; c.Total != 60 || c.OK != 59 {
		t.Errorf("vorletzte zelle %+v", c)
	}
	if c := cells[89]; c.Start.Unix() != cur || c.Total != 1 || c.OK != 1 {
		t.Errorf("laufende stunde %+v", c)
	}
	empty := 0
	for _, c := range cells {
		if c.Total == 0 {
			empty++
		}
	}
	if empty != 87 {
		t.Errorf("%d leere zellen, want 87", empty)
	}
}

func TestLatency(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	id := newTarget(t, s, "Website", true)

	// zwei checks im selben 10-minuten-schritt, einer fehlgeschlagen, einer vor 25 stunden
	addCheck(t, s, id, start.Add(-3*time.Minute), check.Result{OK: true, LatencyMs: 100})
	addCheck(t, s, id, start.Add(-2*time.Minute), check.Result{OK: true, LatencyMs: 300})
	addCheck(t, s, id, start.Add(-1*time.Minute), check.Result{LatencyMs: 10000})
	addCheck(t, s, id, start.Add(-65*time.Minute), check.Result{OK: true, LatencyMs: 50})
	addCheck(t, s, id, start.Add(-25*time.Hour), check.Result{OK: true, LatencyMs: 1})

	pts, err := s.Latency(ctx, id, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("24h: %+v", pts)
	}
	if p := pts[1]; !p.At.Equal(start.Add(-10*time.Minute)) || p.Avg != 200 || p.Max != 300 || p.P95 != 300 {
		t.Errorf("letzter schritt %+v", p)
	}

	cur := hourStart(start)
	exec(t, s, `insert into hourly values (?, ?, 60, 60, 80, 120, 200)`, id, cur-2*hour)
	exec(t, s, `insert into hourly values (?, ?, 60, 0, 0, 0, 0)`, id, cur-3*hour)
	exec(t, s, `insert into hourly values (?, ?, 60, 60, 1, 1, 1)`, id, cur-8*24*hour)

	pts, err = s.Latency(ctx, id, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 || pts[0].Avg != 80 || pts[0].P95 != 120 || pts[0].Max != 200 {
		t.Fatalf("7d: %+v", pts)
	}
}
