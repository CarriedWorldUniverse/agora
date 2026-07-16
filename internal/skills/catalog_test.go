package skills_test

import (
	"os"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/skills"
)

func entry(name, desc, path string) skills.CatalogEntry {
	return skills.CatalogEntry{Name: name, Description: desc, Path: path, Scope: skills.ScopeUser}
}

func TestBudget_PercentOfWindow(t *testing.T) {
	got := skills.Budget(100000)
	want := (100000 * 2 / 100) * skills.BytesPerToken
	if got != want {
		t.Errorf("Budget(100000) = %d, want %d", got, want)
	}
}

func TestBudget_MinOneToken(t *testing.T) {
	got := skills.Budget(10) // 2% of 10 = 0 tokens -> floors to 1
	if got != skills.BytesPerToken {
		t.Errorf("Budget(10) = %d, want %d (min 1 token)", got, skills.BytesPerToken)
	}
}

func TestBudget_UnknownWindowFallsBackTo8000(t *testing.T) {
	if got := skills.Budget(0); got != skills.FallbackBudgetChars {
		t.Errorf("Budget(0) = %d, want %d", got, skills.FallbackBudgetChars)
	}
	if got := skills.Budget(-1); got != skills.FallbackBudgetChars {
		t.Errorf("Budget(-1) = %d, want %d", got, skills.FallbackBudgetChars)
	}
}

// Case (a): everything fits at full size — no truncation, no warnings.
func TestRenderCatalog_CaseA_AllFit(t *testing.T) {
	entries := []skills.CatalogEntry{
		entry("alpha", "A short description.", "/skills/alpha/SKILL.md"),
		entry("beta", "Another short one.", "/skills/beta/SKILL.md"),
	}
	cat := skills.RenderCatalog(entries, 10000)
	if len(cat.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", cat.Warnings)
	}
	if !strings.Contains(cat.Text, "A short description.") {
		t.Errorf("full description missing from catalog: %s", cat.Text)
	}
	if !strings.Contains(cat.Text, "<skills_instructions>") || !strings.Contains(cat.Text, "</skills_instructions>") {
		t.Errorf("missing skills_instructions wrapper: %s", cat.Text)
	}
}

// Case (b): minimum lines fit, but full descriptions don't — round-robin
// description chars until budget exhausted, with a truncation warning.
func TestRenderCatalog_CaseB_RoundRobinDescriptions(t *testing.T) {
	longDesc := strings.Repeat("x", 500)
	entries := []skills.CatalogEntry{
		entry("alpha", longDesc, "/s/alpha/SKILL.md"),
		entry("beta", longDesc, "/s/beta/SKILL.md"),
	}
	// Minimal lines (no description) fit easily; full lines (2x500 chars)
	// do not, at a budget sized between the two.
	budget := 300
	cat := skills.RenderCatalog(entries, budget)
	if len(cat.Warnings) == 0 {
		t.Fatal("expected a truncation warning")
	}
	if !strings.Contains(cat.Text, "alpha") || !strings.Contains(cat.Text, "beta") {
		t.Errorf("expected both minimal lines present: %s", cat.Text)
	}
	if strings.Contains(cat.Text, longDesc) {
		t.Errorf("full description should not appear untruncated: %s", cat.Text)
	}
}

// Case (c): even minimum lines don't fit — omit lowest-priority entries
// in scope-priority order, with an omission warning.
func TestRenderCatalog_CaseC_OmitByScopePriority(t *testing.T) {
	entries := []skills.CatalogEntry{
		{Name: "sys-skill", Description: "d", Path: "/s/sys/SKILL.md", Scope: skills.ScopeSystem},
		{Name: "user-skill", Description: "d", Path: "/s/user/SKILL.md", Scope: skills.ScopeUser},
	}
	// Budget too small to fit even one minimal line comfortably alongside
	// the header + both entries, but enough for exactly one line.
	tiny := len("### Available skills\n") + len("- sys-skill: (file: /s/sys/SKILL.md)\n") + 5
	cat := skills.RenderCatalog(entries, tiny)
	if len(cat.Warnings) == 0 {
		t.Fatal("expected an omission warning")
	}
	if !strings.Contains(cat.Text, "sys-skill") {
		t.Errorf("expected higher-priority (System) entry kept: %s", cat.Text)
	}
	if strings.Contains(cat.Text, "user-skill") {
		t.Errorf("expected lower-priority (User) entry omitted: %s", cat.Text)
	}
}

