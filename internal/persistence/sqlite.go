package persistence

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"

	_ "modernc.org/sqlite" // pure-Go driver, registers as "sqlite"
)

// schemaDDL creates the SQLite mirror tables. Spec §2:
//   - threads: indexed working_dir/project_root/updated_at/identity_fp.
//   - agent_edges: the agent-graph store (subagents spec §3) — table exists
//     per §2 so later units don't need a migration; U3 doesn't populate it.
//   - items_fts: lite text index for /resume search over user/agent message
//     text (spec calls the real FTS index "optional"; this is a minimal
//     LIKE-queryable stand-in, not SQLite FTS5 — see List's Text filter).
//
// PRIMARY vs derived state (spec §2): most columns are derived from the
// JSONL and reconstructed by RebuildIndex. Two are PRIMARY daemon state,
// held only in state.db and NOT derivable from the JSONL — exactly the
// carve-out §2 names ("daemon state — enrollments, hook trust, session
// grants — is primary, small, backed up with the identity dir"):
//   - archived: the JSONL item-type enum has no way to encode "archived"
//     and the meta line is never rewritten, so archived cannot be derived.
//   - agent_edges: an edge's status (open/closed) is not derivable from any
//     single thread's JSONL.
//
// RebuildIndex PRESERVES both in place (it rebuilds the derived columns
// without wiping primary state), and prunes agent_edges that reference
// threads no longer present. A total loss of state.db loses primary state
// by design — back up state.db, not just the JSONL. (This replaced an
// earlier <thread_id>.archived sidecar file — a fragile third on-disk
// format; review PR #38.)
const schemaDDL = `
CREATE TABLE IF NOT EXISTS threads (
	id             TEXT PRIMARY KEY,
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL,
	identity_fp    TEXT NOT NULL,
	identity_name  TEXT,
	profile        TEXT NOT NULL,
	working_dir    TEXT NOT NULL,
	project_root   TEXT,
	title          TEXT,
	archived       INTEGER NOT NULL DEFAULT 0,
	parent_thread  TEXT,
	fork_of_thread TEXT,
	fork_of_seq    INTEGER,
	last_seq       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_threads_working_dir  ON threads(working_dir);
CREATE INDEX IF NOT EXISTS idx_threads_project_root ON threads(project_root);
CREATE INDEX IF NOT EXISTS idx_threads_updated_at   ON threads(updated_at);
CREATE INDEX IF NOT EXISTS idx_threads_identity_fp  ON threads(identity_fp);

CREATE TABLE IF NOT EXISTS agent_edges (
	parent_thread TEXT NOT NULL,
	child_thread  TEXT NOT NULL,
	status        TEXT NOT NULL,
	PRIMARY KEY (parent_thread, child_thread)
);

CREATE TABLE IF NOT EXISTS items_fts (
	thread_id TEXT NOT NULL,
	seq       INTEGER NOT NULL,
	text      TEXT NOT NULL,
	PRIMARY KEY (thread_id, seq)
);
`

// mirrorRow is the threads-table row shape.
type mirrorRow struct {
	ID           string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	IdentityFP   string
	IdentityName string
	Profile      string
	WorkingDir   string
	ProjectRoot  string
	Title        string
	Archived     bool
	ParentThread string
	ForkOfThread string
	ForkOfSeq    sql.NullInt64
	LastSeq      int64
}

func (r mirrorRow) toMeta() contracts.ThreadMeta {
	m := contracts.ThreadMeta{
		ThreadID:     r.ID,
		CreatedAt:    r.CreatedAt,
		IdentityFP:   r.IdentityFP,
		IdentityName: r.IdentityName,
		Profile:      r.Profile,
		WorkingDir:   r.WorkingDir,
		ProjectRoot:  r.ProjectRoot,
		ParentThread: r.ParentThread,
		Title:        r.Title,
	}
	if r.ForkOfThread != "" && r.ForkOfSeq.Valid {
		m.ForkOf = &contracts.ForkRef{ThreadID: r.ForkOfThread, Seq: r.ForkOfSeq.Int64}
	}
	return m
}

const timeLayout = time.RFC3339Nano

