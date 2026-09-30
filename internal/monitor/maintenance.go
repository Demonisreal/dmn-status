package monitor

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Demonisreal/dmn-status/internal/alert"
)

const (
	rollupEvery  = 5 * time.Minute
	cleanupEvery = time.Hour
	retryWindow  = 24 * time.Hour
)

type mailKey struct {
	incident int64
	up       bool
}

func (m *Manager) Maintain(ctx context.Context, retentionDays int) {
	m.wg.Go(func() {
		m.rollup(ctx)
		m.cleanup(ctx, retentionDays)

		roll := time.NewTicker(rollupEvery)
		defer roll.Stop()
		clean := time.NewTicker(cleanupEvery)
		defer clean.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-roll.C:
				m.rollup(ctx)
				m.retry(ctx)
			case <-clean.C:
				m.cleanup(ctx, retentionDays)
			}
		}
	})
}

func (m *Manager) rollup(ctx context.Context) {
	if err := m.store.Rollup(ctx); err != nil && ctx.Err() == nil {
		slog.Error("rollup", "err", err)
	}
}

func (m *Manager) cleanup(ctx context.Context, retentionDays int) {
	if err := m.store.Prune(ctx, retentionDays); err != nil && ctx.Err() == nil {
		slog.Error("alte checks loeschen", "err", err)
	}
	if _, err := m.store.DeleteExpiredSessions(ctx); err != nil && ctx.Err() == nil {
		slog.Error("sitzungen loeschen", "err", err)
	}
	if _, err := m.store.DeleteExpiredDevices(ctx); err != nil && ctx.Err() == nil {
		slog.Error("geraete loeschen", "err", err)
	}
}

func (m *Manager) retry(ctx context.Context) {
	if m.mail == nil {
		return
	}
	list, err := m.store.UnmailedIncidents(ctx, m.now().Add(-retryWindow))
	if err != nil {
		if ctx.Err() == nil {
			slog.Error("offene mails laden", "err", err)
		}
		return
	}

	pending := make(map[mailKey]bool, len(list))
	for _, inc := range list {
		if !inc.Open() && inc.MailedUp {
			continue
		}
		k := mailKey{inc.ID, !inc.Open()}
		retried, seen := m.pending[k]
		if seen && !retried {
			retried = !errors.Is(m.notify(ctx, inc), alert.ErrLimit)
		}
		pending[k] = retried
	}
	m.pending = pending
}
