package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	tea "github.com/charmbracelet/bubbletea"
)

// The tests in this file pin ONE property, for every slash handler that
// touches the disk, the thread store, or a subprocess: the work happens
// when the returned tea.Cmd runs, NOT while Update is still on the stack.
//
// This is the regression the package could not previously catch. The old
// tests exercised the render helpers (renderMCPReport, …) directly and
// never drove a handler, so the fact that every handler blocked Update was
// invisible to them (agora#138).
//
// The discriminator is cfg.Printer. A handler that did its work eagerly
// called Printer before returning; a handler that defers calls it only from
// inside the Cmd. Asserting "nothing printed yet" right after the handler
// returns therefore catches an eager handler no matter WHICH blocking call
// it makes.

// probe records whether a seam was consulted and when.
type probe struct{ called bool }

func (p *probe) mark() { p.called = true }

// recordingPrinter captures printed text like capturingPrinter, but returns
// a NON-nil no-op Cmd. That distinction matters here: with a nil-returning
// printer an eager handler fails these tests for the incidental reason that
// it hands back a nil Cmd, which masks the assertion we actually care about
// ("it printed from inside Update"). Returning a real Cmd makes an eager
// handler fail on the substantive check instead.
func recordingPrinter(out *[]string) Printer {
	return func(text string) tea.Cmd {
		*out = append(*out, text)
		return func() tea.Msg { return nil }
	}
}

// asyncModel builds a Model whose Printer records into printed.
func asyncModel(t *testing.T, printed *[]string, mutate func(*Config)) *Model {
	t.Helper()
	cfg := Config{
		AgentID: "agora",
		Theme:   PlainTheme(),
		Printer: recordingPrinter(printed),
		Now:     func() time.Time { return time.Unix(0, 0).UTC() },
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewModel(cfg)
}

// runDeferred asserts the handler deferred, then runs the Cmd and asserts
// the work actually happened.
func runDeferred(t *testing.T, name string, cmd tea.Cmd, printed *[]string, worked *probe) {
	t.Helper()
	if cmd == nil {
		t.Fatalf("%s: returned a nil tea.Cmd; nothing would ever run", name)
	}
	if worked != nil && worked.called {
		t.Errorf("%s: did its work inside Update — this blocks the render loop and freezes the terminal (agora#138); move it into the returned tea.Cmd", name)
	}
	if len(*printed) != 0 {
		t.Errorf("%s: printed %d block(s) from inside Update; want 0 until the Cmd runs", name, len(*printed))
	}
	cmd()
	if worked != nil && !worked.called {
		t.Errorf("%s: the Cmd ran but never consulted its data source", name)
	}
	if len(*printed) == 0 {
		t.Errorf("%s: the Cmd ran but printed nothing", name)
	}
}

func TestSlashMCP_ReadsConfigInCmdNotUpdate(t *testing.T) {
	var printed []string
	var seen probe
	m := asyncModel(t, &printed, func(c *Config) {
		c.ListServers = func() ([]ServerInfo, error) {
			seen.mark()
			return []ServerInfo{{Name: "fs", Transport: "stdio", Detail: "npx fs", Enabled: true}}, nil
		}
	})
	runDeferred(t, "/mcp", runSlashMCP(m, ""), &printed, &seen)
}

func TestSlashHooks_DiscoversInCmdNotUpdate(t *testing.T) {
	var printed []string
	var seen probe
	m := asyncModel(t, &printed, func(c *Config) {
		c.ListHooks = func() ([]HookInfo, error) {
			seen.mark()
			return []HookInfo{{Event: "PreToolUse", Key: "k", Command: "echo", Trust: "Trusted"}}, nil
		}
	})
	runDeferred(t, "/hooks", runSlashHooks(m, ""), &printed, &seen)
}

func TestSlashPermissions_ReadsStoreInCmdNotUpdate(t *testing.T) {
	var printed []string
	var seen probe
	m := asyncModel(t, &printed, func(c *Config) {
		c.ListPermissions = func() ([]PermissionInfo, error) {
			seen.mark()
			return []PermissionInfo{{Kind: "exec", Scope: "prefix", Key: "git"}}, nil
		}
	})
	runDeferred(t, "/permissions", runSlashPermissions(m, ""), &printed, &seen)
}

func TestSlashPermissionsRevoke_WritesStoreInCmdNotUpdate(t *testing.T) {
	var printed []string
	var seen probe
	m := asyncModel(t, &printed, func(c *Config) {
		c.ListPermissions = func() ([]PermissionInfo, error) { return nil, nil }
		c.RevokePermission = func(kind, scope, key string) (bool, error) {
			seen.mark()
			return true, nil
		}
	})
	// The revoke MUTATES permissions.json — the most important of these to
	// keep off the Update goroutine, since a slow write freezes the UI while
	// the operator is mid-command.
	runDeferred(t, "/permissions revoke", runSlashPermissions(m, "revoke exec prefix git"), &printed, &seen)
}

func TestSlashDiff_SpawnsGitInCmdNotUpdate(t *testing.T) {
	var printed []string
	// /diff has no injectable seam — it spawns real git — so Printer alone
	// is the discriminator. cwd is a non-repo temp dir, so the Cmd resolves
	// quickly and deterministically to "not a git repository".
	t.Chdir(t.TempDir())
	m := asyncModel(t, &printed, nil)
	runDeferred(t, "/diff", runSlashDiff(m, ""), &printed, nil)
}

func TestSlashDiff_RejectsBadArgsWithoutDeferring(t *testing.T) {
	var printed []string
	m := asyncModel(t, &printed, nil)
	// Argument validation is a regexp match and MUST stay synchronous: a
	// rejected argument should never reach the exec path at all. This pins
	// the deliberate exception to the rule above.
	cmd := runSlashDiff(m, "; rm -rf /")
	if cmd != nil {
		cmd()
	}
	if len(printed) != 1 || printed[0] != "unsupported /diff argument" {
		t.Fatalf("printed = %v; want the rejection message", printed)
	}
}

func TestSlashInit_WritesFileInCmdNotUpdate(t *testing.T) {
	var printed []string
	dir := t.TempDir()
	t.Chdir(dir)
	m := asyncModel(t, &printed, nil)

	cmd := runSlashInit(m, "")
	if cmd == nil {
		t.Fatal("/init: returned a nil tea.Cmd")
	}
	// The strongest form of this assertion: the FILE must not exist yet.
	// This catches an eager handler directly, with no reliance on Printer.
	path := filepath.Join(dir, "AGENTS.md")
	if _, err := os.Stat(path); err == nil {
		t.Error("/init wrote AGENTS.md from inside Update; want the write deferred to the tea.Cmd (agora#138)")
	}
	cmd()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("/init: AGENTS.md not created after the Cmd ran: %v", err)
	}
}

