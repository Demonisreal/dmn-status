package monitor

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/store"
)

// hanging laesst jeden check haengen, bis sein context endet, und zaehlt gleichzeitige laeufe
type hanging struct {
	running, peak atomic.Int32
	started       chan string
}

func (h *hanging) run(ctx context.Context, t check.Target) check.Result {
	n := h.running.Add(1)
	defer h.running.Add(-1)
	for {
		p := h.peak.Load()
		if n <= p || h.peak.CompareAndSwap(p, n) {
			break
		}
	}
	h.started <- t.Name
	<-ctx.Done()
	return check.Result{Err: "zeitueberschreitung"}
}

func (h *hanging) next(t *testing.T) string {
	t.Helper()
	select {
	case name := <-h.started:
		return name
	case <-time.After(wait):
		t.Fatal("kein check")
	}
	return ""
}

func TestReloadDuringRunningCheck(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 1, false)
	m := f.manager(time.Millisecond)
	h := &hanging{started: make(chan string, 10)}
	m.check = h.run
	run(t, m)
	ctx := context.Background()

	if got := h.next(t); got != "Website" {
		t.Fatalf("erster check %q", got)
	}

	tg, err := f.st.Target(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	tg.Name = "Website neu"
	if err := f.st.UpdateTarget(ctx, tg); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.Reload(ctx, id) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(wait):
		t.Fatal("Reload wartet auf den haengenden check")
	}

	if got := h.next(t); got != "Website neu" {
		t.Fatalf("check nach Reload mit %q", got)
	}
	if p := h.peak.Load(); p != 1 {
		t.Errorf("%d checks gleichzeitig fuer ein ziel", p)
	}

	// der abgebrochene check ist weder gespeichert noch gezaehlt noch ein vorfall
	if list, err := f.st.LastChecks(ctx, id, 10); err != nil || len(list) != 0 {
		t.Errorf("gespeicherte checks %+v %v", list, err)
	}
	if s := m.Snapshot()[id]; s.ChecksFail != 0 || s.Checked {
		t.Errorf("snapshot %+v", s)
	}
	if list, err := f.st.RecentIncidents(ctx, 10, start.Add(-time.Hour), false); err != nil || len(list) != 0 {
		t.Errorf("vorfaelle %+v %v", list, err)
	}
}

func TestConcurrentReloads(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 1, false)
	m := f.manager(time.Millisecond)
	h := &hanging{started: make(chan string, 100)}
	m.check = h.run
	run(t, m)
	h.next(t)

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := m.Reload(context.Background(), id); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	m.mu.Lock()
	n := len(m.runners)
	m.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d runner nach parallelen Reloads", n)
	}
	h.next(t)
	// ein alter runner, der noch liefe, haette jetzt einen zweiten check gestartet
	time.Sleep(100 * time.Millisecond)
	if p := h.peak.Load(); p != 1 {
		t.Errorf("%d checks gleichzeitig", p)
	}
}

func TestReloadRacingDelete(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 1, false)
	m := f.manager(time.Millisecond)
	run(t, m)
	f.call()
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := m.Delete(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if err := m.Reload(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := m.Delete(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("letztes Delete: %v", err)
	}

	if m.CheckNow(id) {
		t.Fatal("runner nach dem letzten Delete")
	}
	for len(f.calls) > 0 {
		<-f.calls
	}
	select {
	case <-f.calls:
		t.Fatal("check nach Delete")
	case <-time.After(100 * time.Millisecond):
	}
}

func (m *Manager) runnerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runners)
}

// der alte ablauf im admin: runner stoppen und DeleteTarget getrennt. Ein Reload dazwischen
// startet den runner fuer das noch vorhandene ziel neu, und der ueberlebt das loeschen.
func TestReloadBetweenStopAndDelete(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 1, false)
	m := f.manager(time.Hour)
	run(t, m)
	ctx := context.Background()

	m.stop(id)
	if err := m.Reload(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteTarget(ctx, id); err != nil {
		t.Fatal(err)
	}
	n := m.runnerCount()
	m.stop(id)
	if n != 1 {
		t.Fatalf("%d runner, die race tritt nicht mehr auf", n)
	}
}

func TestDelete(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 1, false)
	m := f.manager(time.Millisecond)
	run(t, m)
	f.call()
	ctx := context.Background()

	if err := m.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	// vor dem naechsten Reload pruefen, das wuerde einen uebrigen runner selbst stoppen
	if n := m.runnerCount(); n != 0 {
		t.Fatalf("%d runner nach Delete", n)
	}
	if err := m.Reload(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Reload nach Delete: %v", err)
	}
	if n := m.runnerCount(); n != 0 {
		t.Fatalf("%d runner nach Reload", n)
	}
	if _, ok := m.Snapshot()[id]; ok {
		t.Error("snapshot enthaelt geloeschtes ziel")
	}
	if err := m.Delete(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("zweites Delete: %v", err)
	}
}

// scheitert das loeschen, bleibt das ziel in der datenbank und muss weiter geprueft werden
func TestDeleteFailedRestartsRunner(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 1, false)
	m := f.manager(time.Millisecond)
	run(t, m)
	f.call()
	ctx := context.Background()

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(f.path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `create trigger no_delete before delete on targets
		begin select raise(abort, 'gesperrt'); end`); err != nil {
		t.Fatal(err)
	}

	if err := m.Delete(ctx, id); err == nil || errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Delete: %v", err)
	}
	if n := m.runnerCount(); n != 1 {
		t.Fatalf("%d runner nach gescheitertem Delete", n)
	}
	for len(f.calls) > 0 {
		<-f.calls
	}
	f.call()
}

func TestDeleteRacingReload(t *testing.T) {
	f := newFake(t)
	f.ok.Store(true)
	id := f.target("Website", 1, false)
	m := f.manager(time.Millisecond)
	run(t, m)
	f.call()
	ctx := context.Background()

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := m.Reload(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Error(err)
			}
		})
	}
	wg.Go(func() {
		if err := m.Delete(ctx, id); err != nil {
			t.Error(err)
		}
	})
	wg.Wait()

	if _, err := f.st.Target(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ziel nach Delete: %v", err)
	}
	if n := m.runnerCount(); n != 0 {
		t.Fatalf("%d runner nach Delete", n)
	}
	for len(f.calls) > 0 {
		<-f.calls
	}
	select {
	case <-f.calls:
		t.Fatal("check nach Delete")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestReloadCanceledRequest(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 1, false)
	m := f.manager(time.Hour)
	run(t, m)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Reload(ctx, id); err != nil {
		t.Fatal(err)
	}
	if n := m.runnerCount(); n != 1 {
		t.Fatalf("%d runner nach Reload mit beendeter anfrage", n)
	}
}
