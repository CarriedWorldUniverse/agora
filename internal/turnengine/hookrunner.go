package turnengine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/CarriedWorldUniverse/agora/internal/hooks"
)

// HookRunner wires the fully-built internal/hooks lifecycle engine (config
// parsing, matcher/discovery/trust, aggregation) to the live turn path: it
// owns the discovered hooks.Registry, the trust state read at construction,
// and the hooks.Dispatcher that actually spawns handler processes (this
// package's job per internal/hooks/doc.go: "It does NOT execute shell
// processes itself... this package exposes the RunFunc seam so the daemon
// can plug in a real fork/exec").
//
// A nil *HookRunner means hooks are disabled. Every Fire* method is
// nil-receiver-safe (returns the zero Aggregate — no block, no context, no
// decision), and every call site in this package checks m.hookRunner != nil
// before bothering to build a stdin payload at all — so an operator with no
// hooks.json pays no cost and sees no behavior change (spec build note
// under docs/spec/agora-spec-hooks.md §0: "a missing/empty config = zero
// overhead"). See DiscoverHooks for construction and DEVIATIONS.md for the
// scope this unit deliberately left out (managed/plugin layers, ${KEY}
// beyond simple env substitution, permission_mode is a report-only stub).
type HookRunner struct {
	registry    *hooks.Registry
	state       map[string]hooks.HandlerState
	bypassTrust bool
	dispatcher  *hooks.Dispatcher
	cwd         string

	// permissionMode is what hooks are TOLD the session's approval posture
	// is (spec §3: report-only — hooks never configure via this field).
	// Set by the Manager via setPermissionMode once its policy is resolved;
	// DiscoverHooks runs before any Manager exists, so the zero value ""
	// means "no Manager has claimed this runner yet" and reports the
	// engine's own default posture rather than a misleading preset name.
	permissionMode string

	asyncResults chan hooks.AsyncResult
}

// setPermissionMode records the approval posture hooks should be told
// about. Called by NewManager once opts have resolved m.policy.
func (hr *HookRunner) setPermissionMode(mode string) {
	if hr == nil {
		return
	}
	hr.permissionMode = mode
}

// reportedPermissionMode is the value written into every event's stdin.
func (hr *HookRunner) reportedPermissionMode() string {
	if hr == nil || hr.permissionMode == "" {
		return permissionModeName(defaultPolicy())
	}
	return hr.permissionMode
}

// hookStateEntry is this package's OWN on-disk shape for the trust/enable
// state spec §4.4 says belongs to "the User(+session) layer" — the main
// agora TOML config (with its `[hooks].state` sub-table) doesn't exist yet
// (no central TOML config loader in this codebase — see DEVIATIONS.md), so
// this unit persists the same information as a small JSON sidecar at
// ~/.agora/hooks-state.json instead, keyed by hooks.RegisteredHandler.
// PositionalKey() exactly as the spec's positional-key scheme requires.
// internal/hooks.HandlerState itself carries no json tags (it's an
// already-merged, in-memory-only shape per its own doc comment), so this is
// a deliberately separate wire type, converted 1:1 on load.
type hookStateEntry struct {
	Enabled     bool   `json:"enabled"`
	TrustedHash string `json:"trustedHash"`
}

