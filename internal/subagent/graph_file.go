package subagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
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
	Op   string `json:"op"` // "add" | "close"
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

func replayGraphLog(f *os.File, mem *MemGraphStore) error {
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev graphEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("decode line: %w", err)
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
		}
	}
	return sc.Err()
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
