package clock

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type Real struct{}

func (Real) Now() time.Time { return time.Now().UTC() }

type Manual struct {
	mu  sync.RWMutex
	now time.Time
}

func NewManual(now time.Time) *Manual {
	return &Manual{now: now.UTC()}
}

func (m *Manual) Now() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.now
}

func (m *Manual) Advance(duration time.Duration) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(duration)
	return m.now
}

func (m *Manual) Set(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now.UTC()
}
