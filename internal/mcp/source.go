package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
)

// Source adapts an mcp.Manager into the toolrunner.MCPSource the turn
// engine's Surface folds MCP tools through (Tools + Call). It lazily starts
// the configured servers on first use, aggregates their tools under
// mcp__<server>__<tool> names (naming.AssignNames), and routes a qualified
// call back to the owning server's raw tool via the client's ToolCaller.
//
// A dead or failed server degrades gracefully: it simply contributes no
// tools, and a Call to one of its (stale) names returns a clean IsError
// Result — never a panic or a turn-ending Go error (toolrunner.Surface's
// contract).
type Source struct {
	mgr  *Manager
	cfgs []ServerConfig

	once     sync.Once
	startErr error

	mu     sync.Mutex
	routes map[string]route // qualified name -> owning server + raw tool
}

type route struct {
	server  string
	rawTool string
}

var _ toolrunner.MCPSource = (*Source)(nil)

// NewSource builds a Source over the given resolved server configs (defaults
// applied, {identity} already interpolated). Startup is deferred to the
// first Tools/Call so constructing a Source never spawns a subprocess.
func NewSource(cfgs []ServerConfig) *Source {
	return &Source{
		mgr:    NewManager(StdioConnector{}),
		cfgs:   cfgs,
		routes: map[string]route{},
	}
}

// ensureStarted starts every enabled server exactly once. Startup failures
// of individual servers are non-fatal (they contribute no tools); only a
// total inability to start (StartAll error) propagates.
func (s *Source) ensureStarted(ctx context.Context) error {
	s.once.Do(func() {
		enabled := make([]ServerConfig, 0, len(s.cfgs))
		for _, c := range s.cfgs {
			if c.Enabled {
				enabled = append(enabled, c)
			}
		}
		if len(enabled) == 0 {
			return
		}
		startCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		_, _, err := s.mgr.StartAll(startCtx, enabled)
		s.startErr = err
	})
	return s.startErr
}

// Tools aggregates every ready server's tool list into qualified ToolSpecs
// and records the qualified->raw routing table for Call. EnabledTools/
// DisabledTools filters are applied per server.
func (s *Source) Tools(ctx context.Context) ([]contracts.ToolSpec, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return nil, err
	}

	// Collect (server, rawTool) identities + keep raw specs for description/schema.
	type rawSpec struct {
		server string
		spec   contracts.ToolSpec
	}
	var idents []ToolIdentity
	rawByKey := map[string]rawSpec{} // server+"\x00"+rawTool -> spec
	for _, cfg := range s.cfgs {
		if !cfg.Enabled {
			continue
		}
		client, ok := s.mgr.Client(cfg.Name)
		if !ok {
			continue // not ready (failed/cancelled) — no tools
		}
		specs, err := client.ListTools(ctx)
		if err != nil {
			continue // a listing failure isolates to this server
		}
		allow := toSet(cfg.EnabledTools)
		deny := toSet(cfg.DisabledTools)
		for _, sp := range specs {
			if len(allow) > 0 && !allow[sp.Name] {
				continue
			}
			if deny[sp.Name] {
				continue
			}
			idents = append(idents, ToolIdentity{Server: cfg.Name, Tool: sp.Name})
			rawByKey[cfg.Name+"\x00"+sp.Name] = rawSpec{server: cfg.Name, spec: sp}
		}
	}

	named := AssignNames(idents)
	out := make([]contracts.ToolSpec, 0, len(named))
	routes := make(map[string]route, len(named))
	for _, nt := range named {
		rs := rawByKey[nt.Server+"\x00"+nt.Tool]
		out = append(out, contracts.ToolSpec{
			Name:        nt.Name,
			Description: rs.spec.Description,
			InputSchema: rs.spec.InputSchema,
		})
		routes[nt.Name] = route{server: nt.Server, rawTool: nt.Tool}
	}
	s.mu.Lock()
	s.routes = routes
	s.mu.Unlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Call routes a qualified mcp__server__tool name to the owning server's
// ToolCaller. An unknown name or a server that isn't a ToolCaller returns a
// clean IsError Result (the model sees the failure and reacts) rather than
// a Go error.
func (s *Source) Call(ctx context.Context, name string, args json.RawMessage) (toolrunner.Result, error) {
	if err := s.ensureStarted(ctx); err != nil {
		return toolrunner.Result{}, err
	}
	s.mu.Lock()
	rt, ok := s.routes[name]
	s.mu.Unlock()
	if !ok {
		// Routes populate on Tools(); if Call somehow precedes it, refresh once.
		if _, err := s.Tools(ctx); err == nil {
			s.mu.Lock()
			rt, ok = s.routes[name]
			s.mu.Unlock()
		}
	}
	if !ok {
		return errResult(fmt.Sprintf("unknown MCP tool %q", name)), nil
	}
	client, ready := s.mgr.Client(rt.server)
	if !ready {
		return errResult(fmt.Sprintf("MCP server %q is not connected", rt.server)), nil
	}
	caller, ok := client.(ToolCaller)
	if !ok {
		return errResult(fmt.Sprintf("MCP server %q does not support tool calls", rt.server)), nil
	}
	text, isErr, err := caller.CallTool(ctx, rt.rawTool, args)
	if err != nil {
		return errResult(fmt.Sprintf("MCP call %q failed: %v", name, err)), nil
	}
	return toolrunner.Result{Content: text, IsError: isErr}, nil
}

// Close stops all servers (subprocess teardown). Best-effort.
func (s *Source) Close() {
	for _, cfg := range s.cfgs {
		s.mgr.Cancel(cfg.Name)
	}
}

func errResult(msg string) toolrunner.Result {
	return toolrunner.Result{Content: msg, IsError: true}
}

func toSet(xs []string) map[string]bool {
	if len(xs) == 0 {
		return nil
	}
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// InterpolateIdentity substitutes {identity} / {identity.<field>} across a
// server config's string values (Command, Args, Env values, Cwd, URL,
// HTTPHeaders values) from id — agora's §1 extension letting one global
// stanza serve every instance. EnvVars entries are host var NAMES, not
// interpolated. An unknown {identity.<x>} placeholder is left verbatim (a
// stanza may target a field a future identity kind adds).
func InterpolateIdentity(cfgs map[string]ServerConfig, id contracts.Identity) map[string]ServerConfig {
	rep := strings.NewReplacer(
		"{identity}", id.ID,
		"{identity.id}", id.ID,
		"{identity.fingerprint}", id.Fingerprint,
		"{identity.kind}", string(id.Kind),
		"{identity.display_name}", id.DisplayName,
	)
	sub := func(s string) string { return rep.Replace(s) }
	out := make(map[string]ServerConfig, len(cfgs))
	for name, c := range cfgs {
		c.Command = sub(c.Command)
		c.Cwd = sub(c.Cwd)
		c.URL = sub(c.URL)
		if len(c.Args) > 0 {
			args := make([]string, len(c.Args))
			for i, a := range c.Args {
				args[i] = sub(a)
			}
			c.Args = args
		}
		if len(c.Env) > 0 {
			env := make(map[string]string, len(c.Env))
			for k, v := range c.Env {
				env[k] = sub(v)
			}
			c.Env = env
		}
		if len(c.HTTPHeaders) > 0 {
			h := make(map[string]string, len(c.HTTPHeaders))
			for k, v := range c.HTTPHeaders {
				h[k] = sub(v)
			}
			c.HTTPHeaders = h
		}
		out[name] = c
	}
	return out
}
