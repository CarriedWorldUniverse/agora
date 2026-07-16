package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// FallbackBudgetChars is used when the model's context window is unknown.
// Spec: agora-spec-skills.md §3.2.
const FallbackBudgetChars = 8000

// PerDescriptionCapChars truncates any single description before fitting.
// Spec §3.2.
const PerDescriptionCapChars = 1024

// BytesPerToken is the token estimate used for the budget conversion
// (Spec §3.2: "Token estimate ≈ bytes/4").
const BytesPerToken = 4

// Budget computes the catalog's character budget from a context window
// (in tokens). contextWindowTokens<=0 means "no window known" and yields
// the char fallback. Spec §3.2: "Default = 2% of context window in
// tokens (min 1); no window known ⇒ 8000-char fallback."
func Budget(contextWindowTokens int) int {
	if contextWindowTokens <= 0 {
		return FallbackBudgetChars
	}
	tokens := contextWindowTokens * 2 / 100
	if tokens < 1 {
		tokens = 1
	}
	return tokens * BytesPerToken
}

// CatalogEntry is one skill's every-turn catalog row (name + description
// + path only — §3.1 progressive disclosure).
type CatalogEntry struct {
	Name        string
	Description string
	Path        string
	Scope       Scope
	// RootPath is the discovery root this entry came from, used only for
	// the §3.2 path-alias optimization.
	RootPath string
}

// EntriesFromSkills builds catalog entries from discovered skills,
// filtering to those eligible for the every-turn catalog: enabled (not in
// disabledPaths) and allow_implicit_invocation != false.
// Spec §3.1, §4 ("Disabled skills = path present in a disabled-paths set").
func EntriesFromSkills(all []*Skill, disabledPaths map[string]bool) []CatalogEntry {
	var out []CatalogEntry
	for _, sk := range all {
		if disabledPaths[sk.Path] {
			continue
		}
		if !sk.AllowImplicitInvocation() {
			continue
		}
		desc := truncateWithEllipsis(sk.Description, PerDescriptionCapChars)
		out = append(out, CatalogEntry{Name: sk.Name, Description: desc, Path: sk.Path, Scope: sk.Scope, RootPath: sk.RootPath})
	}
	return out
}

// Catalog is a rendered <skills_instructions> fragment plus warnings
// raised while fitting it to budget.
type Catalog struct {
	Text     string
	Warnings []string
}

const catalogIntro = "Skills are discovered SKILL.md directories. Mention a skill with $name to load its full instructions for this turn (not sticky — re-mention next turn to reload)."

// RenderCatalog builds the developer-role <skills_instructions> fragment
// per §3.1, fitting entries within budgetChars per §3.2's three cases.
// Entries should already be in render order (see Discover/renderRank);
// RenderCatalog does not re-sort — callers control priority order for
// the case-(c) omission cutoff.
func RenderCatalog(entries []CatalogEntry, budgetChars int) Catalog {
	body, warnings := fitBody(entries, budgetChars)
	var sb strings.Builder
	sb.WriteString("<skills_instructions>\n")
	sb.WriteString("## Skills\n")
	sb.WriteString(catalogIntro + "\n")
	sb.WriteString(body)
	sb.WriteString("</skills_instructions>")
	return Catalog{Text: sb.String(), Warnings: warnings}
}

// fitBody implements the §3.2 fitting cases and returns the
// "### Available skills" section (with an optional "### Skill roots"
// alias table prepended when it lets more entries fit).
func fitBody(entries []CatalogEntry, budget int) (string, []string) {
	if len(entries) == 0 {
		return "### Available skills\n(none)\n", nil
	}

	absBody, absWarn, absFit := fitVariant(entries, budget, false)
	aliasBody, aliasWarn, aliasFit := fitVariant(entries, budget, true)

	if aliasFit > absFit {
		return aliasBody, aliasWarn
	}
	return absBody, absWarn
}

