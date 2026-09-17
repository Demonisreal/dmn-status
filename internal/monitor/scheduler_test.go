package monitor

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/store"
	"github.com/Demonisreal/dmn-status/internal/testutil"
)

var start = time.Date(2026, 3, 10, 12, 30, 0, 0, time.UTC)

const wait = 5 * time.Second

type fake struct {
	t     *testing.T
	st    *store.Store
	path  string
	clock *testutil.Clock

	ok    atomic.Bool
	calls chan check.Target

	mu      sync.Mutex
	mails   []store.Incident
	mailed  chan store.Incident
	mailErr error
}

func newFake(t *testing.T) *fake {
	t.Helper()
	clock := testutil.NewClock(start)
	path := filepath.Join(t.TempDir(), "status.db")
	st, err := store.Open(context.Background(), path, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &fake{
		t:      t,
		st:     st,
		path:   path,
		clock:  clock,
		calls:  make(chan check.Target, 100),
		mailed: make(chan store.Incident, 100),
	}
}

// manager mit falschem checker und fake-mail, eine intervall-sekunde ist eine millisekunde
func (f *fake) manager(unit time.Duration) *Manager {
	m := New(f.st, check.Checker{}, nil)
	m.unit = unit
	m.now = f.clock.Now
	m.check = func(ctx context.Context, t check.Target) check.Result {
		select {
		case f.calls <- t:
		default:
		}
		if f.ok.Load() {
			return check.Result{OK: true, LatencyMs: 12}
		}
		return check.Result{LatencyMs: 5000, Err: "zeitueberschreitung"}
	}
	m.mail = func(ctx context.Context, inc store.Incident) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.mailErr != nil {
			return f.mailErr
		}
		f.mails = append(f.mails, inc)
		f.mailed <- inc
		return nil
	}
	return m
}

func (f *fake) target(name string, threshold int, paused bool) int64 {
	f.t.Helper()
	id, err := f.st.CreateTarget(context.Background(), check.Target{
		Name:          name,
		Kind:          check.KindHTTP,
		Address:       "https://example.com/",
		ExpectStatus:  "200-399",
		IntervalS:     30,
		TimeoutMs:     1000,
		FailThreshold: threshold,
		Paused:        paused,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fake) mail() store.Incident {
	f.t.Helper()
	select {
	case inc := <-f.mailed:
		return inc
	case <-time.After(wait):
		f.t.Fatal("keine mail")
	}
	return store.Incident{}
}

func (f *fake) call() check.Target {
	f.t.Helper()
	select {
	case t := <-f.calls:
		return t
	case <-time.After(wait):
		f.t.Fatal("kein check")
	}
	return check.Target{}
}

func (f *fake) mailCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.mails)
}

// run startet den manager und stoppt ihn am testende, haengt Wait, schlaegt der test fehl
func run(t *testing.T, m *Manager) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		waitDone(t, m)
	})
	return cancel
}

func waitDone(t *testing.T, m *Manager) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		m.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("Wait haengt")
	}
}

func TestDownUp(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 2, false)
	m := f.manager(time.Millisecond)
	run(t, m)

	down := f.mail()
	if !down.Open() || down.TargetID != id || down.TargetName != "Website" || down.Cause != "zeitueberschreitung" {
		t.Fatalf("ausfall-mail %+v", down)
	}
	f.ok.Store(true)
	up := f.mail()
	if up.Open() || up.ID != down.ID || !up.StartedAt.Equal(down.StartedAt) {
		t.Fatalf("wiederkehr-mail %+v", up)
	}

	// MarkMailed laeuft nach der mail, kurz warten bis beides vermerkt ist
	deadline := time.Now().Add(wait)
	for {
		list, err := f.st.UnmailedIncidents(context.Background(), start.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(list) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("nicht vermerkt: %+v", list)
		}
		time.Sleep(10 * time.Millisecond)
	}

	s := m.Snapshot()[id]
	if !s.Checked || s.ChecksFail < 2 || s.ChecksOK < 1 {
		t.Errorf("snapshot %+v", s)
	}
	if n := f.mailCount(); n != 2 {
		t.Errorf("%d mails, want 2", n)
	}
}

