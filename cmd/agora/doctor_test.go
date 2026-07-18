package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeEnv builds a doctorEnv from maps/closures for injection, so tests
// never touch the real machine's PATH, env, or filesystem.
func fakeEnv(t *testing.T) *doctorEnv {
	t.Helper()
	e := doctorEnv{
		lookPath: func(file string) (string, error) {
			return "", errors.New("not found: " + file)
		},
		getenv: func(key string) string { return "" },
		stat: func(path string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
		homedir: func() (string, error) { return "/home/fake", nil },
		getwd:   func() (string, error) { return "/work/fake", nil },
		runVersion: func(path string) (string, error) {
			return "", errors.New("no runVersion stub")
		},
	}
	return &e
}

func runChecksFor(e doctorEnv, checks ...check) (int, string) {
	var buf bytes.Buffer
	code := runDoctorWith(&buf, checks, e)
	return code, buf.String()
}

func TestCheckSidecar_MissingIsFail(t *testing.T) {
	e := fakeEnv(t)
	st, detail := checkSidecar(*e)
	if st != statusFail {
		t.Fatalf("expected FAIL when sidecar not on PATH, got %v (%s)", st, detail)
	}
	if !strings.Contains(detail, sidecarBinaryName) {
		t.Errorf("detail should mention %q, got %q", sidecarBinaryName, detail)
	}
}

func TestCheckSidecar_PresentIsOK(t *testing.T) {
	e := fakeEnv(t)
	e.lookPath = func(file string) (string, error) {
		if file == sidecarBinaryName {
			return "/usr/local/bin/bridle-claude-sidecar", nil
		}
		return "", errors.New("not found")
	}
	st, detail := checkSidecar(*e)
	if st != statusOK {
		t.Fatalf("expected OK, got %v (%s)", st, detail)
	}
	if detail != "/usr/local/bin/bridle-claude-sidecar" {
		t.Errorf("expected detail to be the resolved path, got %q", detail)
	}
}

func TestCheckNode_MissingIsFail(t *testing.T) {
	e := fakeEnv(t)
	st, detail := checkNode(*e)
	if st != statusFail {
		t.Fatalf("expected FAIL when node not on PATH, got %v (%s)", st, detail)
	}
	if !strings.Contains(detail, "node") {
		t.Errorf("detail should mention node, got %q", detail)
	}
}

func TestCheckNode_PresentIsOK(t *testing.T) {
	e := fakeEnv(t)
	e.lookPath = func(file string) (string, error) {
		if file == "node" {
			return "/usr/bin/node", nil
		}
		return "", errors.New("not found")
	}
	e.runVersion = func(path string) (string, error) {
		return "v20.10.0", nil
	}
	st, detail := checkNode(*e)
	if st != statusOK {
		t.Fatalf("expected OK, got %v (%s)", st, detail)
	}
	if !strings.Contains(detail, "v20.10.0") {
		t.Errorf("expected detail to include the version, got %q", detail)
	}
}

func TestCheckClaudeCreds_TokenSetIsOK(t *testing.T) {
	e := fakeEnv(t)
	e.getenv = func(key string) string {
		if key == "CLAUDE_CODE_OAUTH_TOKEN" {
			return "sk-super-secret-value"
		}
		return ""
	}
	st, detail := checkClaudeCreds(*e)
	if st != statusOK {
		t.Fatalf("expected OK, got %v (%s)", st, detail)
	}
	if strings.Contains(detail, "sk-super-secret-value") {
		t.Fatalf("detail must NEVER contain the token value, got %q", detail)
	}
	if !strings.Contains(detail, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("detail should name the env var, got %q", detail)
	}
}

func TestCheckClaudeCreds_CredentialsFileIsOK(t *testing.T) {
	e := fakeEnv(t)
	e.stat = func(path string) (os.FileInfo, error) {
		if strings.HasSuffix(path, ".claude/.credentials.json") || strings.Contains(path, ".credentials.json") {
			return fakeFileInfo{}, nil
		}
		return nil, os.ErrNotExist
	}
	st, detail := checkClaudeCreds(*e)
	if st != statusOK {
		t.Fatalf("expected OK, got %v (%s)", st, detail)
	}
	if !strings.Contains(detail, ".credentials.json") {
		t.Errorf("detail should mention the credentials file, got %q", detail)
	}
}

func TestCheckClaudeCreds_NeitherIsFail(t *testing.T) {
	e := fakeEnv(t)
	st, detail := checkClaudeCreds(*e)
	if st != statusFail {
		t.Fatalf("expected FAIL when neither token nor creds file present, got %v (%s)", st, detail)
	}
	if !strings.Contains(detail, "claude login") {
		t.Errorf("detail should point at `claude login`, got %q", detail)
	}
}

func TestCheckConflictingAuthEnv_APIKeySetIsWarn(t *testing.T) {
	e := fakeEnv(t)
	e.getenv = func(key string) string {
		if key == "ANTHROPIC_API_KEY" {
			return "sk-another-secret"
		}
		return ""
	}
	st, detail := checkConflictingAuthEnv(*e)
	if st != statusWarn {
		t.Fatalf("expected WARN, got %v (%s)", st, detail)
	}
	if strings.Contains(detail, "sk-another-secret") {
		t.Fatalf("detail must NEVER contain the value, got %q", detail)
	}
	if !strings.Contains(detail, "ANTHROPIC_API_KEY") {
		t.Errorf("detail should name the var, got %q", detail)
	}
}

func TestCheckConflictingAuthEnv_AuthTokenSetIsWarn(t *testing.T) {
	e := fakeEnv(t)
	e.getenv = func(key string) string {
		if key == "ANTHROPIC_AUTH_TOKEN" {
			return "secret-token"
		}
		return ""
	}
	st, _ := checkConflictingAuthEnv(*e)
	if st != statusWarn {
		t.Fatalf("expected WARN, got %v", st)
	}
}

func TestCheckConflictingAuthEnv_NoneSetIsOK(t *testing.T) {
	e := fakeEnv(t)
	st, _ := checkConflictingAuthEnv(*e)
	if st != statusOK {
		t.Fatalf("expected OK, got %v", st)
	}
}

func TestCheckWorkingDir_OK(t *testing.T) {
	e := fakeEnv(t)
	st, detail := checkWorkingDir(*e)
	if st != statusOK {
		t.Fatalf("expected OK, got %v (%s)", st, detail)
	}
	if detail != "/work/fake" {
		t.Errorf("expected detail to be the cwd, got %q", detail)
	}
}

func TestCheckWorkingDir_GetwdErrorIsFail(t *testing.T) {
	e := fakeEnv(t)
	e.getwd = func() (string, error) { return "", errors.New("boom") }
	st, _ := checkWorkingDir(*e)
	if st != statusFail {
		t.Fatalf("expected FAIL when Getwd errors, got %v", st)
	}
}

// TestRunDoctorWith_ExitCodeNonZeroIffFail is the acceptance-level test:
// the overall exit code is non-zero exactly when a FAIL is present, driven
// entirely by injected checks (never the real environment).
func TestRunDoctorWith_ExitCodeNonZeroIffFail(t *testing.T) {
	okCheck := check{"ok-check", func(doctorEnv) (status, string) { return statusOK, "fine" }}
	warnCheck := check{"warn-check", func(doctorEnv) (status, string) { return statusWarn, "hmm" }}
	failCheck := check{"fail-check", func(doctorEnv) (status, string) { return statusFail, "broken" }}

	e := fakeEnv(t)

	if code, out := runChecksFor(*e, okCheck, warnCheck); code != 0 {
		t.Fatalf("expected exit 0 with only OK/WARN, got %d\noutput:\n%s", code, out)
	}
	if code, out := runChecksFor(*e, okCheck, warnCheck, failCheck); code != 1 {
		t.Fatalf("expected exit 1 with a FAIL present, got %d\noutput:\n%s", code, out)
	}
	if code, _ := runChecksFor(*e, okCheck); code != 0 {
		t.Fatalf("expected exit 0 with only OK, got %d", code)
	}
	if code, _ := runChecksFor(*e, failCheck); code != 1 {
		t.Fatalf("expected exit 1 with only FAIL, got %d", code)
	}
}

// TestRunDoctorWith_FullSummary runs the real check table (via the real
// doctorChecks()) but through a fully-faked doctorEnv, and asserts the
// summary line and tag lines all appear as expected — an end-to-end
// exercise of the whole `agora doctor` output shape without touching the
// real machine.
func TestRunDoctorWith_FullSummary(t *testing.T) {
	e := fakeEnv(t)
	e.lookPath = func(file string) (string, error) {
		if file == sidecarBinaryName || file == "node" {
			return "/usr/bin/" + file, nil
		}
		return "", errors.New("not found")
	}
	e.getenv = func(key string) string {
		if key == "CLAUDE_CODE_OAUTH_TOKEN" {
			return "secret"
		}
		return ""
	}

	code, out := runChecksFor(*e, doctorChecks()...)
	if code != 0 {
		t.Fatalf("expected exit 0 (all satisfiable checks OK), got %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "[ OK ] sidecar on PATH") {
		t.Errorf("missing sidecar OK line:\n%s", out)
	}
	if !strings.Contains(out, "[ OK ] node on PATH") {
		t.Errorf("missing node OK line:\n%s", out)
	}
	if !strings.Contains(out, "[ OK ] Claude credentials") {
		t.Errorf("missing creds OK line:\n%s", out)
	}
	if !strings.Contains(out, "doctor: 5 ok, 0 warn, 0 fail") {
		t.Errorf("missing/incorrect summary line:\n%s", out)
	}
	if !strings.Contains(out, "ready for a live turn") {
		t.Errorf("missing ready-for-turn tail:\n%s", out)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("output must NEVER contain the token value:\n%s", out)
	}
}

type fakeFileInfo struct{ os.FileInfo }

func (fakeFileInfo) Name() string { return ".credentials.json" }
