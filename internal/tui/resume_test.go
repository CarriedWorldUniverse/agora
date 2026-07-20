package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// listerBackend is a fakeBackend that also implements ThreadLister.
type listerBackend struct {
	*fakeBackend
	metas []contracts.ThreadMeta
	gotWD []string
}

func (l *listerBackend) ThreadSummaries(wd string) ([]contracts.ThreadMeta, error) {
	l.gotWD = append(l.gotWD, wd)
	return l.metas, nil
}

func TestResumeCommand_ListsThreadsAndMarksCurrent(t *testing.T) {
	backend := &listerBackend{fakeBackend: newFakeBackend(), metas: []contracts.ThreadMeta{
		{ThreadID: "agora-abc123", CreatedAt: time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC), WorkingDir: "/home/x/agora"},
		{ThreadID: "other-def456", CreatedAt: time.Date(2026, 7, 19, 9, 0, 0, 0, time.UTC), WorkingDir: "/home/x/other"},
	}}
	m := NewModel(Config{Backend: backend, Theme: PlainTheme(), ThreadID: "agora-abc123",
		Now: func() time.Time { return time.Unix(0, 0).UTC() }, ModelRegistry: testRegistry()})
	var printed []string
	m.cfg.Printer = capturingPrinter(&printed)

	m.composer.SetValue("/resume")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if len(backend.Sent) != 0 {
		t.Fatalf("/resume sent %v to the model, want nothing", backend.Sent)
	}
	out := strings.Join(printed, "\n")
	if !strings.Contains(out, "* agora-abc123") {
		t.Fatalf("current thread not marked:\n%s", out)
	}
	if !strings.Contains(out, "other-def456") || !strings.Contains(out, "agora -thread <id>") {
		t.Fatalf("listing/hint incomplete:\n%s", out)
	}
	// plain /resume filters by the current wd; /resume all clears the filter
	m.composer.SetValue("/resume all")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if len(backend.gotWD) != 2 || backend.gotWD[0] == "" || backend.gotWD[1] != "" {
		t.Fatalf("wd filters = %v, want [<cwd>, \"\"]", backend.gotWD)
	}
}

func TestResumeCommand_UnavailableBackend(t *testing.T) {
	m := testModelWithRegistry(newFakeBackend(), testRegistry()) // plain fake: no ThreadLister
	m.composer.SetValue("/resume")
	if cmd := m.submitComposer(); cmd != nil {
		cmd()
	}
	if m.statusErr == "" || !strings.Contains(m.statusErr, "not available") {
		t.Fatalf("statusErr = %q, want a not-available message", m.statusErr)
	}
}
