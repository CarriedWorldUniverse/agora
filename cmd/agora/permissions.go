package main

import (
	"os"
	"path/filepath"

	"github.com/CarriedWorldUniverse/agora/internal/approval"
	"github.com/CarriedWorldUniverse/agora/internal/skills"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
)

// permissions.go adapts the durable scope store into the two callbacks the
// TUI's /permissions command needs, keeping internal/tui free of an
// approval dependency (the same split mcp.go uses for /mcp).
//
// These open the store fresh per call rather than sharing the engine's
// instance. That is deliberate: reading is a rare, interactive operation,
// and re-reading picks up grants written by another agora running in a
// different terminal — the shared-instance alternative would show a stale
// snapshot with no way to refresh it.

// permissionsPath is where durable approval grants live. Under the USER's
// home, never the project — see approval.OpenFileScopeStore's doc comment
// for why a project-layer permissions file would be a supply-chain hole.
func permissionsPath() string {
	return filepath.Join(userHomeOrDot(), ".agora", "permissions.json")
}

// permissionsProjectRoot resolves the bucket whose grants apply here, from
// the CLIENT's working directory.
//
// KNOWN LIMITATION (tracked separately): newTurnEngineManager buckets by
// FindProjectRoot(roots.WorkingDir), and in the daemon lane that comes from
// the THREAD's recorded meta.WorkingDir, which need not match the client
// process's cwd. Attaching a TUI in one directory to a daemon thread rooted
// in another therefore makes /permissions list — and revoke against — the
// client's project rather than the thread's.
//
// It is a display/management mismatch, not an authority one: this path only
// ever reads and deletes, so the worst case is showing the wrong bucket or a
// revoke that no-ops. It cannot grant anything anywhere. In the common lanes
// (in-process TUI, `agora pipe`) cwd IS the thread's working dir and the two
// agree exactly. Fixing it properly needs the attached thread's WorkingDir
// plumbed to the client, which is a separate unit of work.
func permissionsProjectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return skills.FindProjectRoot(wd, nil)
}

// listPermissions feeds the TUI's /permissions command.
func listPermissions() ([]tui.PermissionInfo, error) {
	store, warn := approval.OpenFileScopeStore(permissionsPath(), permissionsProjectRoot())
	if warn != nil {
		// A corrupt/unreadable file is worth surfacing HERE, unlike at
		// session start: the operator explicitly asked what is saved, and
		// answering "none" when the truth is "unreadable" would be a lie.
		return nil, warn
	}
	grants := store.Grants()
	out := make([]tui.PermissionInfo, 0, len(grants))
	for _, g := range grants {
		out = append(out, tui.PermissionInfo{
			Kind:      g.Kind,
			Scope:     g.Scope,
			Key:       g.Key,
			GrantedAt: g.GrantedAt,
			Global:    g.Global,
		})
	}
	return out, nil
}

// revokePermission removes a saved grant, reporting whether one matched.
func revokePermission(kind, scope, key string) (bool, error) {
	store, warn := approval.OpenFileScopeStore(permissionsPath(), permissionsProjectRoot())
	if warn != nil {
		return false, warn
	}
	return store.Revoke(kind, scope, key)
}
