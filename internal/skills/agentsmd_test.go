package skills_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/skills"
)

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverAGENTSMD_RootToCwdOrder(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	mid := filepath.Join(root, "pkg")
	cwd := filepath.Join(mid, "sub")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "AGENTS.md"), "root doc")
	mustWriteFile(t, filepath.Join(mid, "AGENTS.md"), "mid doc")
	mustWriteFile(t, filepath.Join(cwd, "AGENTS.md"), "leaf doc")

	docs := skills.DiscoverAGENTSMD(cwd, nil, nil, skills.DefaultAGENTSBudgetBytes)
	if docs.ProjectRoot != root {
		t.Fatalf("ProjectRoot = %q, want %q", docs.ProjectRoot, root)
	}
	if len(docs.Docs) != 3 {
		t.Fatalf("got %d docs, want 3: %+v", len(docs.Docs), docs.Docs)
	}
	want := []string{"root doc", "mid doc", "leaf doc"}
	for i, d := range docs.Docs {
		if d.Content != want[i] {
			t.Errorf("Docs[%d].Content = %q, want %q (root->cwd order)", i, d.Content, want[i])
		}
	}
}

func TestDiscoverAGENTSMD_OverridePrecedence(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "AGENTS.md"), "plain")
	mustWriteFile(t, filepath.Join(root, "AGENTS.override.md"), "override wins")

	docs := skills.DiscoverAGENTSMD(root, nil, nil, skills.DefaultAGENTSBudgetBytes)
	if len(docs.Docs) != 1 {
		t.Fatalf("got %d docs, want 1: %+v", len(docs.Docs), docs.Docs)
	}
	if docs.Docs[0].Content != "override wins" {
		t.Errorf("Content = %q, want override to win", docs.Docs[0].Content)
	}
	if docs.Docs[0].File != "AGENTS.override.md" {
		t.Errorf("File = %q, want AGENTS.override.md", docs.Docs[0].File)
	}
}

func TestDiscoverAGENTSMD_CLAUDEFallback(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "CLAUDE.md"), "claude compat doc")

	docs := skills.DiscoverAGENTSMD(root, nil, nil, skills.DefaultAGENTSBudgetBytes)
	if len(docs.Docs) != 1 || docs.Docs[0].Content != "claude compat doc" {
		t.Fatalf("got %+v, want CLAUDE.md fallback picked up", docs.Docs)
	}
	if docs.Docs[0].File != "CLAUDE.md" {
		t.Errorf("File = %q, want CLAUDE.md", docs.Docs[0].File)
	}
}

func TestDiscoverAGENTSMD_EmptyFilesSkipped(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "AGENTS.md"), "   \n  ")

	docs := skills.DiscoverAGENTSMD(root, nil, nil, skills.DefaultAGENTSBudgetBytes)
	if len(docs.Docs) != 0 {
		t.Fatalf("got %d docs, want 0 (empty file skipped): %+v", len(docs.Docs), docs.Docs)
	}
}

func TestDiscoverAGENTSMD_BudgetTruncatesLastDoc(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	mid := filepath.Join(root, "pkg")
	if err := os.MkdirAll(mid, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "AGENTS.md"), strings.Repeat("a", 10))
	mustWriteFile(t, filepath.Join(mid, "AGENTS.md"), strings.Repeat("b", 10))

	docs := skills.DiscoverAGENTSMD(mid, nil, nil, 15)
	if len(docs.Docs) != 2 {
		t.Fatalf("got %d docs, want 2 (first full, second truncated): %+v", len(docs.Docs), docs.Docs)
	}
	if docs.Docs[0].Content != strings.Repeat("a", 10) {
		t.Errorf("first doc truncated unexpectedly: %q", docs.Docs[0].Content)
	}
	if len(docs.Docs[1].Content) != 5 {
		t.Errorf("second doc len = %d, want 5 (truncated to remaining budget)", len(docs.Docs[1].Content))
	}
	if len(docs.Warnings) == 0 {
		t.Error("expected a truncation warning")
	}
}

func TestDiscoverAGENTSMD_ZeroBudgetDisables(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, "AGENTS.md"), "should not load")

	docs := skills.DiscoverAGENTSMD(root, nil, nil, 0)
	if len(docs.Docs) != 0 {
		t.Fatalf("got %d docs, want 0 (budget=0 disables)", len(docs.Docs))
	}
}

func TestRenderAGENTSFragment_Wrapping(t *testing.T) {
	docs := &skills.AgentsDocs{
		Docs: []skills.AgentsDoc{
			{Dir: "/proj", File: "AGENTS.md", Content: "project instructions"},
		},
	}
	frag := skills.RenderAGENTSFragment("/proj/sub", "user-level stuff", docs)
	if !strings.HasPrefix(frag, "# AGENTS.md instructions for /proj/sub\n\n<INSTRUCTIONS>\n") {
		t.Fatalf("unexpected prefix: %q", frag)
	}
	if !strings.HasSuffix(frag, "</INSTRUCTIONS>") {
		t.Fatalf("unexpected suffix: %q", frag)
	}
	if !strings.Contains(frag, "user-level stuff") {
		t.Errorf("missing user-level block: %s", frag)
	}
	if !strings.Contains(frag, "--- project-doc ---") {
		t.Errorf("missing one-time project-doc separator: %s", frag)
	}
	if strings.Count(frag, "--- project-doc ---") != 1 {
		t.Errorf("separator should appear exactly once: %s", frag)
	}
	if !strings.Contains(frag, "project instructions") {
		t.Errorf("missing project doc content: %s", frag)
	}
}

func TestRenderAGENTSFragment_NoUserLevelNoSeparator(t *testing.T) {
	docs := &skills.AgentsDocs{
		Docs: []skills.AgentsDoc{{Dir: "/proj", File: "AGENTS.md", Content: "project instructions"}},
	}
	frag := skills.RenderAGENTSFragment("/proj", "", docs)
	if strings.Contains(frag, "--- project-doc ---") {
		t.Errorf("no separator expected when there's no user-level block: %s", frag)
	}
}
