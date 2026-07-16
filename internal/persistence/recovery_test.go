package persistence

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// TestTornLastLineTolerated: a crash leaves a torn (newline-less) partial
// final line. Read must return every fully-written prior item and skip the
// torn tail — spec §1 crash-safety / §2 "corruption is an inconvenience,
// never data loss". Regression for review finding #1 (PR #38): the old code
// hard-failed the whole thread on any decode error.
func TestTornLastLineTolerated(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_torn", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/w"})
	if err := s.Append("th_torn", []contracts.ThreadItem{
		{TS: now.Add(time.Second), Type: contracts.TIUserMessage, Payload: "good one"},
		{TS: now.Add(2 * time.Second), Type: contracts.TIAgentMessage, Payload: "good two"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_ = s.Close()

	// Simulate a crash mid-append: glue a torn partial JSON line (no newline)
	// onto the end of the file.
	path := threadPath(root, now, "th_torn")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for torn append: %v", err)
	}
	if _, err := f.WriteString(`{"seq":3,"type":"agent_message","payl`); err != nil {
		t.Fatalf("write torn: %v", err)
	}
	_ = f.Close()

	// Reopen and read: the two good items survive, the torn tail is dropped.
	s2, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	items := resumeAll(t, s2, "th_torn")
	if len(items) != 2 {
		t.Fatalf("after torn tail: got %d items, want 2: %+v", len(items), items)
	}

	// And a NORMAL append after the crash must heal the torn tail, not glue
	// onto it — the new item must be readable and the torn fragment gone.
	if err := s2.Append("th_torn", []contracts.ThreadItem{
		{TS: now.Add(3 * time.Second), Type: contracts.TIUserMessage, Payload: "after recovery"},
	}); err != nil {
		t.Fatalf("Append after torn: %v", err)
	}
	items = resumeAll(t, s2, "th_torn")
	if len(items) != 3 {
		t.Fatalf("after heal+append: got %d items, want 3: %+v", len(items), items)
	}
	if p, _ := items[2].Payload.(string); p != "after recovery" {
		t.Fatalf("items[2] = %+v, want the recovery item", items[2])
	}
}

// TestMidFileCorruptionIsHardError: a decode failure on a NON-final line is
// genuine mid-file corruption (not a torn write, which only affects the
// tail) and must be a hard error, not silently skipped.
func TestMidFileCorruptionIsHardError(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_mid", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/w"})
	if err := s.Append("th_mid", []contracts.ThreadItem{
		{TS: now.Add(time.Second), Type: contracts.TIUserMessage, Payload: "one"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	_ = s.Close()

	// Insert a garbage complete line (with newline) followed by a good line —
	// the garbage is NOT the last line, so it must hard-error.
	path := threadPath(root, now, "th_mid")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString("this is not json\n" + `{"seq":2,"type":"user_message"}` + "\n")
	_ = f.Close()

	_, _, err = readThreadFile(path)
	if err == nil {
		t.Fatal("mid-file corruption must be a hard error, got nil")
	}
}

// TestThreadIDTraversalRejected: a crafted thread id must never let a write
// escape the store root. Regression for the HIGH security finding (PR #38).
// Both stores must reject identically (behavioral parity).
func TestThreadIDTraversalRejected(t *testing.T) {
	bad := []string{
		"../../../../tmp/pwned",
		"..",
		".",
		"a/b",
		`a\b`,
		"",
		"th_ok/../escape",
		"with space",
		"semi;colon",
	}
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)

	t.Run("local", func(t *testing.T) {
		root := t.TempDir()
		s, err := NewLocalStore(root, Config{})
		if err != nil {
			t.Fatalf("NewLocalStore: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		for _, id := range bad {
			err := s.Create(contracts.ThreadMeta{ThreadID: id, CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/w"})
			if err == nil {
				t.Errorf("Create(%q) should have been rejected", id)
			}
		}
		// Nothing escaped the root: the traversal target must not exist.
		if _, err := os.Stat("/tmp/pwned.jsonl"); err == nil {
			t.Fatal("traversal created /tmp/pwned.jsonl — root escape!")
		}
		// A clean id still works.
		if err := s.Create(contracts.ThreadMeta{ThreadID: "th_ok_1", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/w"}); err != nil {
			t.Fatalf("clean id rejected: %v", err)
		}
	})

	t.Run("mem", func(t *testing.T) {
		s := NewMemStore()
		for _, id := range bad {
			if err := s.Create(contracts.ThreadMeta{ThreadID: id, CreatedAt: now}); err == nil {
				t.Errorf("MemStore.Create(%q) should have been rejected", id)
			}
		}
	})
}

// TestListOrderParity: LocalStore and MemStore must return List() in the
// SAME order (updated_at DESC, id ASC). Regression for the review finding
// that MemStore sorted by id while LocalStore sorted by updated_at.
func TestListOrderParity(t *testing.T) {
	now := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	build := func(s contracts.ThreadStore) []string {
		// three threads; make updated_at differ via appends at different TS.
		for _, id := range []string{"th_a", "th_b", "th_c"} {
			if err := s.Create(contracts.ThreadMeta{ThreadID: id, CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/w"}); err != nil {
				t.Fatal(err)
			}
		}
		// th_b most recent, th_a middle, th_c oldest (created-only).
		if err := s.Append("th_a", []contracts.ThreadItem{{TS: now.Add(1 * time.Hour), Type: contracts.TIUserMessage, Payload: "a"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Append("th_b", []contracts.ThreadItem{{TS: now.Add(2 * time.Hour), Type: contracts.TIUserMessage, Payload: "b"}}); err != nil {
			t.Fatal(err)
		}
		metas, err := s.List(contracts.ListFilter{})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(metas))
		for i, m := range metas {
			ids[i] = m.ThreadID
		}
		return ids
	}

	local, err := NewLocalStore(t.TempDir(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	localOrder := build(local)
	memOrder := build(NewMemStore())

	if strings.Join(localOrder, ",") != strings.Join(memOrder, ",") {
		t.Fatalf("List order diverges: local=%v mem=%v", localOrder, memOrder)
	}
	// And it must be recency order: th_b (newest) before th_a before th_c.
	if strings.Join(localOrder, ",") != "th_b,th_a,th_c" {
		t.Fatalf("List order = %v, want th_b,th_a,th_c (updated_at desc)", localOrder)
	}
}