func TestPausedAndReload(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 1, true)
	m := f.manager(time.Millisecond)
	run(t, m)
	ctx := context.Background()

	if m.CheckNow(id) {
		t.Fatal("pausiertes ziel hat einen runner")
	}

	if err := f.st.SetPaused(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.call()

	m.mu.Lock()
	old := m.runners[id]
	m.mu.Unlock()

	tg, err := f.st.Target(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	tg.Name = "Website neu"
	if err := f.st.UpdateTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx, id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.done:
	default:
		t.Fatal("alter runner laeuft nach Reload weiter")
	}

	// bis der kanal leer ist, koennen noch checks mit dem alten namen drin liegen
	for {
		if c := f.call(); c.Name == "Website neu" {
			break
		}
	}
	for range 3 {
		if c := f.call(); c.Name != "Website neu" {
			t.Fatalf("check mit altem stand nach Reload: %q", c.Name)
		}
	}

	if err := m.Reload(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("reload unbekannt: %v", err)
	}
}

func TestDeleteStopsChecks(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 1, false)
	m := f.manager(time.Millisecond)
	run(t, m)

	f.call()
	if err := m.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	for len(f.calls) > 0 {
		<-f.calls
	}
	select {
	case <-f.calls:
		t.Fatal("check nach Delete")
	case <-time.After(150 * time.Millisecond):
	}
	if _, ok := m.Snapshot()[id]; ok {
		t.Error("snapshot enthaelt entferntes ziel")
	}
	if m.CheckNow(id) {
		t.Error("CheckNow nach Delete")
	}
}

func TestCheckNow(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 1, false)
	// intervall 30 stunden, ohne anstoss kommt kein check
	m := f.manager(time.Hour)
	run(t, m)
	f.nudge(m, id)
}

func TestCheckNowWhileRunning(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 1, false)
	m := f.manager(time.Hour)
	release := make(chan struct{})
	var n atomic.Int32
	m.check = func(ctx context.Context, t check.Target) check.Result {
		n.Add(1)
		f.calls <- t
		<-release
		return check.Result{OK: true}
	}
	run(t, m)

	deadline := time.Now().Add(wait)
	for len(f.calls) == 0 {
		m.CheckNow(id)
		if time.Now().After(deadline) {
			t.Fatal("CheckNow ohne check")
		}
		time.Sleep(time.Millisecond)
	}
	// waehrend der check haengt, gehen weitere anstoesse verloren
	for range 5 {
		m.CheckNow(id)
	}
	close(release)
	time.Sleep(100 * time.Millisecond)
	if got := n.Load(); got != 1 {
		t.Errorf("%d checks, want 1", got)
	}
}

func TestCheckNowGap(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 1, false)
	m := f.manager(time.Hour)
	run(t, m)
	f.checkNow(m, id)

	steps := []struct {
		name    string
		advance time.Duration
		want    bool
	}{
		{"sofort", 0, false},
		{"nach 9s", 9 * time.Second, false},
		{"nach 10s", time.Second, true},
		{"direkt danach", 0, false},
		{"nach 9,999s", 9999 * time.Millisecond, false},
		{"nach einer minute", time.Minute, true},
	}
	for _, step := range steps {
		f.clock.Advance(step.advance)
		if step.want {
			f.nudge(m, id)
			continue
		}
		if m.CheckNow(id) {
			t.Fatalf("%s: CheckNow innerhalb der sperrfrist", step.name)
		}
	}

	// ein neuer runner nach Reload hat noch keinen anstoss bekommen
	if err := m.Reload(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	f.nudge(m, id)
}

