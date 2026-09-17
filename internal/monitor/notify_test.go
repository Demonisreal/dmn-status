package monitor

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/alert"
	"github.com/Demonisreal/dmn-status/internal/alert/smtptest"
	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/store"
)

func (f *fake) waitIncident(id int64) store.Incident {
	f.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		inc, err := f.st.OpenIncidentFor(context.Background(), id)
		if err == nil {
			return inc
		}
		if !errors.Is(err, store.ErrNotFound) {
			f.t.Fatal(err)
		}
		if time.Now().After(deadline) {
			f.t.Fatal("kein vorfall")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFailedMailRetriedOnce(t *testing.T) {
	f := newFake(t)
	id := f.target("Website", 1, false)
	m := f.manager(time.Millisecond)
	var attempts atomic.Int32
	send := m.mail
	m.mail = func(ctx context.Context, inc store.Incident) error {
		attempts.Add(1)
		return send(ctx, inc)
	}
	f.mailErr = alert.ErrLimit
	cancel := run(t, m)
	inc := f.waitIncident(id)
	cancel()
	waitDone(t, m)

	if n := attempts.Load(); n != 1 {
		t.Fatalf("%d versuche im runner, want 1", n)
	}
	if list, _ := f.st.UnmailedIncidents(context.Background(), start.Add(-time.Hour)); len(list) != 1 {
		t.Fatalf("fehlgeschlagene mail vermerkt: %+v", list)
	}

	f.mu.Lock()
	f.mailErr = nil
	f.mu.Unlock()
	for range 5 {
		m.retry(context.Background())
	}
	if n := attempts.Load(); n != 2 {
		t.Errorf("%d versuche nach fuenf wartungslaeufen, want 2", n)
	}
	if got := f.mail(); got.ID != inc.ID || !got.Open() {
		t.Errorf("nachgeholt %+v", got)
	}
	if list, _ := f.st.UnmailedIncidents(context.Background(), start.Add(-time.Hour)); len(list) != 0 {
		t.Errorf("nach dem nachversand offen: %+v", list)
	}
}

func TestRetryGivesUpAfterSecondFailure(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	m := f.manager(time.Hour)
	if _, err := f.st.OpenIncident(ctx, f.target("Website", 1, false), start.Add(-time.Minute), "status 503"); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	m.mail = func(context.Context, store.Incident) error {
		attempts.Add(1)
		return errors.New("535 authentication failed")
	}
	for range 10 {
		m.retry(ctx)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("%d versuche, want 1", n)
	}
}

// am limit ist die mail nie beim server angekommen, der naechste lauf versucht es wieder
func TestRetryKeepsTryingOnErrLimit(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	m := f.manager(time.Hour)
	inc, err := f.st.OpenIncident(ctx, f.target("Website", 1, false), start.Add(-time.Minute), "status 503")
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	limit := true
	m.mail = func(context.Context, store.Incident) error {
		attempts.Add(1)
		if limit {
			return alert.ErrLimit
		}
		return nil
	}
	for range 10 {
		m.retry(ctx)
	}
	// der erste lauf merkt sich die mail nur
	if n := attempts.Load(); n != 9 {
		t.Fatalf("%d versuche am limit, want 9", n)
	}

	limit = false
	for range 5 {
		m.retry(ctx)
	}
	if n := attempts.Load(); n != 10 {
		t.Errorf("%d versuche nach dem limit, want 10", n)
	}
	if list, _ := f.st.UnmailedIncidents(ctx, start.Add(-time.Hour)); len(list) != 0 {
		t.Errorf("vorfall %d nach dem nachversand offen: %+v", inc, list)
	}
}

// restore hat den offenen vorfall nicht gesehen: round findet ihn beim eroeffnen und mailt nicht
func TestRoundKeepsExistingIncident(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	id := f.target("Website", 1, false)
	existing, err := f.st.OpenIncident(ctx, id, start.Add(-time.Hour), "status 503")
	if err != nil {
		t.Fatal(err)
	}
	m := f.manager(time.Hour)
	tg, err := f.st.Target(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	st := m.round(ctx, tg, state{})
	if !st.open || st.incident != existing || !st.startedAt.Equal(start.Add(-time.Hour)) {
		t.Fatalf("zustand %+v", st)
	}
	if n := f.mailCount(); n != 0 {
		t.Errorf("%d mails fuer einen schon offenen vorfall", n)
	}

	f.ok.Store(true)
	st = m.round(ctx, tg, st)
	if st.open {
		t.Fatal("vorfall bleibt offen")
	}
	if up := f.mail(); up.ID != existing || up.Open() {
		t.Errorf("wiederkehr %+v", up)
	}
}

func TestMailLimitAcrossTargets(t *testing.T) {
	f := newFake(t)
	s := smtptest.Start(t, "user", "pass")
	host, port, _ := net.SplitHostPort(s.Addr())
	p, _ := strconv.Atoi(port)
	mailer := &alert.Mailer{
		Host: host, Port: p, User: "user", Pass: "pass",
		From: "dmn-status <noreply@example.test>",
		To:   []string{"admin@example.test"},
		TLS:  &tls.Config{RootCAs: s.CertPool()},
		Now:  f.clock.Now,
	}

	const targets = 21
	for i := range targets {
		f.target("Ziel "+strconv.Itoa(i), 1, false)
	}
	m := New(f.st, check.Checker{}, mailer)
	m.unit, m.now = time.Millisecond, f.clock.Now
	m.check = func(context.Context, check.Target) check.Result {
		return check.Result{Err: "zeitueberschreitung"}
	}
	run(t, m)

	s.Wait(t, 20, wait)
	ctx := context.Background()
	deadline := time.Now().Add(wait)
	for {
		list, err := f.st.UnmailedIncidents(ctx, start.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		recent, err := f.st.RecentIncidents(ctx, 100, start.Add(-time.Hour), false)
		if err != nil {
			t.Fatal(err)
		}
		if len(recent) == targets && len(list) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d vorfaelle, %d ohne mail", len(recent), len(list))
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(s.Messages()); n != 20 {
		t.Errorf("%d mails, want 20", n)
	}
}
