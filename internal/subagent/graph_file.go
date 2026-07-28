package subagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// FileGraphStore is a JSONL-persisted GraphStore: an append-only event log
// (add|close per line) that replays into an in-memory index on open —
// mirrors internal/persistence's append+replay shape, sized to this
// package's much smaller need (spec §3 calls the whole store "tiny").
type FileGraphStore struct {
	mu  sync.Mutex
	mem *MemGraphStore
	f   *os.File
}

// graphEvent is one JSONL line: an edge add or a status close.
type graphEvent struct {
	Op   string `json:"op"` // "add" | "close" | "reopen" | "outcome"
	Edge Edge   `json:"edge"`
}

// OpenFileGraphStore opens (creating if absent) the JSONL log at path and
// replays it into memory. Callers must Close() when done — Windows requires
// the handle closed before any temp-dir cleanup (ground rule 5).
func OpenFileGraphStore(path string) (*FileGraphStore, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("subagent: open graph store %s: %w", path, err)
	}
	mem := NewMemGraphStore()
	if err := replayGraphLog(f, mem); err != nil {
		f.Close()
		return nil, fmt.Errorf("subagent: replay graph store %s: %w", path, err)
	}
	return &FileGraphStore{mem: mem, f: f}, nil
}

// replayGraphLog reads the whole file and replays each complete event line
// into mem. FIX 6 / ground rule "inconvenience, never data loss": a
// TRUNCATED TRAILING line (a partial write left by a process killed
// mid-append) is tolerated — the valid prefix is loaded — mirroring
// internal/persistence's readThreadFile torn-tail handling. A decode
// failure on any COMPLETE (newline-terminated, non-final) line is still a
// hard error: that is real mid-file corruption, never a torn write, which
// can only ever affect the tail.
func replayGraphLog(f *os.File, mem *MemGraphStore) error {
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read graph store: %w", err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	// bytes.Split always leaves a trailing element after the final '\n':
	// empty when the file ends cleanly, or the torn partial final line when
	// a crash left no terminating newline. Either way it is NOT a complete
	// line — drop it before decoding.
	if n := len(lines); n > 0 {
		lines = lines[:n-1]
	}
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		var ev graphEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// The torn trailing line was already dropped above; every line
			// here is complete, so a decode failure is real corruption, not
			// a torn write — hard error.
			return fmt.Errorf("decode line %d: %w", i+1, err)
		}
		switch ev.Op {
		case "add":
			// Replay is best-effort idempotent: an add already present (e.g.
			// a partially-flushed prior run) is not a hard error on reload.
			if err := mem.AddEdge(ev.Edge); err != nil {
				continue
			}
		case "close":
			if err := mem.CloseEdge(ev.Edge.ParentThread, ev.Edge.ChildThread); err != nil {
				continue
			}
		case "reopen":
			if err := mem.ReopenEdge(ev.Edge.ParentThread, ev.Edge.ChildThread); err != nil {
				continue
			}
		case "outcome":
			// agora#158. Best-effort like the others: an outcome for an edge
			// this replay has not seen (a torn prior run) is skipped rather
			// than failing the whole reload.
			if err := mem.RecordOutcome(ev.Edge.ParentThread, ev.Edge.ChildThread, ev.Edge.Outcome, ev.Edge.FinishedAt); err != nil {
				continue
			}
		}
	}
	return nil
}

func (g *FileGraphStore) appendEvent(ev graphEvent) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("subagent: encode graph event: %w", err)
	}
	if _, err := g.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("subagent: write graph event: %w", err)
	}
	return g.f.Sync()
}

var _ GraphStore = (*FileGraphStore)(nil)

func (g *FileGraphStore) AddEdge(e Edge) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.mem.AddEdge(e); err != nil {
		return err
	}
	if err := g.appendEvent(graphEvent{Op: "add", Edge: e}); err != nil {
		return err
	}
	return nil
}

func (g *FileGraphStore) CloseEdge(parent, child string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.mem.CloseEdge(parent, child); err != nil {
		return err
	}
	e, _, _ := g.mem.Edge(parent, child)
	return g.appendEvent(graphEvent{Op: "close", Edge: e})
}
func (g *FileGraphStore) RecordOutcome(parent, child string, status NodeStatus, ts time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.mem.RecordOutcome(parent, child, status, ts); err != nil {
		return err
	}
	return g.appendEvent(graphEvent{Op: "outcome", Edge: Edge{
		ParentThread: parent, ChildThread: child, Outcome: status, FinishedAt: ts.UTC(),
	}})
}

func (g *FileGraphStore) ReopenEdge(parent, child string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.mem.ReopenEdge(parent, child); err != nil {
		return err
	}
	e, _, _ := g.mem.Edge(parent, child)
	return g.appendEvent(graphEvent{Op: "reopen", Edge: e})
}

func (g *FileGraphStore) Edge(parent, child string) (Edge, bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mem.Edge(parent, child)
}

func (g *FileGraphStore) Children(parent string, openOnly bool) ([]Edge, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mem.Children(parent, openOnly)
}

func (g *FileGraphStore) Descendants(root string, openOnly bool) ([]Edge, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.mem.Descendants(root, openOnly)
}

// Close closes the underlying file handle.
func (g *FileGraphStore) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.f.Close()
}
