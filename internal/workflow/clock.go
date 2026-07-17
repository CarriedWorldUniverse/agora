package workflow

import "time"

// Clock is injected time — ground rule (repo-wide): no wall-clock in the
// engine's hot paths or in tests. Structurally identical to subagent.Clock;
// kept package-local per house convention (each package owns its own seam)
// rather than importing another package's interface for a one-method shape.
type Clock interface{ Now() time.Time }

// SystemClock is the real-time Clock used outside tests.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FrozenClock returns a fixed instant regardless of how many times Now is
// called — spec §2: "ctx.now (run-start timestamp, frozen — the only
// clock)". A workflow run is handed exactly one FrozenClock, computed once
// at run start; nothing inside the engine ever calls time.Now() again.
type FrozenClock struct{ T time.Time }

func (f FrozenClock) Now() time.Time { return f.T }