// DiscoverHooks loads hooks.json for cwd's project layer and the operator's
// user layer, resolves the on-disk trust/enable state, and builds a
// HookRunner over a real shell-exec RunFunc (spec §3 "Invocation"). Returns
// (nil, warnings) when neither layer has a hooks.json to load — the
// zero-overhead, no-behavior-change default for an operator who has never
// written one.
//
// Layer order matches spec §4.1 exactly: managed (reserved, not built —
// this unit has nowhere for a broker to push policy hooks yet) and plugin
// (reserved, no plugin source discovery exists) are skipped; user then
// project, lowest-precedence-first, each loaded at most once.
func DiscoverHooks(cwd string) (*HookRunner, []string) {
	var warnings []string
	var reg hooks.Registry
	loaded := false

	userDir := hooksUserDir()
	load := func(layer hooks.Layer, dir string) {
		path := filepath.Join(dir, "hooks.json")
		b, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings, fmt.Sprintf("hooks: read %s: %v", path, err))
			}
			return
		}
		var cfg hooks.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			warnings = append(warnings, fmt.Sprintf("hooks: parse %s: %v", path, err))
			return
		}
		expandConfigEnv(&cfg)
		reg.Load(hooks.Source{Layer: layer, Path: dir}, cfg)
		loaded = true
	}
	load(hooks.LayerUser, userDir)
	load(hooks.LayerProject, filepath.Join(cwd, ".agora"))

	state, stateWarnings := loadHookState(filepath.Join(userDir, "hooks-state.json"))
	warnings = append(warnings, stateWarnings...)
	// Loading the state file (even an empty/absent one) does not itself
	// count as "an operator wrote a hooks.json" — only a discovered
	// hooks.json flips `loaded`, matching the doc comment's "neither layer
	// has a hooks.json" zero-overhead contract.
	if !loaded {
		return nil, warnings
	}

	// NEX-825: tell the operator what was discovered but WITHHELD. Trust is
	// fail-closed (ResolveTrust refuses anything with no recorded hash), and
	// nothing writes hooks-state.json, so before this every hook silently
	// never ran and there was no way to learn that — a configured hook that
	// does nothing, with no signal, is worse than no hook feature at all.
	// Same shape as the MCP trust gate's report: name what was withheld and
	// the exact entry to add.
	warnings = append(warnings, untrustedHookReport(&reg, state, userDir)...)

	asyncResults := make(chan hooks.AsyncResult, 16)
	hr := &HookRunner{
		registry: &reg,
		state:    state,
		cwd:      cwd,
		dispatcher: &hooks.Dispatcher{
			Run:          shellRunFunc,
			Clock:        hooks.RealClock{},
			AsyncResults: asyncResults,
		},
		asyncResults: asyncResults,
	}
	// Best-effort async audit: drain completions so the dispatcher's
	// completing goroutines never block on a full channel (§1.4: async is
	// fire-and-forget by design — see Dispatcher.AsyncResults' doc comment).
	// A real async-run-history UI is a later unit; this just keeps a
	// failure/timeout from vanishing silently.
	go func() {
		for res := range hr.asyncResults {
			if res.Result.Err != nil || res.TimedOut || (res.Result.ExitCode != 0 && res.Result.ExitCode != 2) {
				fmt.Fprintf(os.Stderr, "turnengine: hooks: async %s handler %s finished non-clean: exit=%d timeout=%v err=%v\n",
					res.Event, res.Handler.PositionalKey(), res.Result.ExitCode, res.TimedOut, res.Result.Err)
			}
		}
	}()
	return hr, warnings
}

// hooksUserDir is the User layer's dot-dir (spec §4.2: "~/.agora/hooks.json
// + [hooks] in user config"). Falls back to "." if the home directory can't
// be resolved — matching this package's existing best-effort posture toward
// optional, non-load-bearing filesystem lookups.
func hooksUserDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".agora")
}

// loadHookState reads the trust/enable sidecar (see hookStateEntry's doc
// comment). A missing file is not a warning (the common, unconfigured
// case): every discovered handler simply resolves Untrusted (fail-closed,
// per trust.go's ResolveTrust) until the operator trusts it.
func loadHookState(path string) (map[string]hooks.HandlerState, []string) {
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, []string{fmt.Sprintf("hooks: read %s: %v", path, err)}
		}
		return nil, nil
	}
	var raw map[string]hookStateEntry
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, []string{fmt.Sprintf("hooks: parse %s: %v", path, err)}
	}
	out := make(map[string]hooks.HandlerState, len(raw))
	for k, v := range raw {
		out[k] = hooks.HandlerState{Enabled: v.Enabled, TrustedHash: v.TrustedHash}
	}
	return out, nil
}

