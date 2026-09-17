// Package testutil enthaelt Hilfen, die mehrere Pakete in Tests brauchen.
package testutil

import (
	"sync"
	"time"
)

// Clock ist eine Uhr, die nur weiterlaeuft, wenn der Test es sagt.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(t time.Time) *Clock {
	return &Clock{now: t}
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}
