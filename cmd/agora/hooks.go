package main

import (
	"github.com/CarriedWorldUniverse/agora/internal/tui"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
)

// listHooks feeds the TUI's /hooks verb: the lifecycle hooks discovered for
// this working directory, each with its resolved trust state and the content
// hash that would enable it.
//
// Discovery is re-run here rather than plumbed out of the live engine, the
// same way listMCPServers re-reads .mcp.json — it is two file reads, and it
// keeps the verb honest about what is on disk NOW (an operator who has just
// edited hooks-state.json in another window sees the effect without
// restarting, and the edited-hook-loses-trust rule shows up immediately).
//
// Why the verb exists at all (NEX-825): hook trust is fail-closed, so a
// handler with no recorded hash never fires. DiscoverHooks reports that to
// stderr, but in TUI mode the alt-screen swallows it — leaving a configured
// hook that silently does nothing and no way to find out why.
func listHooks() ([]tui.HookInfo, error) {
	hr, _ := turnengine.DiscoverHooks(mustGetwd())
	discovered := hr.Discovered()
	out := make([]tui.HookInfo, 0, len(discovered))
	for _, d := range discovered {
		out = append(out, tui.HookInfo{
			Event:     d.Event,
			Key:       d.Key,
			Command:   d.Command,
			Matcher:   d.Matcher,
			Trust:     d.Trust,
			Runnable:  d.Runnable,
			Hash:      d.Hash,
			StatePath: d.StatePath,
		})
	}
	return out, nil
}
