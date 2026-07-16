package skills_test

import (
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/skills"
)

func TestParseSkillMD_Basic(t *testing.T) {
	src := "---\nname: my-skill\ndescription: Does the thing.\nmetadata:\n  short-description: Short.\n---\n\nBody text.\n"
	sk, err := skills.ParseSkillMD([]byte(src), "dir-name")
	if err != nil {
		t.Fatalf("ParseSkillMD: %v", err)
	}
	if sk.Name != "my-skill" {
		t.Errorf("Name = %q, want my-skill", sk.Name)
	}
	if sk.Description != "Does the thing." {
		t.Errorf("Description = %q", sk.Description)
	}
	if sk.ShortDescription != "Short." {
		t.Errorf("ShortDescription = %q", sk.ShortDescription)
	}
}

func TestParseSkillMD_NameDefaultsToDir(t *testing.T) {
	src := "---\ndescription: Does the thing.\n---\n"
	sk, err := skills.ParseSkillMD([]byte(src), "fallback-dir")
	if err != nil {
		t.Fatalf("ParseSkillMD: %v", err)
	}
	if sk.Name != "fallback-dir" {
		t.Errorf("Name = %q, want fallback-dir (fallback)", sk.Name)
	}
}

func TestParseSkillMD_EmptyDescriptionErrors(t *testing.T) {
	src := "---\nname: x\ndescription: \"\"\n---\n"
	_, err := skills.ParseSkillMD([]byte(src), "d")
	if err == nil {
		t.Fatal("expected error for empty description")
	}
}

func TestParseSkillMD_MissingDescriptionErrors(t *testing.T) {
	src := "---\nname: x\n---\n"
	_, err := skills.ParseSkillMD([]byte(src), "d")
	if err == nil {
		t.Fatal("expected error for missing description")
	}
}

func TestParseSkillMD_OpenerMustBeFirstNonEmptyLine(t *testing.T) {
	src := "some preamble\n---\nname: x\ndescription: y\n---\n"
	_, err := skills.ParseSkillMD([]byte(src), "d")
	if err == nil {
		t.Fatal("expected error: opener not first non-empty line")
	}
}

func TestParseSkillMD_UnterminatedFrontmatterErrors(t *testing.T) {
	src := "---\nname: x\ndescription: y\n"
	_, err := skills.ParseSkillMD([]byte(src), "d")
	if err == nil {
		t.Fatal("expected error: unterminated frontmatter")
	}
}

func TestParseSkillMD_UnknownKeysIgnored(t *testing.T) {
	src := "---\nname: x\ndescription: y\nallowed-tools: Read, Grep\nlicense: MIT\n---\n"
	sk, err := skills.ParseSkillMD([]byte(src), "d")
	if err != nil {
		t.Fatalf("ParseSkillMD: %v", err)
	}
	if sk.Name != "x" || sk.Description != "y" {
		t.Errorf("unexpected parse result: %+v", sk)
	}
}

func TestParseSkillMD_WhitespaceRunsCollapse(t *testing.T) {
	src := "---\nname: x\ndescription: |\n  line one\n  line two   with   spaces\n---\n"
	sk, err := skills.ParseSkillMD([]byte(src), "d")
	if err != nil {
		t.Fatalf("ParseSkillMD: %v", err)
	}
	if strings.Contains(sk.Description, "\n") {
		t.Errorf("Description still has newline: %q", sk.Description)
	}
	if strings.Contains(sk.Description, "   ") {
		t.Errorf("Description still has multi-space run: %q", sk.Description)
	}
}

// TestParseSkillMD_LenientYAMLRepair covers the §1.1 repair case: an
// unquoted scalar containing ": " (e.g. "Build for AWS: ECS") fails
// strict YAML (colon-space inside a plain scalar in a mapping value
// position) and must be repaired by quoting, then reparsed.
func TestParseSkillMD_LenientYAMLRepair(t *testing.T) {
	src := "---\nname: x\ndescription: Build for AWS: ECS\n---\n"
	sk, err := skills.ParseSkillMD([]byte(src), "d")
	if err != nil {
		t.Fatalf("ParseSkillMD (expected lenient repair to succeed): %v", err)
	}
	if sk.Description != "Build for AWS: ECS" {
		t.Errorf("Description = %q, want %q", sk.Description, "Build for AWS: ECS")
	}
}

func TestParseSkillMD_NameTruncatedAt64(t *testing.T) {
	long := strings.Repeat("a", 100)
	src := "---\nname: " + long + "\ndescription: y\n---\n"
	sk, err := skills.ParseSkillMD([]byte(src), "d")
	if err != nil {
		t.Fatalf("ParseSkillMD: %v", err)
	}
	if len([]rune(sk.Name)) != skills.MaxNameChars {
		t.Errorf("Name length = %d, want %d", len([]rune(sk.Name)), skills.MaxNameChars)
	}
}

func TestParseSidecar_Empty(t *testing.T) {
	sc := skills.ParseSidecar([]byte(""))
	if sc.Policy.AllowImplicitInvocation != nil {
		t.Errorf("expected nil AllowImplicitInvocation on empty sidecar")
	}
}

func TestParseSidecar_ParseFailureYieldsEmpty(t *testing.T) {
	sc := skills.ParseSidecar([]byte("not: [valid: yaml: at: all"))
	if sc.Interface.DisplayName != "" || sc.Policy.AllowImplicitInvocation != nil {
		t.Errorf("expected zero-value Sidecar on parse failure, got %+v", sc)
	}
}

func TestParseSidecar_AllowImplicitInvocationFalse(t *testing.T) {
	src := "policy:\n  allow_implicit_invocation: false\n"
	sc := skills.ParseSidecar([]byte(src))
	if sc.Policy.AllowImplicitInvocation == nil || *sc.Policy.AllowImplicitInvocation != false {
		t.Errorf("expected AllowImplicitInvocation=false, got %+v", sc.Policy.AllowImplicitInvocation)
	}
}

func TestParseSidecar_IconMustStartWithAssets(t *testing.T) {
	src := "interface:\n  icon_small: not-assets/x.svg\n"
	sc := skills.ParseSidecar([]byte(src))
	if sc.Interface.IconSmall != "" {
		t.Errorf("expected icon_small dropped, got %q", sc.Interface.IconSmall)
	}
}
