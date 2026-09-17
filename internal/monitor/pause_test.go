package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/store"
)

func TestPauseClosesIncidentWithoutMail(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 2, false)
	m := f.manager(time.Millisecond)
	run(t, m)
	ctx := context.Background()

	down := f.mail()
	f.clock.Advance(10 * time.Minute)
	if err := f.st.SetPaused(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx, id); err != nil {
		t.Fatal(err)
	}
	if m.CheckNow(id) {
		t.Fatal("runner trotz pause")
	}

	if _, err := f.st.OpenIncidentFor(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("vorfall nach pause offen: %v", err)
	}
	list, err := f.st.TargetIncidents(ctx, id, 10, start.Add(-time.Hour))
	if err != nil || len(list) != 1 {
		t.Fatalf("vorfaelle %+v %v", list, err)
	}
	if got := list[0]; got.ID != down.ID || !got.EndedAt.Equal(start.Add(10*time.Minute)) {
		t.Errorf("beendet %+v", got)
	}

	// weder Reload noch der nachversand schicken eine wiederkehr
	for range 3 {
		m.retry(ctx)
	}
	if n := f.mailCount(); n != 1 {
		t.Errorf("%d mails, want nur die ausfall-mail", n)
	}
}

func TestRestoreAfterPause(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	id := f.target("Website", 3, false)
	for i := range 2 {
		if err := f.st.InsertCheck(ctx, id, start.Add(time.Duration(i)*time.Minute), check.Result{Err: "status 503"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.st.OpenIncident(ctx, id, start, "status 503"); err != nil {
		t.Fatal(err)
	}
	f.clock.Set(start.Add(5 * time.Minute))
	if err := f.st.ClosePausedIncident(ctx, id); err != nil {
		t.Fatal(err)
	}

	m := f.manager(time.Hour)
	tg, err := f.st.Target(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st := m.restore(ctx, tg); st.open || st.fails != 0 {
		t.Fatalf("fehler von vor der pause zaehlen weiter: %+v", st)
	}

	if err := f.st.InsertCheck(ctx, id, start.Add(6*time.Minute), check.Result{Err: "zeitueberschreitung"}); err != nil {
		t.Fatal(err)
	}
	if st := m.restore(ctx, tg); st.fails != 1 || st.cause != "zeitueberschreitung" || !st.firstFail.Equal(start.Add(6*time.Minute)) {
		t.Errorf("neue serie %+v", st)
	}
	if list, _ := f.st.UnmailedIncidents(ctx, start.Add(-time.Hour)); len(list) != 0 {
		t.Errorf("pausierter vorfall im nachversand: %+v", list)
	}
}