func TestCancelStopsEverything(t *testing.T) {
	f := newFake(t)
	for range 5 {
		f.target("Website", 1, false)
	}
	before := runtime.NumGoroutine()

	m := f.manager(time.Millisecond)
	started := make(chan struct{}, 10)
	m.check = func(ctx context.Context, t check.Target) check.Result {
		started <- struct{}{}
		<-ctx.Done()
		return check.Result{Err: "zeitueberschreitung"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	m.Maintain(ctx, 30)
	for range 5 {
		select {
		case <-started:
		case <-time.After(wait):
			t.Fatal("nicht alle runner gestartet")
		}
	}

	cancel()
	waitDone(t, m)

	deadline := time.Now().Add(wait)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("goroutinen: %d vorher, %d nachher", before, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// abgebrochene checks zaehlen nicht als ausfall
	list, err := f.st.RecentIncidents(context.Background(), 10, start.Add(-time.Hour), false)
	if err != nil || len(list) != 0 {
		t.Errorf("vorfaelle nach abbruch: %+v %v", list, err)
	}
	if err := m.Reload(context.Background(), 1); err != nil {
		t.Errorf("reload nach ende: %v", err)
	}
	if m.CheckNow(1) {
		t.Error("runner nach Reload trotz beendetem context")
	}
}

func TestRestartKeepsIncident(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 2, false)

	m := f.manager(time.Millisecond)
	cancel := run(t, m)
	first := f.mail()
	cancel()
	waitDone(t, m)

	// zweiter prozess, das ziel ist weiter down
	m = f.manager(time.Millisecond)
	run(t, m)
	for len(f.calls) > 0 {
		<-f.calls
	}
	for range 5 {
		f.call()
	}
	if n := f.mailCount(); n != 1 {
		t.Fatalf("%d mails nach neustart, want 1", n)
	}

	f.ok.Store(true)
	up := f.mail()
	if up.ID != first.ID || up.Open() {
		t.Fatalf("wiederkehr %+v, vorfall %d", up, first.ID)
	}
	if _, err := f.st.OpenIncidentFor(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("vorfall noch offen: %v", err)
	}
}

func TestRestartOpenIncidentButUp(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 3, false)
	inc, err := f.st.OpenIncident(context.Background(), id, start.Add(-time.Hour), "status 503")
	if err != nil {
		t.Fatal(err)
	}

	m := f.manager(time.Millisecond)
	run(t, m)
	up := f.mail()
	if up.ID != inc || up.Open() || !up.StartedAt.Equal(start.Add(-time.Hour)) {
		t.Fatalf("wiederkehr %+v", up)
	}
}

func TestRestoreFailSeries(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 3, false)
	ctx := context.Background()
	for i, ok := range []bool{true, false, false} {
		r := check.Result{OK: ok}
		if !ok {
			r.Err = "status 503"
		}
		if err := f.st.InsertCheck(ctx, id, start.Add(time.Duration(i)*time.Minute), r); err != nil {
			t.Fatal(err)
		}
	}

	m := f.manager(time.Hour)
	tg, err := f.st.Target(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	st := m.restore(ctx, tg)
	if st.fails != 2 || st.open || !st.firstFail.Equal(start.Add(time.Minute)) || st.cause != "status 503" {
		t.Fatalf("restore %+v", st)
	}
	if s := m.Snapshot()[id]; !s.Checked || s.OK {
		t.Errorf("snapshot aus letztem check %+v", s)
	}
}

// checkNow stoesst einen check an und wartet, bis er laeuft. Der anstoss kommt nur an, wenn
// die vorige runde fertig ist. Die uhr springt vorher ueber die sperrfrist von CheckNow.
func (f *fake) checkNow(m *Manager, id int64) {
	f.t.Helper()
	f.clock.Advance(checkNowGap)
	f.nudge(m, id)
}

// nudge wiederholt CheckNow, bis der anstoss beim runner ankommt. Waehrend er den zustand
// laedt oder prueft, geht ein anstoss ins leere.
func (f *fake) nudge(m *Manager, id int64) {
	f.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		if !m.CheckNow(id) {
			f.t.Fatal("CheckNow abgelehnt")
		}
		select {
		case <-f.calls:
			return
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			f.t.Fatal("CheckNow ohne check")
		}
	}
}

func TestPauseClosesIncident(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 3, false)
	f.mailErr = errors.New("451")
	m := f.manager(time.Hour)
	run(t, m)
	ctx := context.Background()

	for range 3 {
		f.checkNow(m, id)
	}
	deadline := time.Now().Add(wait)
	var first store.Incident
	for {
		inc, err := f.st.OpenIncidentFor(ctx, id)
		if err == nil {
			first = inc
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("kein vorfall: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	pausedAt := start.Add(time.Minute)
	f.clock.Set(pausedAt)
	if err := f.st.SetPaused(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx, id); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.mailErr = nil
	f.mu.Unlock()

	list, err := f.st.TargetIncidents(ctx, id, 10, start.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != first.ID || !list[0].EndedAt.Equal(pausedAt) || !list[0].MailedDown || !list[0].MailedUp {
		t.Fatalf("nach pause %+v", list)
	}
	// weder wiederkehr noch die liegengebliebene ausfall-mail
	for range 3 {
		m.retry(ctx)
	}
	if n := f.mailCount(); n != 0 {
		t.Fatalf("%d mails nach pause", n)
	}

	f.clock.Advance(time.Minute)
	if err := f.st.SetPaused(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx, id); err != nil {
		t.Fatal(err)
	}
	// die fehler vor der pause zaehlen nicht, zwei neue reichen bei schwelle 3 nicht
	f.checkNow(m, id)
	f.checkNow(m, id)
	if _, err := f.st.OpenIncidentFor(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("vorfall vor der schwelle: %v", err)
	}
	f.checkNow(m, id)
	down := f.mail()
	if !down.Open() || down.ID == first.ID || !down.StartedAt.After(pausedAt) {
		t.Fatalf("neuer vorfall %+v", down)
	}
}
