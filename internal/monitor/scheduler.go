package monitor

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Demonisreal/dmn-status/internal/alert"
	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/store"
)

// jeder http-check haelt bis zu 1 MiB body, bei 128 MB fuer den container reicht das
const maxParallel = 8

// checkNowGap ist der Mindestabstand zwischen zwei Anstoessen ueber CheckNow je Runner
const checkNowGap = 10 * time.Second

// Stat ist der letzte bekannte Stand eines Ziels fuer /metrics. Die Zaehler laufen seit dem
// Start des Prozesses und ueberleben ein Reload.
type Stat struct {
	Checked    bool
	OK         bool
	LatencyMs  int
	At         time.Time
	ChecksOK   uint64
	ChecksFail uint64
}

type Manager struct {
	store *store.Store
	check func(context.Context, check.Target) check.Result
	mail  func(context.Context, store.Incident) error // nil ohne smtp
	now   func() time.Time
	unit  time.Duration // eine sekunde im intervall, im test kleiner
	sem   chan struct{}

	// pending gehoert der wartungsschleife, siehe retry
	pending map[mailKey]bool

	// ops serialisiert Start, Reload, Delete und Wait. Die Runner nehmen nur mu, sonst
	// koennte stop beim warten auf einen Runner haengen, der gerade seine Stat schreibt.
	ops sync.Mutex
	ctx context.Context
	wg  sync.WaitGroup

	mu      sync.Mutex
	runners map[int64]*runner
	stats   map[int64]*Stat
}

type runner struct {
	cancel context.CancelFunc
	done   chan struct{}
	now    chan struct{}
	nudged time.Time // letzter angekommener CheckNow, unter mu
}

// New baut den Manager. mailer darf nil sein, dann gibt es Vorfaelle ohne Mail.
func New(st *store.Store, c check.Checker, mailer *alert.Mailer) *Manager {
	m := &Manager{
		store:   st,
		check:   c.Run,
		now:     time.Now,
		unit:    time.Second,
		sem:     make(chan struct{}, maxParallel),
		runners: map[int64]*runner{},
		stats:   map[int64]*Stat{},
	}
	if mailer != nil {
		m.mail = func(ctx context.Context, i store.Incident) error {
			if i.Open() {
				return mailer.Down(ctx, i.TargetName, i.Cause, i.StartedAt)
			}
			return mailer.Up(ctx, i.TargetName, i.StartedAt, i.EndedAt)
		}
	}
	return m
}

// Start startet einen Runner je nicht pausiertem Ziel. Alle Runner laufen, bis ctx endet.
func (m *Manager) Start(ctx context.Context) error {
	m.ops.Lock()
	defer m.ops.Unlock()
	m.ctx = ctx

	targets, err := m.store.Targets(ctx)
	if err != nil {
		return err
	}
	for _, t := range targets {
		if !t.Paused {
			m.start(t)
		}
	}
	return nil
}

// Reload stoppt den Runner eines Ziels und startet ihn mit den aktuellen Daten neu, nach
// Anlegen, Bearbeiten oder Pausieren. Ein pausiertes Ziel verliert dabei seinen offenen
// Vorfall, ohne Mail.
func (m *Manager) Reload(ctx context.Context, id int64) error {
	m.ops.Lock()
	defer m.ops.Unlock()

	m.stop(id)
	// der runner muss auch dann wieder starten, wenn die anfrage inzwischen abgebrochen ist
	ctx = context.WithoutCancel(ctx)
	t, err := m.store.Target(ctx, id)
	if err != nil {
		return err
	}
	if t.Paused {
		// erst nach stop, sonst kann eine laufende runde den vorfall noch mit mail schliessen
		// oder gleich einen neuen eroeffnen
		return m.store.ClosePausedIncident(ctx, id)
	}
	if m.ctx.Err() == nil {
		m.start(t)
	}
	return nil
}

