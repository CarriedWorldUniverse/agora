package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// permModel builds a model with /permissions wired to canned data.
func permModel(t *testing.T, grants []PermissionInfo, listErr error) (*Model, *fakeBackend, *[]string) {
	t.Helper()
	backend := newFakeBackend()
	m := testModel(backend)
	printed := &[]string{}
	m.cfg.Printer = capturingPrinter(printed)
	m.cfg.ListPermissions = func() ([]PermissionInfo, error) { return grants, listErr }
	return m, backend, printed
}

func runSlash(m *Model, text string) {
	m.composer.InsertText(text)
	m.press(tea.KeyMsg{Type: tea.KeyEnter})
}

func TestSlashPermissions_ListsSavedGrants(t *testing.T) {
	m, backend, printed := permModel(t, []PermissionInfo{
		{Kind: "exec", Scope: "prefix", Key: "go test", GrantedAt: "2026-07-24T00:00:00Z"},
		{Kind: "escalation", Scope: "host", Key: "api.github.com", Global: true},
	}, nil)

	runSlash(m, "/permissions")

	if len(*printed) != 1 {
		t.Fatalf("printed %d blocks; want 1", len(*printed))
	}
	out := (*printed)[0]
	for _, want := range []string{"go test", "api.github.com", "exec", "prefix"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got:\n%s", want, out)
		}
	}
	// A grant that applies everywhere is wider authority — it must be
	// visually distinguishable from a project-scoped one.
	if !strings.Contains(out, "all projects") {
		t.Errorf("global grant not marked as applying to all projects; got:\n%s", out)
	}
	if len(backend.Sent) != 0 {
		t.Fatalf("/permissions reached the model; it is a local command")
	}
}

func TestSlashPermissions_EmptyStateExplainsItself(t *testing.T) {
	m, _, printed := permModel(t, nil, nil)
	runSlash(m, "/permissions")
	if len(*printed) != 1 || !strings.Contains((*printed)[0], "none saved") {
		t.Fatalf("printed = %v; want an explicit empty state", *printed)
	}
}

// An unreadable permissions file must say so rather than reporting "none
// saved", which would tell the operator their grants are gone.
func TestSlashPermissions_ReadErrorIsNotReportedAsEmpty(t *testing.T) {
	m, _, printed := permModel(t, nil, errors.New("permissions.json is not valid JSON"))
	runSlash(m, "/permissions")
	out := (*printed)[0]
	if strings.Contains(out, "none saved") {
		t.Fatalf("a read error was reported as an empty list; got:\n%s", out)
	}
	if !strings.Contains(out, "not valid JSON") {
		t.Fatalf("the underlying error was not surfaced; got:\n%s", out)
	}
}

func TestSlashPermissions_NotWiredDegradesCleanly(t *testing.T) {
	backend := newFakeBackend()
	m := testModel(backend)
	printed := &[]string{}
	m.cfg.Printer = capturingPrinter(printed)
	m.cfg.ListPermissions = nil

	runSlash(m, "/permissions")
	if len(*printed) != 1 || !strings.Contains((*printed)[0], "not available") {
		t.Fatalf("printed = %v; want a not-available message, not a panic", *printed)
	}
}

func TestSlashPermissions_Revoke(t *testing.T) {
	m, _, printed := permModel(t, nil, nil)
	var gotKind, gotScope, gotKey string
	m.cfg.RevokePermission = func(kind, scope, key string) (bool, error) {
		gotKind, gotScope, gotKey = kind, scope, key
		return true, nil
	}

	runSlash(m, "/permissions revoke exec prefix go test ./...")

	if gotKind != "exec" || gotScope != "prefix" {
		t.Errorf("revoke got kind=%q scope=%q; want exec/prefix", gotKind, gotScope)
	}
	// The key may contain spaces — a command prefix is not one token.
	if gotKey != "go test ./..." {
		t.Errorf("revoke got key=%q; want the full multi-word key %q", gotKey, "go test ./...")
	}
	out := (*printed)[0]
	if !strings.Contains(out, "revoked") {
		t.Errorf("no confirmation; got:\n%s", out)
	}
	// The store deliberately keeps the grant live for the running session;
	// the message must not imply otherwise.
	if !strings.Contains(out, "next session") {
		t.Errorf("output does not say when the revoke takes effect; got:\n%s", out)
	}
}

func TestSlashPermissions_RevokeNoMatchSaysSo(t *testing.T) {
	m, _, printed := permModel(t, nil, nil)
	m.cfg.RevokePermission = func(string, string, string) (bool, error) { return false, nil }

	runSlash(m, "/permissions revoke exec prefix nothing")
	if !strings.Contains((*printed)[0], "no saved grant matches") {
		t.Fatalf("printed = %v; want a no-match message", *printed)
	}
}

func TestSlashPermissions_RevokeBadUsage(t *testing.T) {
	for _, text := range []string{
		"/permissions revoke",
		"/permissions revoke exec",
		"/permissions revoke exec prefix",
		"/permissions bogus",
	} {
		m, _, printed := permModel(t, nil, nil)
		m.cfg.RevokePermission = func(string, string, string) (bool, error) {
			t.Fatalf("%q should not have reached the revoke callback", text)
			return false, nil
		}
		runSlash(m, text)
		if len(*printed) != 1 || !strings.Contains((*printed)[0], "usage:") {
			t.Errorf("%q printed %v; want a usage message", text, *printed)
		}
	}
}

func TestSlashPermissions_RevokeNotWired(t *testing.T) {
	m, _, printed := permModel(t, nil, nil)
	m.cfg.RevokePermission = nil
	runSlash(m, "/permissions revoke exec prefix x")
	if !strings.Contains((*printed)[0], "not available") {
		t.Fatalf("printed = %v; want a not-available message", *printed)
	}
}

// /permissions must be discoverable.
func TestSlashPermissions_AppearsInHelp(t *testing.T) {
	m, _, printed := permModel(t, nil, nil)
	runSlash(m, "/help")
	if len(*printed) == 0 || !strings.Contains((*printed)[0], "permissions") {
		t.Fatalf("/help does not list /permissions; got:\n%v", *printed)
	}
}
