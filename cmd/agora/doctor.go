// `agora doctor`: a live-turn preflight. It checks the RUNTIME
// prerequisites for a claude-sdk turn (the bridle-claude-sidecar entry
// point, a Node runtime, and ambient Claude credentials) and reports each
// check as [ OK ]/[WARN]/[FAIL], exiting non-zero if any hard check FAILs —
// so the operator (and later live-turn work) can confirm the environment
// before the first live turn.
//
// Scope (deliberately narrow): LookPath/stat/env checks only. No network
// calls, no spawning the sidecar, no running a real claude turn — see the
// build brief (NEX-790).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// status is a check's outcome.
type status int

const (
	statusOK status = iota
	statusWarn
	statusFail
)

func (s status) tag() string {
	switch s {
	case statusOK:
		return "[ OK ]"
	case statusWarn:
		return "[WARN]"
	case statusFail:
		return "[FAIL]"
	default:
		return "[????]"
	}
}

// doctorEnv holds the doctor's inputs as injectable functions, so the
// checks are testable without depending on the real machine's PATH, env,
// or filesystem. Real `agora doctor` runs use newRealDoctorEnv; tests
// construct a doctorEnv by hand with fakes.
type doctorEnv struct {
	lookPath func(file string) (string, error)
	getenv   func(key string) string
	stat     func(path string) (os.FileInfo, error)
	homedir  func() (string, error)
	getwd    func() (string, error)
	// runVersion runs "<path> --version" and returns its trimmed combined
	// output. Separated from lookPath so tests can stub it without
	// executing a real binary.
	runVersion func(path string) (string, error)
}

func newRealDoctorEnv() doctorEnv {
	return doctorEnv{
		lookPath: exec.LookPath,
		getenv:   os.Getenv,
		stat:     os.Stat,
		homedir:  os.UserHomeDir,
		getwd:    os.Getwd,
		runVersion: func(path string) (string, error) {
			out, err := exec.Command(path, "--version").CombinedOutput()
			return trimTrailingNewline(string(out)), err
		},
	}
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// check is one doctor check: a name and a run func over the injected env.
type check struct {
	name string
	run  func(doctorEnv) (status, string)
}

// doctorChecks is the ordered list of checks `agora doctor` runs.
func doctorChecks() []check {
	return []check{
		{"sidecar on PATH", checkSidecar},
		{"node on PATH", checkNode},
		{"Claude credentials", checkClaudeCreds},
		{"conflicting auth env", checkConflictingAuthEnv},
		{"working directory", checkWorkingDir},
	}
}

// sidecarBinaryName is the default bridle-claude-sidecar entry point name
// (mirrors bridle provider/claudesdk.SidecarPath's default). A future
// config/env override for a non-default sidecar path can extend this check;
// for v1 the default name is checked on PATH.
const sidecarBinaryName = "bridle-claude-sidecar"

func checkSidecar(e doctorEnv) (status, string) {
	path, err := e.lookPath(sidecarBinaryName)
	if err != nil {
		return statusFail, fmt.Sprintf(
			"%s not on PATH — install/symlink the bridle-claude-sidecar entry point; claude-sdk turns can't run without it",
			sidecarBinaryName)
	}
	return statusOK, path
}

func checkNode(e doctorEnv) (status, string) {
	path, err := e.lookPath("node")
	if err != nil {
		return statusFail, "node not on PATH — the bridle-claude-sidecar is a Node app and needs a Node runtime"
	}
	if e.runVersion == nil {
		return statusOK, path
	}
	ver, verErr := e.runVersion(path)
	if verErr != nil || ver == "" {
		return statusOK, path
	}
	return statusOK, fmt.Sprintf("%s (%s)", path, ver)
}

func checkClaudeCreds(e doctorEnv) (status, string) {
	if e.getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" {
		return statusOK, "via CLAUDE_CODE_OAUTH_TOKEN env"
	}
	home, err := e.homedir()
	if err == nil && home != "" {
		credsPath := filepath.Join(home, ".claude", ".credentials.json")
		if _, statErr := e.stat(credsPath); statErr == nil {
			return statusOK, "via ~/.claude/.credentials.json"
		}
	}
	return statusFail, "no Claude subscription creds — run `claude login`, or set CLAUDE_CODE_OAUTH_TOKEN"
}

func checkConflictingAuthEnv(e doctorEnv) (status, string) {
	var set []string
	if e.getenv("ANTHROPIC_API_KEY") != "" {
		set = append(set, "ANTHROPIC_API_KEY")
	}
	if e.getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		set = append(set, "ANTHROPIC_AUTH_TOKEN")
	}
	if len(set) == 0 {
		return statusOK, "unset"
	}
	names := set[0]
	for _, n := range set[1:] {
		names += ", " + n
	}
	return statusWarn, fmt.Sprintf(
		"%s set but IGNORED for the funnel/subscription lane — claudesdk scrubs them so they can't outrank CLAUDE_CODE_OAUTH_TOKEN; unset them to avoid confusion",
		names)
}

func checkWorkingDir(e doctorEnv) (status, string) {
	wd, err := e.getwd()
	if err != nil {
		return statusFail, fmt.Sprintf("Getwd: %v", err)
	}
	return statusOK, wd
}

// runDoctorWith runs the given checks against the given env, writing
// [OK]/[WARN]/[FAIL] lines plus a summary to w, and returns the exit code
// (0 if no FAIL, 1 if any FAIL). Split from runDoctor so tests can drive it
// with a fake doctorEnv and a buffer, without touching os.Stdout or the
// real environment.
func runDoctorWith(w interface{ Write([]byte) (int, error) }, checks []check, e doctorEnv) int {
	var ok, warn, fail int
	for _, c := range checks {
		st, detail := c.run(e)
		switch st {
		case statusOK:
			ok++
		case statusWarn:
			warn++
		case statusFail:
			fail++
		}
		line := fmt.Sprintf("%s %s", st.tag(), c.name)
		if detail != "" {
			line += " — " + detail
		}
		fmt.Fprintln(w, line)
	}

	summary := fmt.Sprintf("doctor: %d ok, %d warn, %d fail", ok, warn, fail)
	if fail == 0 {
		summary += " — ready for a live turn"
	} else {
		summary += " — fix the FAILs above first"
	}
	fmt.Fprintln(w, summary)

	if fail > 0 {
		return 1
	}
	return 0
}

// runDoctor implements `agora doctor` (no flags currently). Kept thin over
// runDoctorWith, mirroring runDaemon's shape in daemon.go: a small CLI
// wrapper over a directly-testable Go API.
func runDoctor(args []string) {
	code := runDoctorWith(os.Stdout, doctorChecks(), newRealDoctorEnv())
	os.Exit(code)
}