// Delete stoppt den Runner und loescht das Ziel in einem Schritt. Erst stoppen, sonst schreibt
// ein laufender Check noch in ein geloeschtes Ziel. Getrennt koennte zwischen beidem ein Reload
// den Runner fuer das noch vorhandene Ziel neu starten.
func (m *Manager) Delete(ctx context.Context, id int64) error {
	m.ops.Lock()
	defer m.ops.Unlock()

	m.stop(id)
	// wie bei Reload: ist der runner einmal weg, soll das loeschen nicht am client scheitern
	ctx = context.WithoutCancel(ctx)
	if err := m.store.DeleteTarget(ctx, id); err != nil {
		// das ziel steht noch in der datenbank und wuerde sonst bis zum neustart nicht geprueft
		if !errors.Is(err, store.ErrNotFound) {
			if t, terr := m.store.Target(ctx, id); terr != nil {
				slog.Error("ziel nach loeschfehler laden", "target", id, "err", terr)
			} else if !t.Paused && m.ctx.Err() == nil {
				m.start(t)
			}
		}
		return err
	}
	m.mu.Lock()
	delete(m.stats, id)
	m.mu.Unlock()
	return nil
}

// CheckNow stoesst einen Check ausserhalb des Intervalls an. Laeuft fuer das Ziel gerade ein
// Check, faellt der Anstoss weg. false heisst, es gibt keinen Runner (pausiert oder unbekannt)
// oder der letzte Anstoss liegt noch keine checkNowGap zurueck.
func (m *Manager) CheckNow(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runners[id]
	if r == nil {
		return false
	}
	now := m.now()
	if !r.nudged.IsZero() && now.Sub(r.nudged) < checkNowGap {
		return false
	}
	// ungepuffert: kommt nur an, wenn der runner gerade wartet. Nur ein angekommener anstoss
	// zaehlt fuer die sperrfrist, ein weggefallener hat keinen check ausgeloest.
	select {
	case r.now <- struct{}{}:
		r.nudged = now
	default:
	}
	return true
}

// Wait wartet nach dem Ende des Start-Contexts, bis alle Runner und die Wartung fertig sind.
func (m *Manager) Wait() {
	m.ops.Lock()
	defer m.ops.Unlock()
	m.wg.Wait()
}

func (m *Manager) Snapshot() map[int64]Stat {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int64]Stat, len(m.stats))
	for id, s := range m.stats {
		out[id] = *s
	}
	return out
}

func (m *Manager) start(t check.Target) {
	ctx, cancel := context.WithCancel(m.ctx)
	r := &runner{cancel: cancel, done: make(chan struct{}), now: make(chan struct{})}
	m.mu.Lock()
	m.runners[t.ID] = r
	m.mu.Unlock()

	m.wg.Go(func() {
		defer close(r.done)
		m.run(ctx, t, r.now)
	})
}

func (m *Manager) stop(id int64) {
	m.mu.Lock()
	r := m.runners[id]
	delete(m.runners, id)
	m.mu.Unlock()
	if r != nil {
		r.cancel()
		<-r.done
	}
}

func (m *Manager) run(ctx context.Context, t check.Target, now <-chan struct{}) {
	st := m.restore(ctx, t)
	interval := time.Duration(t.IntervalS) * m.unit

	// zufaelliger start, damit nach einem neustart nicht alle ziele gleichzeitig pruefen
	timer := time.NewTimer(rand.N(interval)) //nolint:gosec // nur jitter, nicht sicherheitsrelevant
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-now:
		}
		st = m.round(ctx, t, st)
		timer.Reset(interval - interval/10 + rand.N(interval/5+1)) //nolint:gosec // nur jitter
	}
}

