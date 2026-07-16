package skills_test

import (
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/agora/internal/skills"
)

func TestMatchScriptRun_Match(t *testing.T) {
	sk := &skills.Skill{Name: "deployer", Dir: "/skills/deployer"}
	scriptPath := filepath.Join(sk.ScriptsDir(), "deploy.sh")

	got, ok := skills.MatchScriptRun([]string{"bash", scriptPath}, []*skills.Skill{sk})
	if !ok {
		t.Fatal("expected a match")
	}
	if got.Name != "deployer" {
		t.Errorf("matched %q, want deployer", got.Name)
	}
}

func TestMatchScriptRun_SkipsFlags(t *testing.T) {
	sk := &skills.Skill{Name: "deployer", Dir: "/skills/deployer"}
	scriptPath := filepath.Join(sk.ScriptsDir(), "deploy.py")

	got, ok := skills.MatchScriptRun([]string{"python3", "-u", scriptPath}, []*skills.Skill{sk})
	if !ok || got.Name != "deployer" {
		t.Fatalf("got=%v ok=%v", got, ok)
	}
}

func TestMatchScriptRun_UnknownInterpreterNoMatch(t *testing.T) {
	sk := &skills.Skill{Name: "deployer", Dir: "/skills/deployer"}
	scriptPath := filepath.Join(sk.ScriptsDir(), "deploy.sh")
	_, ok := skills.MatchScriptRun([]string{"php", scriptPath}, []*skills.Skill{sk})
	if ok {
		t.Fatal("expected no match for unrecognized interpreter")
	}
}

func TestMatchScriptRun_UnknownExtensionNoMatch(t *testing.T) {
	sk := &skills.Skill{Name: "deployer", Dir: "/skills/deployer"}
	scriptPath := filepath.Join(sk.ScriptsDir(), "deploy.exe")
	_, ok := skills.MatchScriptRun([]string{"bash", scriptPath}, []*skills.Skill{sk})
	if ok {
		t.Fatal("expected no match for unrecognized extension")
	}
}

func TestMatchScriptRun_PathOutsideAnyScriptsDirNoMatch(t *testing.T) {
	sk := &skills.Skill{Name: "deployer", Dir: "/skills/deployer"}
	_, ok := skills.MatchScriptRun([]string{"bash", "/tmp/unrelated.sh"}, []*skills.Skill{sk})
	if ok {
		t.Fatal("expected no match: script isn't under any skill's scripts/ dir")
	}
}

func TestMatchDocRead_Match(t *testing.T) {
	sk := &skills.Skill{Name: "guide", Path: "/skills/guide/SKILL.md"}
	got, ok := skills.MatchDocRead("/skills/guide/SKILL.md", []*skills.Skill{sk})
	if !ok || got.Name != "guide" {
		t.Fatalf("got=%v ok=%v", got, ok)
	}
}

func TestMatchDocRead_NoMatch(t *testing.T) {
	sk := &skills.Skill{Name: "guide", Path: "/skills/guide/SKILL.md"}
	_, ok := skills.MatchDocRead("/skills/other/SKILL.md", []*skills.Skill{sk})
	if ok {
		t.Fatal("expected no match")
	}
}
