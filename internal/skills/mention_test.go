package skills_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/skills"
)

func TestExtractMentions_Bare(t *testing.T) {
	got := skills.ExtractMentions("please run $my-skill:v2 now")
	if len(got) != 1 {
		t.Fatalf("got %d mentions, want 1: %+v", len(got), got)
	}
	if got[0].Name != "my-skill:v2" || got[0].Linked {
		t.Errorf("mention = %+v", got[0])
	}
}

func TestExtractMentions_Linked(t *testing.T) {
	got := skills.ExtractMentions("see [$builder](skill://builder/SKILL.md) for details")
	if len(got) != 1 {
		t.Fatalf("got %d mentions, want 1: %+v", len(got), got)
	}
	if !got[0].Linked || got[0].Name != "builder" || got[0].Path != "skill://builder/SKILL.md" {
		t.Errorf("mention = %+v", got[0])
	}
}

func TestExtractMentions_LinkedNotDoubleMatched(t *testing.T) {
	got := skills.ExtractMentions("[$builder](skill://builder/SKILL.md)")
	if len(got) != 1 {
		t.Fatalf("got %d mentions, want exactly 1 (no double match from bare regex): %+v", len(got), got)
	}
}

func TestExtractMentions_EnvVarGuard(t *testing.T) {
	for _, s := range []string{"$PATH", "$HOME", "$user", "$Shell", "$PWD", "$TMPDIR", "$TEMP", "$TMP", "$LANG", "$TERM", "$XDG_CONFIG_HOME"} {
		got := skills.ExtractMentions("echo " + s)
		if len(got) != 0 {
			t.Errorf("%s: expected guarded (0 mentions), got %+v", s, got)
		}
	}
}

func TestExtractMentions_NonGuardedNameNotBlocked(t *testing.T) {
	got := skills.ExtractMentions("$PATHFINDER is a skill")
	if len(got) != 1 || got[0].Name != "PATHFINDER" {
		t.Errorf("expected PATHFINDER to NOT be guarded (exact match only), got %+v", got)
	}
}

func TestResolveMention_ExactPathFirst(t *testing.T) {
	all := []*skills.Skill{
		{Name: "dup", Path: "/a/dup/SKILL.md", Dir: "/a/dup"},
		{Name: "dup", Path: "/b/dup/SKILL.md", Dir: "/b/dup"},
	}
	m := skills.Mention{Name: "dup", Path: "/b/dup/SKILL.md", Linked: true}
	sk, err := skills.ResolveMention(m, all, nil)
	if err != nil {
		t.Fatalf("ResolveMention: %v", err)
	}
	if sk.Path != "/b/dup/SKILL.md" {
		t.Errorf("resolved to %q, want /b/dup/SKILL.md", sk.Path)
	}
}

func TestResolveMention_NameUnambiguous(t *testing.T) {
	all := []*skills.Skill{
		{Name: "unique", Path: "/a/unique/SKILL.md"},
	}
	sk, err := skills.ResolveMention(skills.Mention{Name: "unique"}, all, nil)
	if err != nil {
		t.Fatalf("ResolveMention: %v", err)
	}
	if sk.Path != "/a/unique/SKILL.md" {
		t.Errorf("resolved to %q", sk.Path)
	}
}

func TestResolveMention_AmbiguousNameIgnored(t *testing.T) {
	all := []*skills.Skill{
		{Name: "dup", Path: "/a/dup/SKILL.md"},
		{Name: "dup", Path: "/b/dup/SKILL.md"},
	}
	_, err := skills.ResolveMention(skills.Mention{Name: "dup"}, all, nil)
	if err != skills.ErrAmbiguousMention {
		t.Fatalf("err = %v, want ErrAmbiguousMention", err)
	}
}

func TestResolveMention_NotFound(t *testing.T) {
	_, err := skills.ResolveMention(skills.Mention{Name: "nope"}, nil, nil)
	if err != skills.ErrMentionNotFound {
		t.Fatalf("err = %v, want ErrMentionNotFound", err)
	}
}

func TestResolveMention_DisabledSkillNotResolved(t *testing.T) {
	all := []*skills.Skill{{Name: "off", Path: "/a/off/SKILL.md"}}
	_, err := skills.ResolveMention(skills.Mention{Name: "off"}, all, map[string]bool{"/a/off/SKILL.md": true})
	if err != skills.ErrMentionNotFound {
		t.Fatalf("err = %v, want ErrMentionNotFound (disabled)", err)
	}
}