// restore holt offenen Vorfall und laufende Fehlerserie aus der Datenbank. Ohne das gaebe
// es nach jedem Neustart oder Reload eine zweite Ausfall-Mail.
func (m *Manager) restore(ctx context.Context, t check.Target) state {
	var st state
	inc, err := m.store.OpenIncidentFor(ctx, t.ID)
	switch {
	case err == nil:
		st.open, st.startedAt, st.incident = true, inc.StartedAt, inc.ID
	case !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil:
		slog.Error("offenen vorfall laden", "target", t.ID, "err", err)
	}

	// fehler vor dem ende des letzten vorfalls zaehlen nicht mehr, nach einer pause beginnt
	// die serie neu
	end, err := m.store.LastIncidentEnd(ctx, t.ID)
	if err != nil && ctx.Err() == nil {
		slog.Error("letztes vorfallende laden", "target", t.ID, "err", err)
	}
	last, err := m.store.LastChecks(ctx, t.ID, t.FailThreshold)
	if err != nil && ctx.Err() == nil {
		slog.Error("letzte checks laden", "target", t.ID, "err", err)
	}
	for _, c := range last {
		if c.OK || !c.At.After(end) {
			break
		}
		st.fails++
		st.firstFail = c.At
		if st.cause == "" {
			st.cause = c.Err
		}
	}

	if len(last) > 0 {
		m.mu.Lock()
		if m.stats[t.ID] == nil {
			m.stats[t.ID] = &Stat{Checked: true, OK: last[0].OK, LatencyMs: last[0].LatencyMs, At: last[0].At}
		}
		m.mu.Unlock()
	}
	return st
}

func (m *Manager) round(ctx context.Context, t check.Target, st state) state {
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		return st
	}
	at := m.now()
	res := m.check(ctx, t)
	<-m.sem
	// ein durch Reload oder Shutdown abgebrochener check ist kein ausfall
	if ctx.Err() != nil {
		return st
	}
	m.record(t.ID, at, res)

	// die angefangene runde noch zu ende schreiben, auch wenn gleich ein Reload kommt
	wctx := context.WithoutCancel(ctx)
	if err := m.store.InsertCheck(wctx, t.ID, at, res); err != nil {
		slog.Error("check speichern", "target", t.ID, "err", err)
	}

	nst, ev := next(st, res.OK, res.Err, at, t.FailThreshold)
	switch ev {
	case down:
		id, err := m.store.OpenIncident(wctx, t.ID, nst.startedAt, nst.cause)
		if errors.Is(err, store.ErrIncidentOpen) {
			// restore ist gescheitert, der vorfall steht schon drin und bekommt keine zweite mail
			inc, ferr := m.store.OpenIncidentFor(wctx, t.ID)
			if ferr == nil {
				nst.startedAt, nst.incident = inc.StartedAt, inc.ID
				return nst
			}
			err = ferr
		}
		if err != nil {
			slog.Error("vorfall eroeffnen", "target", t.ID, "err", err)
			nst.open = false
			return nst
		}
		nst.incident = id
		_ = m.notify(ctx, store.Incident{ID: id, TargetID: t.ID, TargetName: t.Name, StartedAt: nst.startedAt, Cause: nst.cause})

	case up:
		err := m.store.CloseIncident(wctx, st.incident, at)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			slog.Error("vorfall schliessen", "target", t.ID, "incident", st.incident, "err", err)
			// offen lassen, der naechste erfolgreiche check versucht es noch einmal
			nst.open = true
			return nst
		}
		_ = m.notify(ctx, store.Incident{ID: st.incident, TargetID: t.ID, TargetName: t.Name, StartedAt: st.startedAt, EndedAt: at})
	}
	return nst
}

func (m *Manager) record(id int64, at time.Time, res check.Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.stats[id]
	if s == nil {
		s = &Stat{}
		m.stats[id] = s
	}
	s.Checked, s.OK, s.LatencyMs, s.At = true, res.OK, res.LatencyMs, at
	if res.OK {
		s.ChecksOK++
	} else {
		s.ChecksFail++
	}
}

// notify schickt die Mail und merkt sie sich nur bei Erfolg, sonst holt retry sie nach.
// Der SMTP-Fehler wird schon hier geloggt, retry braucht ihn nur fuer ErrLimit.
func (m *Manager) notify(ctx context.Context, inc store.Incident) error {
	if m.mail == nil {
		return nil
	}
	if err := m.mail(ctx, inc); err != nil {
		slog.Warn("mail", "target", inc.TargetID, "incident", inc.ID, "up", !inc.Open(), "err", err)
		return err
	}
	if err := m.store.MarkMailed(context.WithoutCancel(ctx), inc.ID, !inc.Open()); err != nil {
		slog.Error("mail vermerken", "incident", inc.ID, "err", err)
	}
	return nil
}
