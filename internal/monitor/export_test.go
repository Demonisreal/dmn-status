package monitor

import (
	"context"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
)

func SetCheck(m *Manager, unit time.Duration, fn func(context.Context, check.Target) check.Result) {
	m.unit, m.check = unit, fn
}
