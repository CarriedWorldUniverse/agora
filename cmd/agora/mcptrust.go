package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/agora/internal/mcp"
)

// Project-scoped MCP servers are UNTRUSTED until the operator says otherwise.
//
// Why this file exists (security review 2026-07-25): .mcp.json is read from
// the WORKING DIRECTORY — i.e. whatever repo agora was started in — and every
// stanza defaults to Enabled. Building the per-turn tool list calls
// Source.Tools -> ensureStarted -> exec.Command(cfg.Command, cfg.Args...), so
// merely opening agora in a repo that ships a .mcp.json and sending one
// message executed that repo's chosen command as the operator, BEFORE any
// approval gate (the spawn happens while ASSEMBLING the tool list, so
// beforeToolCall never sees it). Claude Code — whose config format this is —
// prompts before running project-scoped servers for exactly this reason.
//
// The gate is deliberately the same shape as the hooks engine's
// (internal/hooks/trust.go): a content hash over what actually executes,
// recorded in a USER-layer file that a repo cannot write. A stanza whose
// command/args/env change gets a new hash and falls back to untrusted, so an
// edit to a previously-trusted server cannot ride the old grant.
const mcpTrustFileName = "mcp-trust.json"

// mcpTrustFile is the on-disk shape: server name -> accepted content hash.
// Kept dead simple; it is edited by hand today (a /mcp trust verb is the
// follow-up, same gap the hooks trust sidecar has).
type mcpTrustFile struct {
	Trusted map[string]string `json:"trusted"`
}

// mcpServerFingerprint hashes exactly what makes a server dangerous: the
// command, its arguments, its env, and its cwd. The NAME is deliberately not
// hashed — renaming a stanza must not silently inherit trust, so the name is
// the lookup key and the hash is the check.
func mcpServerFingerprint(c mcp.ServerConfig) string {
	h := sha256.New()
	fmt.Fprintf(h, "cmd:%s\n", c.Command)
	for _, a := range c.Args {
		fmt.Fprintf(h, "arg:%s\n", a)
	}
	keys := make([]string, 0, len(c.Env))
	for k := range c.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "env:%s=%s\n", k, c.Env[k])
	}
	fmt.Fprintf(h, "cwd:%s\n", c.Cwd)
	fmt.Fprintf(h, "url:%s\n", c.URL)
	return hex.EncodeToString(h.Sum(nil))
}

// loadMCPTrust reads the user-layer trust file. A missing/broken file means
// "nothing is trusted" — fail closed, never fail open.
func loadMCPTrust(userDir string) map[string]string {
	data, err := os.ReadFile(filepath.Join(userDir, mcpTrustFileName))
	if err != nil {
		return nil
	}
	var f mcpTrustFile
	if err := json.Unmarshal(data, &f); err != nil {
		fmt.Fprintf(os.Stderr, "agora: %s is unreadable (%v) — treating every project MCP server as untrusted\n", mcpTrustFileName, err)
		return nil
	}
	return f.Trusted
}

// gateProjectMCPServers disables every server whose fingerprint is not
// recorded in the user-layer trust file, and tells the operator — by name,
// with the command that would have run and the exact hash to record — what
// was withheld. Returns the gated configs in the given order.
func gateProjectMCPServers(cfgs []mcp.ServerConfig, trusted map[string]string, userDir string) []mcp.ServerConfig {
	var withheld []string
	out := make([]mcp.ServerConfig, 0, len(cfgs))
	for _, c := range cfgs {
		if !c.Enabled {
			out = append(out, c)
			continue
		}
		fp := mcpServerFingerprint(c)
		if trusted[c.Name] == fp {
			out = append(out, c)
			continue
		}
		c.Enabled = false
		out = append(out, c)
		what := c.Command
		if what == "" {
			what = c.URL
		}
		if len(c.Args) > 0 {
			what += " " + strings.Join(c.Args, " ")
		}
		withheld = append(withheld, fmt.Sprintf("  %-20s %s\n      trust with: %q: %q", c.Name, what, c.Name, fp))
	}
	if len(withheld) > 0 {
		fmt.Fprintf(os.Stderr,
			"agora: %d project MCP server(s) in .mcp.json were NOT started — a repo cannot grant itself execution.\n%s\n"+
				"  To allow one, add its entry under \"trusted\" in %s and restart.\n",
			len(withheld), strings.Join(withheld, "\n"), filepath.Join(userDir, mcpTrustFileName))
	}
	return out
}