// fitVariant renders the catalog once with either absolute paths or
// root-aliased relative paths, and reports how many entries it managed
// to represent (full or partial) within budget — used to pick the
// better variant (§3.2 "Alias optimization").
func fitVariant(entries []CatalogEntry, budget int, useAlias bool) (string, []string, int) {
	var roots []string
	rootIdx := map[string]int{}
	displayPath := func(p string) string {
		if !useAlias {
			return p
		}
		root := ""
		for _, r := range roots {
			if strings.HasPrefix(p, r) {
				if len(r) > len(root) {
					root = r
				}
			}
		}
		if root == "" {
			return p
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return p
		}
		return fmt.Sprintf("r%d/%s", rootIdx[root], rel)
	}

	if useAlias {
		seen := map[string]bool{}
		for _, e := range entries {
			r := e.RootPath
			if r == "" {
				// No known discovery root (e.g. built directly in a
				// test) — fall back to the skill's own directory so
				// aliasing still shortens the path.
				r = filepath.Dir(e.Path)
			}
			if !seen[r] {
				seen[r] = true
				rootIdx[r] = len(roots)
				roots = append(roots, r)
			}
		}
	}

	// header overhead
	var header strings.Builder
	if useAlias && len(roots) > 0 {
		header.WriteString("### Skill roots\n")
		for i, r := range roots {
			header.WriteString(fmt.Sprintf("- `r%d` = `%s`\n", i, r))
		}
	}
	header.WriteString("### Available skills\n")

	full := func(e CatalogEntry) string {
		return fmt.Sprintf("- %s: %s (file: %s)\n", e.Name, e.Description, displayPath(e.Path))
	}
	minimal := func(e CatalogEntry, desc string) string {
		if desc == "" {
			return fmt.Sprintf("- %s: (file: %s)\n", e.Name, displayPath(e.Path))
		}
		return fmt.Sprintf("- %s: %s (file: %s)\n", e.Name, desc, displayPath(e.Path))
	}

	headerLen := header.Len()

	// Case (a): everything fits at full size.
	var fullLines strings.Builder
	for _, e := range entries {
		fullLines.WriteString(full(e))
	}
	if headerLen+fullLines.Len() <= budget {
		return header.String() + fullLines.String(), nil, len(entries)
	}

	// Case (b): minimum lines (no description) + round-robin description
	// chars, one char at a time, until budget exhausted.
	minLen := headerLen
	for _, e := range entries {
		minLen += len(minimal(e, ""))
	}
	if minLen <= budget {
		// Round-robin description characters one at a time. The budget is a
		// BYTE budget (Budget() = tokens*4; the case-a/min checks use len()),
		// so each appended rune costs its UTF-8 byte length, not 1 — else
		// multi-byte (e.g. CJK) descriptions overshoot the budget 2-4x
		// (review finding F1). A rune too wide for the remaining budget is
		// skipped for that entry (descChars must stay a contiguous prefix);
		// the loop ends when no entry can afford its next rune.
		descRunes := make([][]rune, len(entries))
		for i, e := range entries {
			descRunes[i] = []rune(e.Description)
		}
		descChars := make([]int, len(entries))
		remaining := budget - minLen
		progress := true
		for remaining > 0 && progress {
			progress = false
			for i := range entries {
				if remaining <= 0 {
					break
				}
				if descChars[i] >= len(descRunes[i]) {
					continue
				}
				cost := utf8.RuneLen(descRunes[i][descChars[i]])
				if cost < 0 {
					cost = 1 // invalid rune (RuneError); charge a byte
				}
				if descChars[i] == 0 {
					// First description char for this entry: minimal(e, desc)
					// inserts a separator space before "(file: ...)" that the
					// minimal(e, "") line (counted in minLen) does not have.
					cost++
				}
				if cost > remaining {
					continue
				}
				descChars[i]++
				remaining -= cost
				progress = true
			}
		}
		var sb strings.Builder
		sb.WriteString(header.String())
		truncatedAny := false
		for i, e := range entries {
			d := string(descRunes[i][:descChars[i]])
			if descChars[i] < len(descRunes[i]) {
				truncatedAny = true
			}
			sb.WriteString(minimal(e, d))
		}
		var warns []string
		if truncatedAny {
			warns = append(warns, "skills catalog: descriptions truncated to fit budget")
		}
		return sb.String(), warns, len(entries)
	}

	// Case (c): minimum lines in scope-priority (render) order until
	// budget exhausted; rest omitted.
	ordered := make([]CatalogEntry, len(entries))
	copy(ordered, entries)
	sortByRenderOrder(ordered)

	var sb strings.Builder
	sb.WriteString(header.String())
	used := headerLen
	included := 0
	var omitted []string
	for idx, e := range ordered {
		line := minimal(e, "")
		if used+len(line) > budget {
			// Preserve scope priority: once an entry does not fit, every
			// lower-priority entry after it (ordered is scope-priority
			// sorted) is omitted too — never keep a lower-priority skill
			// while a higher-priority one was dropped (spec §3.2(c): "in
			// scope-priority order until exhausted, rest omitted"; review
			// finding F2).
			for _, rem := range ordered[idx:] {
				omitted = append(omitted, rem.Name)
			}
			break
		}
		sb.WriteString(line)
		used += len(line)
		included++
	}
	var warns []string
	if len(omitted) > 0 {
		warns = append(warns, fmt.Sprintf("skills catalog: %d skill(s) omitted for budget: %s", len(omitted), strings.Join(omitted, ", ")))
	}
	return sb.String(), warns, included
}

func sortByRenderOrder(entries []CatalogEntry) {
	// insertion sort is fine at catalog scale; keeps this file
	// dependency-light and stable.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && lessRenderOrder(entries[j], entries[j-1]); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

func lessRenderOrder(a, b CatalogEntry) bool {
	ra, rb := renderRank(a.Scope), renderRank(b.Scope)
	if ra != rb {
		return ra < rb
	}
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.Path < b.Path
}

func truncateWithEllipsis(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}

// RenderInvocation reads a skill's full SKILL.md (capped at 8000 bytes,
// with a truncation warning) and wraps it as the §3.3 user-role
// fragment. Non-sticky by contract of the caller (context.go's fixed
// contract #3: state fragments regenerated fresh) — RenderInvocation
// itself is stateless.
// Spec: agora-spec-skills.md §3.3.
const InvocationBodyCapBytes = 8000
const InvocationNameCapBytes = 256
const InvocationPathCapBytes = 1024

func RenderInvocation(sk *Skill) (string, []string, error) {
	data, err := os.ReadFile(sk.Path)
	if err != nil {
		return "", nil, err
	}
	var warnings []string
	body := data
	if len(body) > InvocationBodyCapBytes {
		body = body[:InvocationBodyCapBytes]
		warnings = append(warnings, fmt.Sprintf("skill %q: SKILL.md truncated to %d bytes", sk.Name, InvocationBodyCapBytes))
	}
	name := capBytes(sk.Name, InvocationNameCapBytes)
	path := capBytes(sk.Path, InvocationPathCapBytes)
	frag := fmt.Sprintf("<skill>\n<name>%s</name>\n<path>%s</path>\n%s\n</skill>", name, path, string(body))
	return frag, warnings, nil
}

func capBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