func openMirror(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("persistence: open mirror: %w", err)
	}
	// modernc.org/sqlite is a single-connection-safe pure-Go driver; force a
	// single connection so our own mutex is the only serialization we need
	// to reason about (avoids sqlite "database is locked" under -race).
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaDDL); err != nil {
		db.Close()
		return nil, fmt.Errorf("persistence: apply schema: %w", err)
	}
	return db, nil
}

func insertThread(db *sql.DB, meta contracts.ThreadMeta) error {
	var forkThread any
	var forkSeq any
	if meta.ForkOf != nil {
		forkThread = meta.ForkOf.ThreadID
		forkSeq = meta.ForkOf.Seq
	}
	lastSeq := int64(0)
	if meta.ForkOf != nil {
		lastSeq = meta.ForkOf.Seq
	}
	_, err := db.Exec(`
		INSERT INTO threads
			(id, created_at, updated_at, identity_fp, identity_name, profile,
			 working_dir, project_root, title, archived, parent_thread,
			 fork_of_thread, fork_of_seq, last_seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?)`,
		meta.ThreadID, meta.CreatedAt.UTC().Format(timeLayout), meta.CreatedAt.UTC().Format(timeLayout),
		meta.IdentityFP, meta.IdentityName, meta.Profile, meta.WorkingDir, meta.ProjectRoot,
		meta.Title, meta.ParentThread, forkThread, forkSeq, lastSeq)
	if err != nil {
		return fmt.Errorf("persistence: insert thread mirror row: %w", err)
	}
	return nil
}