func TestRenderCatalog_PerDescriptionCapTruncatesWithEllipsis(t *testing.T) {
	long := strings.Repeat("y", skills.PerDescriptionCapChars+50)
	all := []*skills.Skill{
		{Name: "alpha", Description: long, Path: "/s/alpha/SKILL.md", Scope: skills.ScopeUser},
	}
	entries := skills.EntriesFromSkills(all, nil)
	if len(entries[0].Description) > skills.PerDescriptionCapChars {
		t.Fatalf("description not capped: len=%d", len(entries[0].Description))
	}
	if !strings.HasSuffix(entries[0].Description, "...") {
		t.Errorf("expected ellipsis truncation, got %q", entries[0].Description[len(entries[0].Description)-10:])
	}
}

func TestEntriesFromSkills_FiltersDisabledAndHidden(t *testing.T) {
	no := false
	all := []*skills.Skill{
		{Name: "visible", Description: "d", Path: "/s/visible/SKILL.md"},
		{Name: "disabled", Description: "d", Path: "/s/disabled/SKILL.md"},
		{Name: "hidden", Description: "d", Path: "/s/hidden/SKILL.md", Sidecar: skills.Sidecar{}},
	}
	all[2].Sidecar.Policy.AllowImplicitInvocation = &no

	entries := skills.EntriesFromSkills(all, map[string]bool{"/s/disabled/SKILL.md": true})
	if len(entries) != 1 || entries[0].Name != "visible" {
		t.Fatalf("got %+v, want only 'visible'", entries)
	}
}

func TestRenderCatalog_AliasTableWhenPathsCauseOmission(t *testing.T) {
	longRoot := "/very/long/absolute/path/to/a/deeply/nested/skills/store/that/eats/budget"
	entries := []skills.CatalogEntry{
		{Name: "alpha", Description: "d1", Path: longRoot + "/alpha/SKILL.md", Scope: skills.ScopeUser, RootPath: longRoot},
		{Name: "beta", Description: "d2", Path: longRoot + "/beta/SKILL.md", Scope: skills.ScopeUser, RootPath: longRoot},
		{Name: "gamma", Description: "d3", Path: longRoot + "/gamma/SKILL.md", Scope: skills.ScopeUser, RootPath: longRoot},
	}
	// Budget picked so that minimal lines with FULL absolute paths for
	// all 3 don't fit, but minimal lines with aliased relative paths do.
	absMinimalLen := 0
	for _, e := range entries {
		absMinimalLen += len("- " + e.Name + ": (file: " + e.Path + ")\n")
	}
	budget := absMinimalLen - 40 // shave enough to force the switch
	cat := skills.RenderCatalog(entries, budget)
	if !strings.Contains(cat.Text, "### Skill roots") {
		t.Fatalf("expected alias table when it lets more skills fit:\n%s", cat.Text)
	}
	if !strings.Contains(cat.Text, "alpha") || !strings.Contains(cat.Text, "beta") || !strings.Contains(cat.Text, "gamma") {
		t.Errorf("expected all three entries present via aliasing:\n%s", cat.Text)
	}
}

func TestRenderInvocation_CapsAndWraps(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "invoked skill")
	sk, err := skills.ParseSkillMD(mustRead(t, dir+"/SKILL.md"), "d")
	if err != nil {
		t.Fatal(err)
	}
	sk.Path = dir + "/SKILL.md"
	sk.Dir = dir

	frag, warnings, err := skills.RenderInvocation(sk)
	if err != nil {
		t.Fatalf("RenderInvocation: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings for small file: %v", warnings)
	}
	if !strings.Contains(frag, "<skill>") || !strings.Contains(frag, "</skill>") {
		t.Errorf("missing <skill> wrapper: %s", frag)
	}
	if !strings.Contains(frag, "<name>d</name>") {
		t.Errorf("missing name element: %s", frag)
	}
}

func TestRenderInvocation_TruncatesAt8000Bytes(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("z", 9000)
	writeSkill(t, dir, "d")
	// overwrite with a body that pushes the file past 8000 bytes
	content := "---\ndescription: d\n---\n" + big
	if err := os.WriteFile(dir+"/SKILL.md", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	sk := &skills.Skill{Name: "big", Path: dir + "/SKILL.md", Dir: dir}
	frag, warnings, err := skills.RenderInvocation(sk)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Fatal("expected a truncation warning")
	}
	if len(frag) > skills.InvocationBodyCapBytes+500 {
		t.Errorf("frag suspiciously large: %d bytes", len(frag))
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
