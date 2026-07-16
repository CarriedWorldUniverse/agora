package persistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// maxLineBytes caps a single JSONL line on read. A line larger than this
// aborts only THAT thread's read (documented limit); item payloads are
// size-capped upstream by the context-curation layer, so this is a
// backstop, not the primary bound.
const maxLineBytes = 1 << 24 // 16 MiB

// createThreadFile writes a brand-new thread file with meta as line 1.
// Fails if the file already exists (O_EXCL) — Create must not clobber an
// existing thread. Spec §1: "Line 1 = meta ... never rewritten." The meta
// line is always terminated with a newline and fsynced, so a healthy file
// always contains at least one complete line (relied on by healTornTail).
func createThreadFile(path string, meta contracts.ThreadMeta) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("persistence: create thread file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	if err := enc.Encode(meta); err != nil {
		return fmt.Errorf("persistence: encode meta: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("persistence: fsync meta: %w", err)
	}
	return nil
}

// readThreadFile reads line 1 (meta) and all subsequent item lines from a
// thread's JSONL file, in file order (= Seq order, since Append is
// append-only).
//
// Crash tolerance (spec §1 crash-safety + §2 "corruption is an
// inconvenience, never data loss"): a TORN TRAILING line — a partial final
// line from a crash mid-append — is skipped, and every fully-written prior
// item is returned intact. A decode failure on a NON-final line is a real
// mid-file corruption and is a hard error (it cannot be a torn write, which
// only ever affects the tail). A torn meta line (line 1) is a hard error —
// the thread is genuinely unreadable — but createThreadFile fsyncs the meta
// line, so that requires losing an fsynced write, not a torn append.
func readThreadFile(path string) (contracts.ThreadMeta, []contracts.ThreadItem, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: open thread file: %w", err)
	}
	if len(data) == 0 {
		return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: empty thread file %s", path)
	}
	lines := bytes.Split(data, []byte{'\n'})
	// bytes.Split always leaves a trailing element after the final '\n':
	// empty when the file ends cleanly, or the torn partial final line when a
	// crash left no terminating newline. Either way it is NOT a complete item
	// line — drop it. This is the entirety of torn-tail tolerance: every line
	// that remains is complete, so any later decode failure is genuine
	// mid-file corruption, not a torn write.
	if n := len(lines); n > 0 {
		lines = lines[:n-1]
	}
	if len(lines) == 0 {
		return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: no complete lines in %s", path)
	}
	if len(lines[0]) > maxLineBytes {
		return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: meta line exceeds %d bytes in %s", maxLineBytes, path)
	}
	var meta contracts.ThreadMeta
	if err := json.Unmarshal(lines[0], &meta); err != nil {
		return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: decode meta: %w", err)
	}

	items := make([]contracts.ThreadItem, 0, len(lines)-1)
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if len(line) == 0 {
			continue
		}
		if len(line) > maxLineBytes {
			return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: item line %d exceeds %d bytes in %s", i, maxLineBytes, path)
		}
		var it contracts.ThreadItem
		if err := json.Unmarshal(line, &it); err != nil {
			// The torn trailing line was already dropped above; every line
			// here is complete, so a decode failure is real mid-file
			// corruption (bit-rot / a bug), never a torn write — hard error.
			return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: decode item line %d: %w", i, err)
		}
		items = append(items, it)
	}
	return meta, items, nil
}

// healTornTail truncates a torn partial final line (no trailing newline)
// left by a crash mid-append, so the next append starts on a clean line
// boundary rather than gluing onto the fragment. The meta line always ends
// in a fsynced newline (createThreadFile), so a healthy or crashed file
// always contains at least one newline to truncate back to.
func healTornTail(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("persistence: stat for heal: %w", err)
	}
	size := fi.Size()
	if size == 0 {
		return nil
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, size-1); err != nil {
		return fmt.Errorf("persistence: read tail byte: %w", err)
	}
	if last[0] == '\n' {
		return nil // clean boundary
	}
	// Torn tail: find the last newline and truncate just past it.
	data := make([]byte, size)
	if _, err := f.ReadAt(data, 0); err != nil {
		return fmt.Errorf("persistence: read for heal: %w", err)
	}
	idx := bytes.LastIndexByte(data, '\n')
	if idx < 0 {
		// No complete line at all — should be impossible (meta line is
		// fsynced with its newline). Refuse rather than truncate to 0.
		return fmt.Errorf("persistence: torn file with no complete line")
	}
	if err := f.Truncate(int64(idx + 1)); err != nil {
		return fmt.Errorf("persistence: truncate torn tail: %w", err)
	}
	return nil
}

// appendItems appends items to an existing thread file, one JSON line per
// item, honoring the fsync mode. Before writing it HEALS any torn trailing
// line from a prior crash (spec §1 crash-safety) so items never glue onto a
// partial fragment. Spec §1: "append + fsync on turn boundaries (config:
// per-item for paranoid mode)."
func appendItems(path string, items []contracts.ThreadItem, mode FsyncMode) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("persistence: open thread file for append: %w", err)
	}
	defer f.Close()

	if err := healTornTail(f); err != nil {
		return err
	}
	if _, err := f.Seek(0, os.SEEK_END); err != nil {
		return fmt.Errorf("persistence: seek to end: %w", err)
	}

	enc := json.NewEncoder(f)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return fmt.Errorf("persistence: encode item: %w", err)
		}
		if mode == FsyncItem {
			if err := f.Sync(); err != nil {
				return fmt.Errorf("persistence: fsync item: %w", err)
			}
		}
	}
	if mode == FsyncTurn {
		if err := f.Sync(); err != nil {
			return fmt.Errorf("persistence: fsync turn: %w", err)
		}
	}
	return nil
}
