package store

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/testutil"
)

// 12:30 utc, damit laufende stunde und stundengrenzen im test eindeutig sind
var start = time.Date(2026, 3, 10, 12, 30, 0, 0, time.UTC)

func newStore(t *testing.T) (*Store, *testutil.Clock) {
	t.Helper()
	clock := testutil.NewClock(start)
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "status.db"), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, clock
}

func newTarget(t *testing.T, s *Store, name string, public bool) int64 {
	t.Helper()
	id, err := s.CreateTarget(context.Background(), check.Target{
		Name:          name,
		Kind:          check.KindHTTP,
		Address:       "https://example.com/",
		ExpectStatus:  "200-399",
		IntervalS:     60,
		TimeoutMs:     10000,
		FailThreshold: 3,
		Public:        public,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func addCheck(t *testing.T, s *Store, id int64, at time.Time, r check.Result) {
	t.Helper()
	if err := s.InsertCheck(context.Background(), id, at, r); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.r.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func exec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.w.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestOpenTwice(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "status.db")
	clock := testutil.NewClock(start)
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		s, err := Open(ctx, path, clock.Now)
		if err != nil {
			t.Fatal(err)
		}
		if n := count(t, s, `select count(*) from schema_migrations`); n != len(files) {
			t.Errorf("schema_migrations hat %d eintraege, want %d", n, len(files))
		}
		s.Close()
	}
}

func TestPragmas(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	var mode string
	if err := s.w.QueryRowContext(ctx, `pragma journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode %q, err %v", mode, err)
	}
	if n := count(t, s, `pragma foreign_keys`); n != 1 {
		t.Errorf("foreign_keys auf dem reader aus")
	}
	if _, err := s.r.ExecContext(ctx, `insert into admin (id, username, pw_hash, updated_at) values (1, 'a', 'b', 0)`); err == nil {
		t.Error("reader darf nicht schreiben")
	}
}

func TestTargets(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()

	a := newTarget(t, s, "Website", true)
	b := newTarget(t, s, "Intern", false)

	got, err := s.Target(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Website" || !got.Public || got.Paused || !got.CreatedAt.Equal(start) {
		t.Fatalf("target %+v", got)
	}

	clock.Advance(time.Minute)
	got.Name = "Website neu"
	got.Keyword = "ok"
	got.Sort = -1
	if err := s.UpdateTarget(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPaused(ctx, b, true); err != nil {
		t.Fatal(err)
	}

	all, err := s.Targets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != a || all[0].Name != "Website neu" || all[0].Keyword != "ok" {
		t.Fatalf("Targets %+v", all)
	}
	if !all[0].UpdatedAt.Equal(start.Add(time.Minute)) || !all[0].CreatedAt.Equal(start) {
		t.Errorf("zeiten %v %v", all[0].CreatedAt, all[0].UpdatedAt)
	}
	if !all[1].Paused {
		t.Error("SetPaused wirkt nicht")
	}

	pub, err := s.PublicTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 1 || pub[0].ID != a {
		t.Fatalf("PublicTargets %+v", pub)
	}

	if err := s.DeleteTarget(ctx, b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Target(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Errorf("geloeschtes ziel: %v", err)
	}
	if err := s.DeleteTarget(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Errorf("doppelt geloescht: %v", err)
	}
	if err := s.UpdateTarget(ctx, check.Target{ID: 999, Kind: check.KindTCP, IntervalS: 60, TimeoutMs: 1000, FailThreshold: 1}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update unbekannt: %v", err)
	}
	if err := s.SetPaused(ctx, 999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("pause unbekannt: %v", err)
	}
}

func TestCascade(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	id := newTarget(t, s, "Website", true)

	addCheck(t, s, id, start, check.Result{OK: true, LatencyMs: 10})
	exec(t, s, `insert into hourly values (?, ?, 1, 1, 10, 10, 10)`, id, hourStart(start)-hour)
	if _, err := s.OpenIncident(ctx, id, start, "status 503"); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteTarget(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"checks", "hourly", "incidents"} {
		if n := count(t, s, `select count(*) from `+table); n != 0 {
			t.Errorf("%s: %d zeilen nach dem loeschen", table, n)
		}
	}
}

func TestChecks(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()
	id := newTarget(t, s, "FiveM", true)

	players, maxPlayers := 12, 64
	results := []check.Result{
		{OK: false, LatencyMs: 10000, Err: "zeitueberschreitung"},
		{OK: true, LatencyMs: 40, StatusCode: 200, Players: &players, MaxPlayers: &maxPlayers, Version: "FXServer v1"},
		{OK: true, LatencyMs: 42, StatusCode: 200},
	}
	// der erste liegt noch in der vorigen stunde
	at := start.Add(-31 * time.Minute)
	for _, r := range results {
		addCheck(t, s, id, at, r)
		at = at.Add(time.Minute + 30*time.Second)
	}
	clock.Advance(5 * time.Minute)

	last, err := s.LastChecks(ctx, id, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 2 || last[0].LatencyMs != 42 || last[1].LatencyMs != 40 {
		t.Fatalf("LastChecks %+v", last)
	}
	if last[0].Players != nil || last[0].Version != "" {
		t.Errorf("leere felder nicht leer: %+v", last[0])
	}
	if *last[1].Players != 12 || *last[1].MaxPlayers != 64 || last[1].Version != "FXServer v1" {
		t.Errorf("fivem-felder %+v", last[1])
	}
	if n := count(t, s, `select count(*) from checks where version is null`); n != 2 {
		t.Errorf("leere version soll null sein, %d zeilen", n)
	}

	running, err := s.HourChecks(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(running) != 2 || !running[0].OK || running[0].LatencyMs != 40 {
		t.Fatalf("HourChecks %+v", running)
	}
}

func TestAdmin(t *testing.T) {
	s, clock := newStore(t)
	ctx := context.Background()

	if _, _, err := s.Admin(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("leer: %v", err)
	}
	if err := s.SetAdmin(ctx, "leon", "hash1"); err != nil {
		t.Fatal(err)
	}
	token, _, err := s.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	if err := s.SetAdmin(ctx, "leon", "hash2"); err != nil {
		t.Fatal(err)
	}
	user, hash, err := s.Admin(ctx)
	if err != nil || user != "leon" || hash != "hash2" {
		t.Fatalf("Admin %q %q %v", user, hash, err)
	}
	if n := count(t, s, `select count(*) from admin where updated_at = ?`, start.Add(time.Hour).Unix()); n != 1 {
		t.Error("updated_at nicht gesetzt")
	}
	if _, err := s.Session(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("sitzung nach passwortwechsel: %v", err)
	}
}

func TestBackup(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	newTarget(t, s, "Website", true)

	file := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(ctx, file); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, file); err == nil {
		t.Error("vorhandene datei ueberschrieben")
	}
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	b, err := Open(ctx, file, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if list, err := b.Targets(ctx); err != nil || len(list) != 1 {
		t.Fatalf("backup: %v %v", list, err)
	}
}

func TestPrune(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	id := newTarget(t, s, "Website", true)

	for _, age := range []time.Duration{0, 29 * 24 * time.Hour, 31 * 24 * time.Hour} {
		addCheck(t, s, id, start.Add(-age), check.Result{OK: true})
	}
	cur := hourStart(start)
	for _, days := range []int64{1, 399, 401} {
		exec(t, s, `insert into hourly values (?, ?, 1, 1, 0, 0, 0)`, id, cur-days*24*hour)
	}
	const day = 24 * hour
	// nur der vor 401 tagen beendete vorfall ist weg, ein offener bleibt egal wie alt
	for _, days := range []int64{399, 401} {
		exec(t, s, `insert into incidents (target_id, started_at, ended_at) values (?, ?, ?)`,
			id, start.Unix()-(days+1)*day, start.Unix()-days*day)
	}
	exec(t, s, `insert into incidents (target_id, started_at, ended_at) values (?, ?, ?)`,
		id, start.Unix()-500*day, start.Unix()-10*day)
	exec(t, s, `insert into incidents (target_id, started_at) values (?, ?)`, id, start.Unix()-500*day)

	if err := s.Prune(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `select count(*) from checks`); n != 2 {
		t.Errorf("%d checks uebrig, want 2", n)
	}
	if n := count(t, s, `select count(*) from hourly`); n != 2 {
		t.Errorf("%d stundenwerte uebrig, want 2", n)
	}
	if n := count(t, s, `select count(*) from incidents where ended_at < ?`, start.Unix()-400*day); n != 0 {
		t.Errorf("%d alte vorfaelle uebrig", n)
	}
	if n := count(t, s, `select count(*) from incidents`); n != 3 {
		t.Errorf("%d vorfaelle uebrig, want 3", n)
	}
}
