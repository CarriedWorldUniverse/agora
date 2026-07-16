package persistence

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// createThreadFile writes a brand-new thread file with meta as line 1.
// Fails if the file already exists (O_EXCL) — Create must not clobber an
// existing thread. Spec §1: "Line 1 = meta ... never rewritten."
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

// readThreadFile reads line 1 (meta) and all subsequent lines (items, in
// file order — already Seq order since Append is append-only) from a
// thread's JSONL file.
func readThreadFile(path string) (contracts.ThreadMeta, []contracts.ThreadItem, error) {
	f, err := os.Open(path)
	if err != nil {
		return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: open thread file: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<24)

	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: read meta line: %w", err)
		}
		return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: empty thread file %s", path)
	}
	var meta contracts.ThreadMeta
	if err := json.Unmarshal(sc.Bytes(), &meta); err != nil {
		return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: decode meta: %w", err)
	}

	var items []contracts.ThreadItem
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var it contracts.ThreadItem
		if err := json.Unmarshal(line, &it); err != nil {
			return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: decode item: %w", err)
		}
		items = append(items, it)
	}
	if err := sc.Err(); err != nil {
		return contracts.ThreadMeta{}, nil, fmt.Errorf("persistence: scan items: %w", err)
	}
	return meta, items, nil
}

// appendItems appends items to an existing thread file, one JSON line per
// item, honoring the fsync mode. Spec §1: "append + fsync on turn
// boundaries (config: per-item for paranoid mode)."
func appendItems(path string, items []contracts.ThreadItem, mode FsyncMode) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("persistence: open thread file for append: %w", err)
	}
	defer f.Close()

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
