package mcp

import "time"

// Clock abstracts wall time so the catalog TTL and the fs-watcher's
// mtime-sweep are deterministically testable (ground rule 4: no wall-clock
// sleeps in tests).
type Clock interface {
	Now() time.Time
}

// SystemClock is the real clock, used at runtime.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

// FakeClock is a manually-advanced clock for tests.
type FakeClock struct {
	t time.Time
}

func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{t: t} }

func (f *FakeClock) Now() time.Time { return f.t }

// Advance moves the fake clock forward by d.
func (f *FakeClock) Advance(d time.Duration) { f.t = f.t.Add(d) }

// Set moves the fake clock to an absolute time.
func (f *FakeClock) Set(t time.Time) { f.t = t }
