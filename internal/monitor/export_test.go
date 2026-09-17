package monitor

import (
	"context"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
)

// SetCheck gibt es nur in den tests dieses pakets. Der E2E-Test in monitor_test braucht einen
// check, der httptest auf loopback erreicht, und einen schnelleren takt.
func SetCheck(m *Manager, unit time.Duration, fn func(context.Context, check.Target) check.Result) {
	m.unit, m.check = unit, fn
}
