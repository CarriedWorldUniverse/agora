package skills

// Regression tests for the U5 multi-model review gate (Sonnet + DeepSeek-v4-pro
// + security-validator). Each test encodes one confirmed finding and, where the
// finding is a spec deviation, cites the spec clause it enforces. White-box
// (package skills) so it can drive fitBody and the unexported scan path directly.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// F1 — case-(b) round-robin must charge the BYTE cost of each appended rune
// against the byte budget, not 1-per-rune. Spec §3.2: the budget is bytes
// (Budget returns tokens*4; case-a/min checks use len()). A CJK description
// (3 bytes/rune) previously overshot the budget ~3x.
func TestFitBody_MultiByteDescriptionRespectsByteBudget(t *testing.T) {
	desc := strings.Repeat("測", 200) // 200 runes, 600 bytes
	entries := []CatalogEntry{
		{Name: "a", Description: desc, Path: "p", Scope: ScopeUser},
		{Name: "b", Description: desc, Path: "q", Scope: ScopeUser},
	}
	// Force case (b): well below full, comfortably above min-lines.
	var minLen int
	for _, e := range entries {
		minLen += len("- " + e.Name + ": (file: " + e.Path + ")\n")
	}
	minLen += len("### Available skills\n")
	budget := minLen + 30
	body, _ := fitBody(entries, budget)
	if len(body) > budget {
		t.Fatalf("case-(b) body = %d bytes, exceeds byte budget %d (multi-byte rune miscount)", len(body), budget)
	}
}

// F2 — case-(c) must preserve scope priority: once a higher-priority entry is
// omitted for budget, every lower-priority entry after it is omitted too.
// Spec §3.2(c): "minimum lines in scope-priority order until exhausted, rest
// omitted." Keeping a short User entry while dropping a long System entry
// inverts precedence.
func TestFitBody_CaseCPreservesScopePriority(t *testing.T) {
	entries := []CatalogEntry{
		{Name: "a", Description: "d", Path: "pa", Scope: ScopeSystem},
		{Name: "b", Description: "d", Path: strings.Repeat("x", 300), Scope: ScopeSystem},
		{Name: "c", Description: "d", Path: "pc", Scope: ScopeUser},
	}
	// Budget fits header + entry "a" (+ the small "c"), but NOT entry "b".
	header := len("### Available skills\n")
	budget := header + len("- a: (file: pa)\n") + len("- c: (file: pc)\n") + 4
	body, _ := fitBody(entries, budget)
	if !strings.Contains(body, "- a:") {
		t.Fatalf("case-(c) dropped highest-priority entry a: %q", body)
	}
	if strings.Contains(body, "- c:") {
		t.Fatalf("case-(c) kept lower-priority User entry c while System entry b was omitted (precedence inversion): %q", body)
	}
}

