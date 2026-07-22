// NEX-796 §2 (FTS live-indexing): LocalStore.Append must write items_fts
// rows for user/agent messages AS THEY'RE APPENDED, not only when a later
// RebuildIndex backfills them — /resume search must find text from a live
// session, not just a rebuilt one.

package persistence

import (
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

type ftsRow struct {
	ThreadID string
	Seq      int64
	Text     string
}

// queryFTS reads every items_fts row for threadID, ordered by seq — the
// same shape indexFTS/rebuildIndex write, queried directly against the
// sqlite mirror rather than through List's Text filter, so this test
// verifies the ROWS, not just that a search happens to match.
func queryFTS(t *testing.T, s *LocalStore, threadID string) []ftsRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT thread_id, seq, text FROM items_fts WHERE thread_id = ? ORDER BY seq`, threadID)
	if err != nil {
		t.Fatalf("query items_fts: %v", err)
	}
	defer rows.Close()
	var out []ftsRow
	for rows.Next() {
		var r ftsRow
		if err := rows.Scan(&r.ThreadID, &r.Seq, &r.Text); err != nil {
			t.Fatalf("scan items_fts row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("items_fts rows: %v", err)
	}
	return out
}

// TestAppend_WritesItemsFTS_Live: BEFORE evidence — a fresh store's
// items_fts is empty for a thread with no items. AFTER evidence — appending
// a user_message + agent_message pair through LocalStore.Append (not
// RebuildIndex) immediately produces two items_fts rows with the message
// text, at the item's real seq.
func TestAppend_WritesItemsFTS_Live(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_fts_live", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work/a"})

	// BEFORE: SELECT COUNT(*) FROM items_fts is 0 for this thread — the
	// live regression this test guards (Append alone must not leave the
	// index empty until a rebuild).
	if before := queryFTS(t, s, "th_fts_live"); len(before) != 0 {
		t.Fatalf("items_fts before Append = %+v; want 0 rows (TestAppend_WritesItemsFTS_Live/before_count=0)", before)
	}

	if err := s.Append("th_fts_live", []contracts.ThreadItem{
		{TS: now.Add(time.Second), Type: contracts.TIUserMessage, Payload: "porter pack store metadata design"},
		{TS: now.Add(2 * time.Second), Type: contracts.TIAgentMessage, Payload: "log-structured 32MiB padded casket packs"},
		// A non-message item must NOT produce an items_fts row.
		{TS: now.Add(3 * time.Second), Type: contracts.TIToolCall, Payload: map[string]any{"id": "1", "name": "read_file"}},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// AFTER: SELECT COUNT(*) FROM items_fts = 2 for this thread — the fix
	// (TestAppend_WritesItemsFTS_Live/after_count=2).
	after := queryFTS(t, s, "th_fts_live")
	if len(after) != 2 {
		t.Fatalf("items_fts after Append = %+v; want 2 rows (user_message + agent_message only, not tool_call)", after)
	}
	if after[0].Seq != 1 || after[0].Text != "porter pack store metadata design" {
		t.Fatalf("items_fts[0] = %+v; want seq=1 text=%q", after[0], "porter pack store metadata design")
	}
	if after[1].Seq != 2 || after[1].Text != "log-structured 32MiB padded casket packs" {
		t.Fatalf("items_fts[1] = %+v; want seq=2 text=%q", after[1], "log-structured 32MiB padded casket packs")
	}

	// The live index also feeds List's Text filter end to end.
	found, err := s.List(contracts.ListFilter{Text: "casket packs"})
	if err != nil {
		t.Fatalf("List(Text=...): %v", err)
	}
	if len(found) != 1 || found[0].ThreadID != "th_fts_live" {
		t.Fatalf("List(Text=%q) = %+v; want [th_fts_live]", "casket packs", found)
	}
}

// TestAppend_ItemsFTS_ParityWithRebuildIndex: the rows Append produces live
// must be BYTE-IDENTICAL (thread_id, seq, text) to what a from-scratch
// RebuildIndex produces from the same JSONL — Append is not a shortcut
// with a different shape, it's the same indexFTS call rebuildIndex uses.
func TestAppend_ItemsFTS_ParityWithRebuildIndex(t *testing.T) {
	root := t.TempDir()
	s, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}

	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	mustCreate(t, s, contracts.ThreadMeta{ThreadID: "th_fts_parity", CreatedAt: now, IdentityFP: "agora:x", Profile: "dev", WorkingDir: "/work/a"})
	if err := s.Append("th_fts_parity", []contracts.ThreadItem{
		{TS: now.Add(time.Second), Type: contracts.TIUserMessage, Payload: "single seed derivation"},
		{TS: now.Add(2 * time.Second), Type: contracts.TIAgentMessage, Payload: "recovery cluster org agent keys"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	live := queryFTS(t, s, "th_fts_parity")
	if len(live) != 2 {
		t.Fatalf("live items_fts = %+v; want 2 rows", live)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen fresh (a NEW mirror) and RebuildIndex from the JSONL alone —
	// nothing Appended live this time.
	s2, err := NewLocalStore(root, Config{})
	if err != nil {
		t.Fatalf("reopen NewLocalStore: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	if _, err := s2.RebuildIndex(); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	rebuilt := queryFTS(t, s2, "th_fts_parity")

	if len(rebuilt) != len(live) {
		t.Fatalf("rebuilt items_fts has %d rows; live Append had %d: rebuilt=%+v live=%+v", len(rebuilt), len(live), rebuilt, live)
	}
	for i := range live {
		if rebuilt[i] != live[i] {
			t.Fatalf("items_fts[%d]: rebuilt=%+v != live=%+v (Append and RebuildIndex must produce the SAME rows)", i, rebuilt[i], live[i])
		}
	}
}
