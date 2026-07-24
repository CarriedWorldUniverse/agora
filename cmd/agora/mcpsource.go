package main

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/mcp"
)

// buildMCPSource loads the Claude-Code-compatible .mcp.json from workingDir,
// interpolates the instance identity into each server stanza (§1
// {identity}), and returns a live mcp.Source folding those servers' tools
// into the turn surface. Returns nil (no MCP) when the file is absent or
// empty — a broken file surfaces via the /mcp command's own loader, not
// here (a bad config must not block session/thread construction).
func buildMCPSource(workingDir string) *mcp.Source {
	data, err := os.ReadFile(filepath.Join(workingDir, defaultMCPConfigPath))
	if err != nil {
		return nil
	}
	cfgs, err := mcp.LoadServersJSON(data)
	if err != nil || len(cfgs) == 0 {
		return nil
	}
	cfgs = mcp.InterpolateIdentity(cfgs, loadInstanceIdentity())
	ordered := make([]mcp.ServerConfig, 0, len(cfgs))
	for _, name := range mcp.SortedNames(cfgs) {
		ordered = append(ordered, cfgs[name])
	}
	return mcp.NewSource(ordered)
}

// loadInstanceIdentity resolves this agora instance's acting identity for
// {identity} interpolation and (later) attribution: ~/.agora/identity.json
// if present, else a local operator identity derived from the OS user.
// This is the v1 identity source — a keyring/herald-backed provider
// (agora-spec-remote §identity sources) is a later unit; the shape is the
// spec's contracts.Identity so that swap is drop-in.
func loadInstanceIdentity() contracts.Identity {
	fallback := contracts.Identity{
		ID:     osUsername(),
		Kind:   contracts.IdentityOperator,
		Source: "local",
	}
	data, err := os.ReadFile(filepath.Join(userHomeOrDot(), ".agora", "identity.json"))
	if err != nil {
		return fallback
	}
	var id contracts.Identity
	if err := json.Unmarshal(data, &id); err != nil || id.ID == "" {
		return fallback
	}
	if id.Kind == "" {
		id.Kind = contracts.IdentityOperator
	}
	if id.Source == "" {
		id.Source = "local"
	}
	return id
}

func osUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if n := os.Getenv("USER"); n != "" {
		return n
	}
	return "operator"
}
