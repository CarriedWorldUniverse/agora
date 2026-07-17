package subagent

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// graphStoreFactories drives the shared behavioral suite below across both
// GraphStore implementations, mirroring internal/persistence's
// LocalStore/MemStore shared-suite pattern.
func graphStoreFactories(t *testing.T) map[string]func() GraphStore {
	return map[string]func() GraphStore{
		"mem": func() GraphStore { return NewMemGraphStore() },
		"file": func() GraphStore {
			dir := t.TempDir()
			fs, err := OpenFileGraphStore(filepath.Join(dir, "graph.jsonl"))
			if err != nil {
				t.Fatalf("OpenFileGraphStore: %v", err)
			}
			t.Cleanup(func() { fs.Close() })
			return fs
		},
	}
}

func TestGraphStore_AddChildrenClose(t *testing.T) {
	for name, factory := range graphStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			g := factory()
			now := time.Unix(0, 0).UTC()
			if err := g.AddEdge(Edge{ParentThread: "root", ChildThread: "b", CreatedAt: now}); err != nil {
				t.Fatalf("AddEdge b: %v", err)
			}
			if err := g.AddEdge(Edge{ParentThread: "root", ChildThread: "a", CreatedAt: now}); err != nil {
				t.Fatalf("AddEdge a: %v", err)
			}
			children, err := g.Children("root", false)
			if err != nil {
				t.Fatalf("Children: %v", err)
			}
			if len(children) != 2 || children[0].ChildThread != "a" || children[1].ChildThread != "b" {
				t.Fatalf("Children = %v, want deterministic [a, b]", children)
			}

			if err := g.CloseEdge("root", "a"); err != nil {
				t.Fatalf("CloseEdge: %v", err)
			}
			openOnly, err := g.Children("root", true)
			if err != nil {
				t.Fatalf("Children openOnly: %v", err)
			}
			if len(openOnly) != 1 || openOnly[0].ChildThread != "b" {
				t.Fatalf("Children openOnly = %v, want [b]", openOnly)
			}

			e, ok, err := g.Edge("root", "a")
			if err != nil || !ok {
				t.Fatalf("Edge lookup: ok=%v err=%v", ok, err)
			}
			if e.Status != EdgeClosed {
				t.Errorf("Status = %v, want closed", e.Status)
			}
		})
	}
}

func TestGraphStore_AddEdge_Duplicate(t *testing.T) {
	for name, factory := range graphStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			g := factory()
			if err := g.AddEdge(Edge{ParentThread: "p", ChildThread: "c"}); err != nil {
				t.Fatalf("first AddEdge: %v", err)
			}
			err := g.AddEdge(Edge{ParentThread: "p", ChildThread: "c"})
			if !errors.Is(err, ErrEdgeExists) {
				t.Fatalf("err = %v, want ErrEdgeExists", err)
			}
		})
	}
}

func TestGraphStore_CloseEdge_NotFound(t *testing.T) {
	for name, factory := range graphStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			g := factory()
			err := g.CloseEdge("p", "nope")
			if !errors.Is(err, ErrEdgeNotFound) {
				t.Fatalf("err = %v, want ErrEdgeNotFound", err)
			}
		})
	}
}

func TestGraphStore_ReopenEdge(t *testing.T) {
	for name, factory := range graphStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			g := factory()
			if err := g.AddEdge(Edge{ParentThread: "p", ChildThread: "c"}); err != nil {
				t.Fatalf("AddEdge: %v", err)
			}
			if err := g.CloseEdge("p", "c"); err != nil {
				t.Fatalf("CloseEdge: %v", err)
			}
			if err := g.ReopenEdge("p", "c"); err != nil {
				t.Fatalf("ReopenEdge: %v", err)
			}
			e, ok, _ := g.Edge("p", "c")
			if !ok || e.Status != EdgeOpen {
				t.Fatalf("edge = %+v ok=%v, want reopened", e, ok)
			}
		})
	}
}

// buildFanoutGraph builds:
//
//	root -> b, a  (root's direct children, added out of order to prove sort)
//	a -> d, c
//	b -> e
//
// BFS from root, level order with each level sorted, must be:
// [a, b] (level 1), [c, d, e] (level 2).
func buildFanoutGraph(t *testing.T, g GraphStore) {
	t.Helper()
	edges := []Edge{
		{ParentThread: "root", ChildThread: "b"},
		{ParentThread: "root", ChildThread: "a"},
		{ParentThread: "a", ChildThread: "d"},
		{ParentThread: "a", ChildThread: "c"},
		{ParentThread: "b", ChildThread: "e"},
	}
	for _, e := range edges {
		if err := g.AddEdge(e); err != nil {
			t.Fatalf("AddEdge %+v: %v", e, err)
		}
	}
}

func TestGraphStore_Descendants_BFS_Deterministic(t *testing.T) {
	for name, factory := range graphStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			g := factory()
			buildFanoutGraph(t, g)

			desc, err := g.Descendants("root", false)
			if err != nil {
				t.Fatalf("Descendants: %v", err)
			}
			got := make([]string, len(desc))
			for i, e := range desc {
				got[i] = e.ChildThread
			}
			want := []string{"a", "b", "c", "d", "e"}
			if len(got) != len(want) {
				t.Fatalf("Descendants = %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("Descendants = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestGraphStore_Descendants_ClosedEdgeHidesSubtree(t *testing.T) {
	for name, factory := range graphStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			g := factory()
			buildFanoutGraph(t, g)
			// Close root->a: a's whole subtree (a, c, d) must vanish from an
			// openOnly BFS (spec §3: "a closed edge hides its subtree").
			if err := g.CloseEdge("root", "a"); err != nil {
				t.Fatalf("CloseEdge: %v", err)
			}
			desc, err := g.Descendants("root", true)
			if err != nil {
				t.Fatalf("Descendants: %v", err)
			}
			got := map[string]bool{}
			for _, e := range desc {
				got[e.ChildThread] = true
			}
			for _, hidden := range []string{"a", "c", "d"} {
				if got[hidden] {
					t.Errorf("Descendants(openOnly) contains %q, want hidden by closed edge", hidden)
				}
			}
			if !got["b"] || !got["e"] {
				t.Errorf("Descendants(openOnly) = %v, want b and e still visible", got)
			}
		})
	}
}

func TestFileGraphStore_PersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.jsonl")

	g1, err := OpenFileGraphStore(path)
	if err != nil {
		t.Fatalf("OpenFileGraphStore: %v", err)
	}
	buildFanoutGraph(t, g1)
	if err := g1.CloseEdge("a", "d"); err != nil {
		t.Fatalf("CloseEdge: %v", err)
	}
	if err := g1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	g2, err := OpenFileGraphStore(path)
	if err != nil {
		t.Fatalf("reopen OpenFileGraphStore: %v", err)
	}
	defer g2.Close()

	desc, err := g2.Descendants("root", false)
	if err != nil {
		t.Fatalf("Descendants after reload: %v", err)
	}
	got := make([]string, len(desc))
	for i, e := range desc {
		got[i] = e.ChildThread
	}
	want := []string{"a", "b", "c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("Descendants after reload = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Descendants after reload = %v, want %v", got, want)
		}
	}

	e, ok, err := g2.Edge("a", "d")
	if err != nil || !ok {
		t.Fatalf("Edge a->d after reload: ok=%v err=%v", ok, err)
	}
	if e.Status != EdgeClosed {
		t.Errorf("a->d Status after reload = %v, want closed (close event replayed)", e.Status)
	}
}