// F3 — dedup must key on the CANONICALIZED path so a skill reachable via a
// symlinked directory alias in a second root is listed once, not twice.
// Spec §2: roots "deduped by canonicalized path".
func TestDiscover_DedupBySymlinkCanonicalPath(t *testing.T) {
	tmp := t.TempDir()
	realRoot := filepath.Join(tmp, "real")
	aliasRoot := filepath.Join(tmp, "alias")
	if err := os.MkdirAll(filepath.Join(realRoot, "myskill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "myskill", "SKILL.md"),
		[]byte("---\ndescription: real skill\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(aliasRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// alias/myskill -> real/myskill (a directory symlink, followed for User/Repo)
	if err := os.Symlink(filepath.Join(realRoot, "myskill"), filepath.Join(aliasRoot, "myskill")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	found, _ := Discover([]Root{
		{Path: realRoot, Scope: ScopeRepo, FollowSymlinks: true},
		{Path: aliasRoot, Scope: ScopeUser, FollowSymlinks: true},
	})
	if len(found) != 1 {
		t.Fatalf("symlink-aliased skill discovered %d times, want 1 (dedup by canonical path): %+v", len(found), found)
	}
}

// S1 — a SKILL.md that is itself a symlink to a file OUTSIDE the discovery
// root must never be read (confused-deputy arbitrary file read). Applies even
// with FollowSymlinks=true (containment), and always for FollowSymlinks=false.
// Spec §2 trust boundary; security-validator HIGH.
func TestScanRoot_SymlinkedSkillMDOutsideRootRejected(t *testing.T) {
	tmp := t.TempDir()
	secret := filepath.Join(tmp, "secret.md")
	if err := os.WriteFile(secret, []byte("---\ndescription: SECRET-LEAK-XYZ\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, follow := range []bool{true, false} {
		root := filepath.Join(tmp, "root")
		if err := os.MkdirAll(filepath.Join(root, "evil"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, filepath.Join(root, "evil", "SKILL.md")); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		found, _ := Discover([]Root{{Path: root, Scope: ScopeRepo, FollowSymlinks: follow}})
		for _, sk := range found {
			if strings.Contains(sk.Description, "SECRET-LEAK-XYZ") {
				t.Fatalf("FollowSymlinks=%v: symlinked SKILL.md pointing outside root was read (arbitrary file read)", follow)
			}
		}
		os.RemoveAll(root)
	}
}

// S2 — reads must be size-capped BEFORE buffering, so a symlink to /dev/zero
// (or an enormous real file) cannot OOM the host. We assert the cap path is
// taken (warning emitted) for an oversized SKILL.md; the skill still parses
// from the capped prefix (frontmatter is at the top). security-validator HIGH.
func TestScanRoot_OversizedSkillMDCapped(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "root")
	dir := filepath.Join(root, "big")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("---\ndescription: big skill\n---\n")
	b.WriteString(strings.Repeat("A", (MaxSkillFileBytes)+4096)) // over the cap
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	found, warns := Discover([]Root{{Path: root, Scope: ScopeRepo, FollowSymlinks: true}})
	if len(found) != 1 {
		t.Fatalf("oversized SKILL.md should still parse from capped prefix; got %d skills", len(found))
	}
	hasCapWarn := false
	for _, w := range warns {
		if strings.Contains(strings.ToLower(w.Message), "too large") || strings.Contains(strings.ToLower(w.Message), "cap") || strings.Contains(strings.ToLower(w.Message), "truncat") {
			hasCapWarn = true
		}
	}
	if !hasCapWarn {
		t.Fatalf("oversized SKILL.md read was not size-capped (no cap/truncation warning): %+v", warns)
	}
}

// F4 — a linked mention using a spec §4 URI scheme (skill://, plugin://,
// mcp://, app://) whose path is not a literal filesystem path must fall
// through to plain-name resolution. Spec §4: "exact path match first; then
// plain name only if globally unambiguous".
func TestResolveMention_SchemePathFallsThroughToName(t *testing.T) {
	sk := &Skill{Name: "builder", Path: "/x/builder/SKILL.md", Dir: "/x/builder"}
	ms := ExtractMentions("[$builder](skill://builder/SKILL.md)")
	if len(ms) != 1 || !ms[0].Linked {
		t.Fatalf("expected one linked mention, got %+v", ms)
	}
	got, err := ResolveMention(ms[0], []*Skill{sk}, nil)
	if err != nil {
		t.Fatalf("scheme-path linked mention failed to resolve by name: %v", err)
	}
	if got != sk {
		t.Fatalf("resolved to wrong skill: %+v", got)
	}
}

// F5 — a namespaced name (contains ':') may be up to 128 chars; a plain name
// is still capped at 64. Spec §1.1: "≤64 chars (qualified w/ namespace ≤128)".
func TestParseSkillMD_NamespacedNameAllows128(t *testing.T) {
	ns := "myplugin:" + strings.Repeat("a", 100) // 109 runes, namespaced
	sk, err := ParseSkillMD([]byte("---\nname: "+ns+"\ndescription: d\n---\n"), "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(sk.Name) != len([]rune(ns)) {
		t.Fatalf("namespaced name truncated to %d runes, want %d (≤128 allowed)", utf8.RuneCountInString(sk.Name), len([]rune(ns)))
	}
	// Plain (non-namespaced) still capped at 64.
	plain := strings.Repeat("b", 100)
	sk2, err := ParseSkillMD([]byte("---\nname: "+plain+"\ndescription: d\n---\n"), "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(sk2.Name) != MaxNameChars {
		t.Fatalf("plain name = %d runes, want %d", utf8.RuneCountInString(sk2.Name), MaxNameChars)
	}
}

// F6 — a description longer than the render cap must show the "..." marker.
// Spec §1.1: "≤1024 effective at render (truncate with '...')". Double
// truncation previously produced an exactly-1024 string with no ellipsis.
func TestEntriesFromSkills_LongDescriptionHasEllipsis(t *testing.T) {
	sk, err := ParseSkillMD([]byte("---\ndescription: "+strings.Repeat("x", 2000)+"\n---\n"), "s")
	if err != nil {
		t.Fatal(err)
	}
	got := EntriesFromSkills([]*Skill{sk}, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got))
	}
	if utf8.RuneCountInString(got[0].Description) > PerDescriptionCapChars {
		t.Fatalf("description = %d runes, exceeds cap %d", utf8.RuneCountInString(got[0].Description), PerDescriptionCapChars)
	}
	if !strings.HasSuffix(got[0].Description, "...") {
		t.Fatalf("truncated description lacks '...' marker: ...%q", got[0].Description[len(got[0].Description)-10:])
	}
}

// F7 — an empty AGENTS.override.md must not suppress a populated AGENTS.md in
// the same directory; discovery falls through to the next filename. Spec §6:
// "first hit of AGENTS.override.md > AGENTS.md > ...; empty files skipped".
func TestDiscoverAGENTSMD_EmptyOverrideFallsThroughSameDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.override.md"), []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("REAL-CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	docs := DiscoverAGENTSMD(root, nil, nil, 32*1024)
	if len(docs.Docs) != 1 || !strings.Contains(docs.Docs[0].Content, "REAL-CONTENT") {
		t.Fatalf("empty override suppressed populated AGENTS.md in same dir: %+v", docs.Docs)
	}
}

// S3 — icon path validation must reject traversal, not merely check the
// "assets/" prefix. security-validator LOW (defense-in-depth for future
// asset-serving consumers).
func TestParseSidecar_IconTraversalRejected(t *testing.T) {
	sc := ParseSidecar([]byte("interface:\n  icon_small: assets/../../../../etc/passwd\n"))
	if sc.Interface.IconSmall != "" {
		t.Fatalf("traversal icon path accepted: %q", sc.Interface.IconSmall)
	}
}

// Delta #1 (Sonnet + DeepSeek) — case-(c) must not write the header when the
// header alone exceeds the byte budget; the rendered body must never exceed
// the budget. Budget(10)=4 bytes is a reachable production value (tiny context
// window), far below the 21-byte "### Available skills\n" header.
func TestFitBody_CaseCHeaderOverBudgetDoesNotOvershoot(t *testing.T) {
	entries := []CatalogEntry{
		{Name: "a", Description: "d", Path: "/root/a/SKILL.md", Scope: ScopeUser, RootPath: "/root"},
	}
	budget := Budget(10) // = 4 bytes
	body, _ := fitBody(entries, budget)
	if len(body) > budget {
		t.Fatalf("case-(c) body = %d bytes, exceeds budget %d (header written unconditionally): %q", len(body), budget, body)
	}
}

// Delta #2 (Sonnet) — a colon at or beyond the namespaced cap is stripped by
// truncation, leaving a colon-less name that must NOT keep the 128-char
// namespaced budget. Fix-induced by F5: nameCap decided on the untruncated
// string. Spec §1.1: plain names ≤64.
func TestParseSkillMD_LateColonDoesNotBypassPlainCap(t *testing.T) {
	raw := strings.Repeat("a", 200) + ":x" // colon at rune 200, past the 128 cap
	sk, err := ParseSkillMD([]byte("---\nname: "+raw+"\ndescription: d\n---\n"), "fb")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sk.Name, ":") && utf8.RuneCountInString(sk.Name) > MaxNameChars {
		t.Fatalf("late-colon name bypassed the plain %d-char cap: %d runes, no colon", MaxNameChars, utf8.RuneCountInString(sk.Name))
	}
}

// De-tautologized Budget assertions: literal expected values, independent of
// the implementation formula. Spec §3.2.
func TestBudget_LiteralValues(t *testing.T) {
	if got := Budget(0); got != 8000 {
		t.Errorf("Budget(0) = %d, want 8000 fallback", got)
	}
	if got := Budget(100000); got != 8000 { // 2% = 2000 tokens * 4 bytes
		t.Errorf("Budget(100000) = %d, want 8000", got)
	}
	if got := Budget(10); got != 4 { // 2% floors to 0 -> min 1 token * 4 bytes
		t.Errorf("Budget(10) = %d, want 4", got)
	}
}
