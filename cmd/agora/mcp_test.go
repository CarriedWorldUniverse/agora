package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeMCPConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".mcp.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListMCPServersFrom_MissingFileIsEmpty(t *testing.T) {
	servers, err := listMCPServersFrom(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil || servers != nil {
		t.Fatalf("missing file: got (%v, %v); want (nil, nil)", servers, err)
	}
}

func TestListMCPServersFrom_EnvelopeForm(t *testing.T) {
	path := writeMCPConfig(t, `{"mcpServers": {
		"github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"]}
	}}`)
	servers, err := listMCPServersFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers; want 1", len(servers))
	}
	s := servers[0]
	if s.Name != "github" || s.Transport != "stdio" || s.Detail != "npx -y @modelcontextprotocol/server-github" || !s.Enabled {
		t.Errorf("got %+v", s)
	}
}

func TestListMCPServersFrom_BareMapHTTPAndDisabled(t *testing.T) {
	path := writeMCPConfig(t, `{
		"remote": {"url": "https://example.com/mcp"},
		"old-tool": {"command": "uvx", "args": ["old-tool"], "enabled": false}
	}`)
	servers, err := listMCPServersFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("got %d servers; want 2", len(servers))
	}
	// Deterministic (sorted) order: old-tool before remote.
	if servers[0].Name != "old-tool" || servers[1].Name != "remote" {
		t.Fatalf("order = %q, %q; want old-tool, remote", servers[0].Name, servers[1].Name)
	}
	if servers[0].Enabled || servers[0].Detail != "uvx old-tool" {
		t.Errorf("old-tool: got %+v", servers[0])
	}
	if servers[1].Transport != "streamable_http" || servers[1].Detail != "https://example.com/mcp" {
		t.Errorf("remote: got %+v", servers[1])
	}
}

func TestListMCPServersFrom_MalformedConfigIsAnError(t *testing.T) {
	for _, content := range []string{
		`{not json`,
		`{"mixed": {"command": "x", "url": "https://y"}}`,
		`{"notransport": {}}`,
	} {
		path := writeMCPConfig(t, content)
		if _, err := listMCPServersFrom(path); err == nil {
			t.Errorf("config %s: got nil error; want failure", content)
		}
	}
}