// expandConfigEnv applies spec §3's "${KEY} substitution into the command
// string at discovery time" to every handler's Command/CommandWindows.
// os.Expand also honors bare $KEY (a strict superset of the spec's ${KEY}
// form) — a deliberate, narrow reading rather than hand-rolling a
// ${...}-only scanner for no behavioral gain (ground rule 7). Plugin env
// vars (PLUGIN_ROOT/CLAUDE_PLUGIN_ROOT/PLUGIN_DATA/CLAUDE_PLUGIN_DATA) are
// NOT set here — no plugin hook sources are discovered by this unit (see
// DiscoverHooks), so those keys simply expand to "" if referenced, exactly
// like any other unset env var.
func expandConfigEnv(cfg *hooks.Config) {
	for event, groups := range cfg.Hooks {
		for gi := range groups {
			for hi := range groups[gi].Hooks {
				h := &groups[gi].Hooks[hi]
				h.Command = os.Expand(h.Command, envLookup)
				h.CommandWindows = os.Expand(h.CommandWindows, envLookup)
			}
		}
		cfg.Hooks[event] = groups
	}
}

func envLookup(key string) string {
	v, _ := os.LookupEnv(key)
	return v
}

// shellRunFunc is the production hooks.RunFunc: spec §3 "Invocation" —
// unix `$SHELL -lc <command>` (fallback /bin/sh), windows `%COMSPEC% /C`
// (fallback cmd.exe); stdin written then closed; stdout/stderr captured;
// "Child killed on drop" is exec.CommandContext's documented behavior on
// ctx cancellation (the dispatcher's runWithTimeout cancels runCtx on
// timeout — see dispatch.go).
func shellRunFunc(ctx context.Context, rh hooks.ResolvedHandler, event hooks.EventName, stdin []byte) hooks.RunResult {
	command := rh.Handler.EffectiveCommand(runtime.GOOS)

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = "cmd.exe"
		}
		cmd = exec.CommandContext(ctx, comspec, "/C", command)
	} else {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		cmd = exec.CommandContext(ctx, sh, "-lc", command)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = os.Environ() // "Env = inherited + per-handler env" (per-handler env: none defined by this unit's Config shape)

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return hooks.RunResult{ExitCode: -1, Err: err}
	}
	if err := cmd.Start(); err != nil {
		return hooks.RunResult{ExitCode: -1, Err: err}
	}
	go func() {
		_, _ = stdinPipe.Write(stdin)
		_ = stdinPipe.Close()
	}()

	waitErr := cmd.Wait()
	if waitErr == nil {
		return hooks.RunResult{ExitCode: 0, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	}
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		return hooks.RunResult{ExitCode: exitErr.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	}
	// Spawn error, killed-by-context, or anything else that isn't a clean
	// exit code — an invocation-level failure (dispatch.go's RunResult.Err
	// doc comment), which baseInterpret's exit-code switch treats as
	// Failed regardless of the sentinel ExitCode value.
	return hooks.RunResult{ExitCode: -1, Err: waitErr, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}

// untrustedHookReport lists handlers that will NOT run because no trusted
// hash is recorded for them, with the exact hooks-state.json entry that
// would allow each. Returns warning lines (the caller surfaces them the same
// way it surfaces parse warnings).
func untrustedHookReport(reg *hooks.Registry, state map[string]hooks.HandlerState, userDir string) []string {
	resolved := hooks.Resolve(reg.All(), state, false)
	var out []string
	for _, r := range resolved {
		if r.Runnable {
			continue
		}
		cmd := r.Handler.Command
		if cmd == "" {
			cmd = r.Handler.CommandWindows
		}
		out = append(out, fmt.Sprintf(
			"hooks: %s handler %q will NOT run (%s) — command: %s\n"+
				"       to allow it, add to %s:  %q: {\"enabled\": true, \"trusted_hash\": %q}",
			r.Event, r.PositionalKey(), r.TrustState, cmd,
			filepath.Join(userDir, "hooks-state.json"), r.PositionalKey(), r.ContentHash))
	}
	return out
}
