package monitor

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetry(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	m := f.manager(time.Hour)

	a := f.target("Website", 1, false)
	b := f.target("Portal", 1, false)
	downOnly, err := f.st.OpenIncident(ctx, a, start.Add(-time.Hour), "status 503")
	if err != nil {
		t.Fatal(err)
	}
	// beendet, keine der beiden mails raus: nur die wiederkehr wird nachgeholt
	closed, err := f.st.OpenIncident(ctx, b, start.Add(-2*time.Hour), "zeitueberschreitung")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.CloseIncident(ctx, closed, start.Add(-90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// zu alt fuer den nachversand
	old, err := f.st.OpenIncident(ctx, f.target("Alt", 1, false), start.Add(-48*time.Hour), "status 500")
	if err != nil {
		t.Fatal(err)
	}

	f.mailErr = errors.New("451")
	m.retry(ctx)
	if n := f.mailCount(); n != 0 {
		t.Fatalf("erster durchlauf verschickt %d mails", n)
	}

	m.retry(ctx)
	if n := f.mailCount(); n != 0 {
		t.Fatalf("fehlschlag gespeichert: %d", n)
	}

	// jede mail hatte ihren einen versuch, auch wenn der server jetzt wieder geht
	f.mailErr = nil
	m.retry(ctx)
	if n := f.mailCount(); n != 0 {
		t.Fatalf("zweiter versuch: %d mails", n)
	}

	// neuer vorfall wird nach zwei durchlaeufen genau einmal nachgeschickt
	if err := f.st.CloseIncident(ctx, downOnly, start); err != nil {
		t.Fatal(err)
	}
	m.retry(ctx)
	m.retry(ctx)
	m.retry(ctx)
	if n := f.mailCount(); n != 1 {
		t.Fatalf("%d mails, want 1", n)
	}
	got := f.mail()
	if got.ID != downOnly || got.Open() {
		t.Errorf("nachgeholt %+v", got)
	}
	for _, id := range []int64{closed, old} {
		if _, ok := m.pending[mailKey{id, false}]; ok {
			t.Errorf("vorfall %d als ausfall-mail vorgemerkt", id)
		}
	}
}
