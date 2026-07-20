package main

import (
	"errors"
	"os"
	"strings"

	"github.com/CarriedWorldUniverse/agora/internal/mcp"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
)

// defaultMCPConfigPath is where the /mcp command looks for server config:
// a Claude-Code-compatible .mcp.json in the working directory (§1's JSON
// path — agora's own TOML config file is a deferred unit; the loader's
// ParseServerConfig is already format-agnostic for when it lands).
const defaultMCPConfigPath = ".mcp.json"

// listMCPServers feeds the TUI's /mcp command from the default path.
func listMCPServers() ([]tui.ServerInfo, error) {
	return listMCPServersFrom(defaultMCPConfigPath)
}

// listMCPServersFrom reads Claude-Code-compatible mcp.json (either the
// {"mcpServers": {...}} envelope or a bare {"<name>": {...}} map) via the
// internal/mcp loader and adapts it to tui.ServerInfo, keeping the tui
// package itself free of an mcp dependency. A missing file is an empty
// list, not an error; malformed JSON / invalid server entries ARE errors
// (the operator should hear their config is broken, not see "none
// configured"). Deterministic order via mcp.SortedNames (ground rule 3).
func listMCPServersFrom(path string) ([]tui.ServerInfo, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfgs, err := mcp.LoadServersJSON(data)
	if err != nil {
		return nil, err
	}
	out := make([]tui.ServerInfo, 0, len(cfgs))
	for _, name := range mcp.SortedNames(cfgs) {
		c := cfgs[name]
		detail := c.Command
		if len(c.Args) > 0 {
			detail += " " + strings.Join(c.Args, " ")
		}
		if c.Transport == mcp.TransportHTTP {
			detail = c.URL
		}
		out = append(out, tui.ServerInfo{
			Name:      c.Name,
			Transport: string(c.Transport),
			Detail:    detail,
			Enabled:   c.Enabled,
		})
	}
	return out, nil
}
