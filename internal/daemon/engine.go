// engine.go: the EngineFactory seam a Daemon-hosted thread's Session runs.
package daemon

import (
	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// EngineFactory builds the io.Engine a freshly-registered thread's Session
// runs. Mirrors pod.EngineFactory's shape one level up: pod resolves
// (identity, profile); a daemon-hosted thread resolves off its persisted
// ThreadMeta instead (§1 design blueprint). Called once per thread, the
// first time Session(threadID) mints it.
//
// The `by`-attribution side-channel (approvals.go's byLookup) is
// deliberately NOT a parameter here — threading it through this signature
// would make every EngineFactory implementation carry a param it mostly
// ignores. Instead, a factory that needs it captures the *Daemon by
// reference (assigned after NewDaemon returns — a standard two-phase
// construction: declare `var d *Daemon`, build a factory closure over it,
// then `d = NewDaemon(ctx, Config{EngineFactory: factory, ...})`) and calls
// d.WaitForBy/d.StashBy directly. See conformance's flow-engine tests for
// the pattern in practice.
type EngineFactory func(threadID string, meta contracts.ThreadMeta) agoraio.Engine
