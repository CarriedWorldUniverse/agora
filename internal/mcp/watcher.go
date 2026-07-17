package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// WatchMode is the §5a `context.fs_watch` config value.
type WatchMode string

const (
	WatchNotify WatchMode = "notify"
	WatchSweep  WatchMode = "sweep"
	WatchOff    WatchMode = "off"
)

// IgnoredDirs are the protected dirs whose internal churn is never a
// model-read artifact (§5a: ".git", ".agora", ".cairn").
var IgnoredDirs = []string{".git", ".agora", ".cairn"}

func isIgnoredDir(name string) bool {
	for _, d := range IgnoredDirs {
		if name == d {
			return true
		}
	}
	return false
}

// fileState is the sweep's last-known state for one path.
type fileState struct {
	modTime time.Time
}

// Sweeper is the §5a mtime-sweep fallback fs-watcher: a periodic poll of
// the writable roots, run between turns, that detects change by mtime
// (cheap, no content read on the common path) rather than content hash —
// which is exactly why it "over-invalidates" per spec: a touch that
// changes mtime without changing bytes still reports modified. That is the
// documented safe direction (worst case an unneeded re-read, never stale-
// content-as-truth), not a bug.
//
// This unit implements the sweep fallback only, per the ground rules'
// injectable-clock/no-wall-clock-sleep testability requirement and the
// "no fsnotify dep unless truly needed" steer: a real OS-notify primary
// watcher (inotify/FSEvents/ReadDirectoryChangesW) is deferred to whichever
// unit wires WatchNotify to a real backend — Sweeper alone already
// satisfies WatchMode "sweep" and the "off" fallback the spec names, and is
// what every consumer (§5a's two) can be built and tested against today.
type Sweeper struct {
	roots []string
	clock Clock

	mu     sync.Mutex
	states map[string]fileState
}

// NewSweeper builds a sweeper over roots (the session's working dir +
// declared add_dir roots, §5a "Scope"). A nil clock uses SystemClock.
//
// roots are assumed to be real directories, not symlinks — a symlinked
// root is not resolved/validated here, so a root that is (or becomes) a
// symlink can walk unexpected targets. (LOW, review finding, not
// implemented — caller must pass resolved paths.)
func NewSweeper(roots []string, clock Clock) *Sweeper {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Sweeper{roots: roots, clock: clock, states: make(map[string]fileState)}
}

// Sweep walks the roots once and returns the FSChange events for every
// path whose mtime differs from (or is new since) the last sweep, plus a
// deleted event for every previously-seen path no longer present. First
// call establishes the baseline and reports every file as `created` — a
// consumer typically discards the first sweep's events as "pre-existing",
// or treats them as the initial known-state seed; both are valid callers'
// choices, Sweep just reports what it observed.
func (w *Sweeper) Sweep() ([]contracts.FSChange, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.clock.Now()
	seen := make(map[string]bool)
	var changes []contracts.FSChange

	for _, root := range w.roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if path != root && isIgnoredDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			// Never follow symlinks out of the sandbox root (§5a "Bound"):
			// a symlink dir entry is reported neither as content nor
			// traversed into.
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}
			seen[path] = true
			prev, known := w.states[path]
			mt := info.ModTime()
			if !known {
				w.states[path] = fileState{modTime: mt}
				changes = append(changes, contracts.FSChange{
					Path: path, Kind: "created", At: now, ContentHash: hashFile(path),
				})
				return nil
			}
			if !mt.Equal(prev.modTime) {
				w.states[path] = fileState{modTime: mt}
				changes = append(changes, contracts.FSChange{
					Path: path, Kind: "modified", At: now, ContentHash: hashFile(path),
				})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// Deleted: previously known paths under any root not seen this sweep.
	var deletedPaths []string
	for p := range w.states {
		if !seen[p] && w.underRoots(p) {
			deletedPaths = append(deletedPaths, p)
		}
	}
	sort.Strings(deletedPaths)
	for _, p := range deletedPaths {
		delete(w.states, p)
		changes = append(changes, contracts.FSChange{Path: p, Kind: "deleted", At: now})
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func (w *Sweeper) underRoots(p string) bool {
	for _, r := range w.roots {
		if p == r || pathHasPrefix(p, r) {
			return true
		}
	}
	return false
}

func pathHasPrefix(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !hasDotDotPrefix(rel)
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 2 && rel[0] == '.' && rel[1] == '.'
}

func hashFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
