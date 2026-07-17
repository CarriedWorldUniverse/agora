package ctxmgr

import "github.com/CarriedWorldUniverse/agora/contracts"

// HookRunner is the narrow seam Manager calls through to fire
// Pre/PostCompact (context spec §2 contract 2). The real dispatch engine
// (internal/hooks, U9) is a much larger surface (layering, trust hashes,
// async) than ctxmgr needs to know about — the turn-engine unit wires a
// hooks-package-backed implementation; NoopHookRunner is the working v1
// default (and what every ctxmgr test not exercising hook interaction
// uses), consistent with "Assemble/Compact must be usable standalone".
type HookRunner interface {
	// RunPreCompact fires PreCompact for trigger; halted=true means a
	// handler returned continue:false and the compaction episode must
	// abort (context spec §2 contract 2).
	RunPreCompact(trigger contracts.CompactionTrigger) (halted bool)
	// RunPostCompact fires PostCompact after a completed episode.
	RunPostCompact(result contracts.CompactionResult)
}

// NoopHookRunner never halts and does nothing on PostCompact — the
// default when no hooks engine is wired.
type NoopHookRunner struct{}

func (NoopHookRunner) RunPreCompact(contracts.CompactionTrigger) bool { return false }
func (NoopHookRunner) RunPostCompact(contracts.CompactionResult)      {}
