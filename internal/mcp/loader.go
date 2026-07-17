package mcp

import "encoding/json"

// LoadServersJSON parses a Claude-Code-compatible mcp.json document: either
// `{"mcpServers": {"<name>": {...}}}` or a bare `{"<name>": {...}}` map
// (§1's "reads both its own TOML and Claude Code's .mcp.json").
func LoadServersJSON(data []byte) (map[string]ServerConfig, error) {
	var envelope struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}

	raw := envelope.MCPServers
	if raw == nil {
		// Bare map form: no "mcpServers" wrapper key.
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
	}

	table := make(map[string]map[string]any, len(raw))
	for name, msg := range raw {
		var m map[string]any
		if err := json.Unmarshal(msg, &m); err != nil {
			return nil, err
		}
		table[name] = m
	}
	return ParseServers(table)
}
