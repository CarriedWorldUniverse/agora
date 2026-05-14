// Command agora is the operator-facing CLI for the nexus cluster.
//
// Connects to nexus over WS, opens a TUI chat panel, lets the operator
// type into the aspect's inbox, renders incoming chat in real time,
// and spawns a per-turn engine (claude-code subprocess via bridle)
// when the inbox has work.
//
// See docs/spec.md for the full architecture + behaviour rules.
//
// This is the v0 skeleton (NEX-51). Subsequent commits add the WS
// intake (NEX-52), TUI body (NEX-53), operator-input plumbing
// (NEX-54), output routing (NEX-55), notify_operator tool (NEX-56),
// and streaming render (NEX-57).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/CarriedWorldUniverse/agora/internal/ui"
)

// Keyfile mirrors the subset of nexus's keyfile shape that agora
// cares about today. Once we wire nexus's runtime/keyfile package as
// a real dep (with the replace directive in go.mod), this stub is
// replaced by importing the canonical type. For the v0 skeleton it
// just lets us load the file off disk and pull the .tty block.
type Keyfile struct {
	Version          int       `json:"version"`
	Format           string    `json:"format"`
	Envelope         Envelope  `json:"envelope"`
	EncryptedPayload string    `json:"encrypted_payload"`
	TTY              *TTYBlock `json:"tty,omitempty"`
}

type Envelope struct {
	NexusURL string `json:"nexus_url"`
	NexusID  string `json:"nexus_id"`
	IssuedAt string `json:"issued_at"`
}

// TTYBlock holds agora-specific tuning per the spec §6.2. All fields
// optional with defaults.
type TTYBlock struct {
	OperatorName  string `json:"operator_name,omitempty"`
	HistoryDepth  int    `json:"history_depth,omitempty"`
	InputHistory  int    `json:"input_history,omitempty"`
}

func main() {
	var (
		keyfilePath = flag.String("keyfile", "", "Path to aspect keyfile JSON (required)")
		logFile     = flag.String("log-file", "", "Write logs here; default /tmp/agora.log")
	)
	flag.Parse()

	if *keyfilePath == "" {
		fmt.Fprintln(os.Stderr, "agora: -keyfile required")
		os.Exit(2)
	}

	kf, err := loadKeyfile(*keyfilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agora: %v\n", err)
		os.Exit(2)
	}

	logPath := *logFile
	if logPath == "" {
		logPath = "/tmp/agora.log"
	}
	logCloser, log, err := openLogger(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agora: open log: %v\n", err)
		os.Exit(2)
	}
	defer logCloser()

	log.Info("agora starting",
		"keyfile", *keyfilePath,
		"nexus_url", kf.Envelope.NexusURL,
		"log_file", logPath)

	cfg := ui.Config{
		Logger: log,
		// OperatorName defaults to "operator" if .tty block absent.
		OperatorName: "operator",
	}
	if kf.TTY != nil {
		if kf.TTY.OperatorName != "" {
			cfg.OperatorName = kf.TTY.OperatorName
		}
		if kf.TTY.HistoryDepth > 0 {
			cfg.HistoryDepth = kf.TTY.HistoryDepth
		}
		if kf.TTY.InputHistory > 0 {
			cfg.InputHistory = kf.TTY.InputHistory
		}
	}
	// AspectID would normally come from the validation handshake; for
	// the skeleton, derive a placeholder from the keyfile filename
	// (e.g. "shadow" from "shadow.keyfile.json"). Replaced when NEX-52
	// wires the real keyfile.Client + validation path.
	cfg.AspectID = aspectFromKeyfileName(*keyfilePath)

	model := ui.NewModel(cfg)
	p := tea.NewProgram(model, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		log.Error("bubbletea program ended with error", "err", err)
		os.Exit(1)
	}
	log.Info("agora shutting down")
}

// loadKeyfile reads + JSON-parses the keyfile. v0 skeleton only —
// no validation handshake with nexus yet (NEX-52 wires the real
// keyfile.Client).
func loadKeyfile(path string) (*Keyfile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve keyfile path: %w", err)
	}
	buf, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read keyfile: %w", err)
	}
	var kf Keyfile
	if err := json.Unmarshal(buf, &kf); err != nil {
		return nil, fmt.Errorf("parse keyfile: %w", err)
	}
	if kf.Format != "nexus-keyfile-v1" {
		return nil, fmt.Errorf("unexpected keyfile format %q (want nexus-keyfile-v1)", kf.Format)
	}
	return &kf, nil
}

// openLogger opens the log file (append, mode 0600 — secrets-aware)
// and wraps it in a slog.Logger. Returns a closer the caller defers.
func openLogger(path string) (func(), *slog.Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return func() {}, nil, err
	}
	log := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return func() { _ = f.Close() }, log, nil
}

// aspectFromKeyfileName extracts a best-effort aspect name from the
// keyfile filename. Used only as a placeholder until the real
// validation handshake (NEX-52) returns the canonical AspectName.
// "shadow.keyfile.json" → "shadow"; falls back to "aspect" on weird
// shapes.
func aspectFromKeyfileName(path string) string {
	base := filepath.Base(path)
	for _, suffix := range []string{".keyfile.json", ".json"} {
		if len(base) > len(suffix) && base[len(base)-len(suffix):] == suffix {
			return base[:len(base)-len(suffix)]
		}
	}
	return "aspect"
}