func getThread(db *sql.DB, threadID string) (mirrorRow, error) {
	row := db.QueryRow(`
		SELECT id, created_at, updated_at, identity_fp, identity_name, profile,
		       working_dir, project_root, title, archived, parent_thread,
		       fork_of_thread, fork_of_seq, last_seq
		FROM threads WHERE id = ?`, threadID)
	r, err := scanThreadRow(row)
	if err == sql.ErrNoRows {
		return mirrorRow{}, fmt.Errorf("%w: %s", ErrNotFound, threadID)
	}
	if err != nil {
		return mirrorRow{}, fmt.Errorf("persistence: query thread: %w", err)
	}
	return r, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanThreadRow(row rowScanner) (mirrorRow, error) {
	var r mirrorRow
	var createdAt, updatedAt string
	var identityName, projectRoot, title, parentThread, forkThread sql.NullString
	var archived int
	if err := row.Scan(&r.ID, &createdAt, &updatedAt, &r.IdentityFP, &identityName,
		&r.Profile, &r.WorkingDir, &projectRoot, &title, &archived, &parentThread,
		&forkThread, &r.ForkOfSeq, &r.LastSeq); err != nil {
		return mirrorRow{}, err
	}
	var err error
	if r.CreatedAt, err = time.Parse(timeLayout, createdAt); err != nil {
		return mirrorRow{}, fmt.Errorf("persistence: parse created_at: %w", err)
	}
	if r.UpdatedAt, err = time.Parse(timeLayout, updatedAt); err != nil {
		return mirrorRow{}, fmt.Errorf("persistence: parse updated_at: %w", err)
	}
	r.IdentityName = identityName.String
	r.ProjectRoot = projectRoot.String
	r.Title = title.String
	r.ParentThread = parentThread.String
	r.ForkOfThread = forkThread.String
	r.Archived = archived != 0
	return r, nil
}

// updateThreadAfterAppend advances last_seq/updated_at and, when items
// contain a wd_changed item, the mirrored working_dir/project_root — "the
// latest wins at resume" (spec §1 line 9).
func updateThreadAfterAppend(db *sql.DB, threadID string, newLastSeq int64, updatedAt time.Time, wd, root *string) error {
	if wd != nil {
		_, err := db.Exec(`UPDATE threads SET last_seq = ?, updated_at = ?, working_dir = ?, project_root = ? WHERE id = ?`,
			newLastSeq, updatedAt.UTC().Format(timeLayout), *wd, valueOrEmpty(root), threadID)
		if err != nil {
			return fmt.Errorf("persistence: update thread mirror (wd): %w", err)
		}
		return nil
	}
	_, err := db.Exec(`UPDATE threads SET last_seq = ?, updated_at = ? WHERE id = ?`,
		newLastSeq, updatedAt.UTC().Format(timeLayout), threadID)
	if err != nil {
		return fmt.Errorf("persistence: update thread mirror: %w", err)
	}
	return nil
}

func valueOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func setArchived(db *sql.DB, threadID string, archived bool) error {
	v := 0
	if archived {
		v = 1
	}
	res, err := db.Exec(`UPDATE threads SET archived = ? WHERE id = ?`, v, threadID)
	if err != nil {
		return fmt.Errorf("persistence: set archived: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, threadID)
	}
	return nil
}

func deleteThreadRow(db *sql.DB, threadID string) error {
	if _, err := db.Exec(`DELETE FROM threads WHERE id = ?`, threadID); err != nil {
		return fmt.Errorf("persistence: delete thread mirror row: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM agent_edges WHERE parent_thread = ? OR child_thread = ?`, threadID, threadID); err != nil {
		return fmt.Errorf("persistence: delete agent edges: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM items_fts WHERE thread_id = ?`, threadID); err != nil {
		return fmt.Errorf("persistence: delete fts rows: %w", err)
	}
	return nil
}

func indexFTS(db *sql.DB, threadID string, items []contracts.ThreadItem) error {
	for _, it := range items {
		if it.Type != contracts.TIUserMessage && it.Type != contracts.TIAgentMessage {
			continue
		}
		text := extractText(it.Payload)
		if text == "" {
			continue
		}
		if _, err := db.Exec(`INSERT OR REPLACE INTO items_fts (thread_id, seq, text) VALUES (?, ?, ?)`,
			threadID, it.Seq, text); err != nil {
			return fmt.Errorf("persistence: index fts: %w", err)
		}
	}
	return nil
}

// extractText pulls best-effort message text out of an item payload for the
// items_fts lite index. Payload is `any` (contracts/thread.go); it decodes
// either a bare string or an object with a "text" field, and is a no-op for
// anything else (there is no wire contract for message payload shape at
// U3 — the prompt/context units own that; this is a deliberately narrow,
// documented convenience for /resume search, not a full FTS index).
func extractText(payload any) string {
	switch v := payload.(type) {
	case string:
		return v
	case map[string]any:
		if t, ok := v["text"].(string); ok {
			return t
		}
	}
	return ""
}

// escapeLike escapes the LIKE metacharacters %, _ and the escape char
// itself so a Text filter matches its literal string, not as a wildcard
// pattern. Paired with ESCAPE '\' in the query.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// decodeWDChanged returns (workingDir, projectRoot, ok) for a wd_changed
// item's payload.
func decodeWDChanged(payload any) (string, string, bool) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", "", false
	}
	var p wdChangedPayload
	if err := json.Unmarshal(b, &p); err != nil || p.WorkingDir == "" {
		return "", "", false
	}
	return p.WorkingDir, p.ProjectRoot, true
}

func listThreads(db *sql.DB, f contracts.ListFilter) ([]contracts.ThreadMeta, error) {
	q := strings.Builder{}
	q.WriteString(`SELECT id, created_at, updated_at, identity_fp, identity_name, profile,
		working_dir, project_root, title, archived, parent_thread, fork_of_thread, fork_of_seq, last_seq
		FROM threads WHERE 1=1`)
	var args []any
	if f.WorkingDir != "" {
		q.WriteString(" AND working_dir = ?")
		args = append(args, f.WorkingDir)
	}
	if f.ProjectRoot != "" {
		q.WriteString(" AND project_root = ?")
		args = append(args, f.ProjectRoot)
	}
	if f.IdentityFP != "" {
		q.WriteString(" AND identity_fp = ?")
		args = append(args, f.IdentityFP)
	}
	if f.Archived != nil {
		q.WriteString(" AND archived = ?")
		if *f.Archived {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if f.Text != "" {
		q.WriteString(` AND id IN (SELECT DISTINCT thread_id FROM items_fts WHERE text LIKE ? ESCAPE '\')`)
		args = append(args, "%"+escapeLike(f.Text)+"%")
	}
	// Deterministic order: recency first, then id as a stable tie-break so
	// two threads with equal updated_at sort identically to MemStore.
	q.WriteString(" ORDER BY updated_at DESC, id ASC")

	rows, err := db.Query(q.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("persistence: list threads: %w", err)
	}
	defer rows.Close()

	var out []contracts.ThreadMeta
	for rows.Next() {
		r, err := scanThreadRow(rows)
		if err != nil {
			return nil, fmt.Errorf("persistence: scan listed thread: %w", err)
		}
		out = append(out, r.toMeta())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("persistence: list threads rows: %w", err)
	}
	return out, nil
}

// RebuildIndex regenerates the SQLite mirror entirely from the JSONL
// source of truth, per spec §2 ("always derivable from the JSONL"). It is
// exported on *LocalStore (see local.go); this is the walk+parse core.
//
// Rebuild-derived fields: everything in the threads table except archived,
// preserving PRIMARY state (archived, agent_edges — see the schemaDDL doc
// comment). A single corrupt/unreadable thread file is skipped (logged via
// the returned skipped list), not fatal — rebuild is the recovery path and
// must not be defeated by one bad file. Returns the count of skipped files.
func rebuildIndex(root string, db *sql.DB) (skipped []string, err error) {
	// Snapshot PRIMARY state that is NOT derivable from the JSONL, so it
	// survives the rebuild of the derived columns.
	archivedIDs, err := archivedThreadIDs(db)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(`DELETE FROM threads`); err != nil {
		return nil, fmt.Errorf("persistence: rebuild: clear threads: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM items_fts`); err != nil {
		return nil, fmt.Errorf("persistence: rebuild: clear fts: %w", err)
	}

	threadsRoot := filepath.Join(root, "threads")
	entries, err := os.ReadDir(threadsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("persistence: rebuild: read threads root: %w", err)
	}

	present := map[string]bool{}
	for _, monthEnt := range entries {
		if !monthEnt.IsDir() {
			continue
		}
		monthPath := filepath.Join(threadsRoot, monthEnt.Name())
		files, err := os.ReadDir(monthPath)
		if err != nil {
			return nil, fmt.Errorf("persistence: rebuild: read month dir: %w", err)
		}
		for _, fEnt := range files {
			if fEnt.IsDir() || filepath.Ext(fEnt.Name()) != ".jsonl" {
				continue
			}
			threadID := strings.TrimSuffix(fEnt.Name(), ".jsonl")
			path := filepath.Join(monthPath, fEnt.Name())
			meta, items, err := readThreadFile(path)
			if err != nil {
				// Recovery must not be defeated by one bad file. Skip + record.
				skipped = append(skipped, threadID)
				continue
			}
			if err := insertThread(db, meta); err != nil {
				return skipped, err
			}
			present[threadID] = true
			lastSeq := int64(0)
			if meta.ForkOf != nil {
				lastSeq = meta.ForkOf.Seq
			}
			updatedAt := meta.CreatedAt
			var wdPtr, rootPtr *string
			for _, it := range items {
				if it.Seq > lastSeq {
					lastSeq = it.Seq
				}
				if it.TS.After(updatedAt) {
					updatedAt = it.TS
				}
				if it.Type == contracts.TIWDChanged {
					if wd, pr, ok := decodeWDChanged(it.Payload); ok {
						wdPtr = &wd
						rootPtr = &pr
					}
				}
			}
			if err := updateThreadAfterAppend(db, threadID, lastSeq, updatedAt, wdPtr, rootPtr); err != nil {
				return skipped, err
			}
			if err := indexFTS(db, threadID, items); err != nil {
				return skipped, err
			}
			// Restore PRIMARY archived state for threads that still exist.
			if archivedIDs[threadID] {
				if err := setArchived(db, threadID, true); err != nil {
					return skipped, err
				}
			}
		}
	}

	// Prune agent_edges referencing threads that no longer exist (idempotency
	// of the primary edge table across rebuilds).
	if _, err := db.Exec(`DELETE FROM agent_edges
		WHERE parent_thread NOT IN (SELECT id FROM threads)
		   OR child_thread  NOT IN (SELECT id FROM threads)`); err != nil {
		return skipped, fmt.Errorf("persistence: rebuild: prune agent edges: %w", err)
	}
	return skipped, nil
}

// archivedThreadIDs returns the set of thread ids currently flagged
// archived — PRIMARY state snapshotted before a rebuild clears the table.
func archivedThreadIDs(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`SELECT id FROM threads WHERE archived = 1`)
	if err != nil {
		return nil, fmt.Errorf("persistence: snapshot archived: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