// probingLister is a ThreadLister that records WHEN it was consulted.
// (resume_test.go's listerBackend records what it was asked; this one
// records whether it has been asked yet, which is the property here.)
type probingLister struct {
	*fakeBackend
	seen *probe
	err  error
}

func (l *probingLister) ThreadSummaries(string) ([]contracts.ThreadMeta, error) {
	l.seen.mark()
	if l.err != nil {
		return nil, l.err
	}
	return []contracts.ThreadMeta{{ThreadID: "t1", CreatedAt: time.Unix(0, 0).UTC(), WorkingDir: "/tmp"}}, nil
}

func TestResume_WalksStoreInCmdNotUpdate(t *testing.T) {
	var printed []string
	var seen probe
	m := asyncModel(t, &printed, func(c *Config) {
		c.Backend = &probingLister{fakeBackend: newFakeBackend(), seen: &seen}
	})
	cmd, handled := m.handleResumeCommand("/resume")
	if !handled {
		t.Fatal("/resume was not handled")
	}
	// ThreadSummaries scales with how many threads have ever been persisted,
	// so it degrades quietly over time — exactly the shape that should never
	// sit on the render loop.
	runDeferred(t, "/resume", cmd, &printed, &seen)
}

func TestResume_ErrorRoutesThroughStatusErrMsg(t *testing.T) {
	var printed []string
	var seen probe
	m := asyncModel(t, &printed, func(c *Config) {
		c.Backend = &probingLister{fakeBackend: newFakeBackend(), seen: &seen, err: os.ErrPermission}
	})
	cmd, handled := m.handleResumeCommand("/resume")
	if !handled || cmd == nil {
		t.Fatal("/resume was not handled")
	}
	// The failure path must NOT assign m.statusErr from the Cmd goroutine —
	// that races the render loop. It returns a statusErrMsg, and Update (the
	// sole owner of the Model) applies it.
	msg := cmd()
	got, ok := msg.(statusErrMsg)
	if !ok {
		t.Fatalf("cmd() = %T; want statusErrMsg", msg)
	}
	if m.statusErr != "" {
		t.Error("statusErr was assigned from the Cmd goroutine; want it set only by Update")
	}
	if _, _ = m.Update(got); m.statusErr == "" {
		t.Error("Update(statusErrMsg) did not set statusErr")
	}
}
