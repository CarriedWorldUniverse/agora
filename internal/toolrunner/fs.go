package toolrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// Tool names for the fs family.
const (
	ToolReadFile  = "read_file"
	ToolWriteFile = "write_file"
	ToolEditFile  = "edit_file"
	ToolListDir   = "list_dir"
	ToolGlob      = "glob"
	ToolGrep      = "grep"
)

// fsMaxFileSize bounds read_file/grep against a huge file blowing out the
// model's context or the process's memory (review fix 4).
const fsMaxFileSize = 10 << 20 // 10 MiB

// fsToolNames is the set fsFamily.Handles checks against.
var fsToolNames = map[string]bool{
	ToolReadFile:  true,
	ToolWriteFile: true,
	ToolEditFile:  true,
	ToolListDir:   true,
	ToolGlob:      true,
	ToolGrep:      true,
}

// FSFamily is the fs native tool family (agora-spec-mcp.md §5a): model-
// facing read/write/edit/list/glob/grep tools, mirroring the codex/
// Claude-Code convention, with HARD path containment (roots.go) and a v1
// read-before-write staleness guard (in-memory, per-path content hash —
// Phase 3: replace with the curation-ledger's per-key content hash,
// agora-spec-context-curation.md §2, once that ledger exists).
type FSFamily struct {
	roots Roots

	mu       sync.Mutex
	readHash map[string]string      // resolved absolute path -> sha256 hex of content at last read
	pathLock map[string]*sync.Mutex // resolved absolute path -> lock over its whole read+checkStale+write sequence
}

// NewFSFamily builds the fs family over roots.
func NewFSFamily(roots Roots) *FSFamily {
	return &FSFamily{
		roots:    roots,
		readHash: make(map[string]string),
		pathLock: make(map[string]*sync.Mutex),
	}
}

// lockPath returns (lazily creating) the mutex serializing one resolved
// path's whole read+checkStale+write sequence. Review fix 3: without this,
// two concurrent write_file/edit_file calls to the same path can each
// independently os.ReadFile + checkStale against the same on-disk
// snapshot and then race two unsynchronized os.WriteFile calls against
// each other — not just "last write wins" but an actual torn/corrupted
// file (confirmed empirically: interleaved writes of different lengths
// produce neither attempted content, e.g. "v2"+"v11" landing as "v21").
func (f *FSFamily) lockPath(resolved string) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.pathLock[resolved]
	if !ok {
		l = &sync.Mutex{}
		f.pathLock[resolved] = l
	}
	return l
}

func (f *FSFamily) Name() string { return contracts.FamilyFS }

func (f *FSFamily) Handles(name string) bool { return fsToolNames[name] }

func (f *FSFamily) Specs() []contracts.ToolSpec {
	return []contracts.ToolSpec{
		{
			Name:        ToolReadFile,
			Description: "Read a text file's contents, optionally a line range.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "description": "File path, relative to the working dir or absolute inside a writable root."},
					"offset": map[string]any{"type": "integer", "description": "0-based line number to start from (default 0)."},
					"limit":  map[string]any{"type": "integer", "description": "Max number of lines to return (default: all remaining)."},
				},
				"required": []string{"path"},
			}),
		},
		{
			Name:        ToolWriteFile,
			Description: "Write (overwrite or create) a file's full contents. If the file already exists it must have been read this session first (staleness guard).",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []string{"path", "content"},
			}),
		},
		{
			Name:        ToolEditFile,
			Description: "Replace an exact substring in a file that has been read this session.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":        map[string]any{"type": "string"},
					"old_string":  map[string]any{"type": "string"},
					"new_string":  map[string]any{"type": "string"},
					"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence instead of requiring exactly one (default false)."},
				},
				"required": []string{"path", "old_string", "new_string"},
			}),
		},
		{
			Name:        ToolListDir,
			Description: "List a directory's immediate entries (name + is_dir).",
			InputSchema: mustSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			}),
		},
		{
			Name:        ToolGlob,
			Description: "Find files under the writable roots matching a glob pattern (supports ** for recursive match).",
			InputSchema: mustSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{"pattern": map[string]any{"type": "string"}},
				"required":   []string{"pattern"},
			}),
		},
		{
			Name:        ToolGrep,
			Description: "Search file contents under the writable roots (or one path) for a regexp pattern.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string"},
					"path":    map[string]any{"type": "string", "description": "Restrict the search to this file or directory (default: all writable roots)."},
				},
				"required": []string{"pattern"},
			}),
		},
	}
}

func mustSchema(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // static schema literals only — a marshal failure here is a programmer error
	}
	return b
}

func (f *FSFamily) Execute(ctx context.Context, call Call) (Result, error) {
	switch call.Name {
	case ToolReadFile:
		return f.readFile(call.Args), nil
	case ToolWriteFile:
		return f.writeFile(call.Args), nil
	case ToolEditFile:
		return f.editFile(call.Args), nil
	case ToolListDir:
		return f.listDir(call.Args), nil
	case ToolGlob:
		return f.glob(call.Args), nil
	case ToolGrep:
		return f.grep(call.Args), nil
	default:
		return errorResult(fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)), nil
	}
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkFileSize rejects a file over fsMaxFileSize before it's read into
// memory/context (review fix 4). A stat failure is not this check's
// business — the caller's own os.ReadFile/os.Stat will surface it.
func checkFileSize(resolved string) error {
	info, err := os.Stat(resolved)
	if err != nil {
		return nil
	}
	if info.Size() > fsMaxFileSize {
		return ErrFileTooLarge
	}
	return nil
}

// --- read_file ---

type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (f *FSFamily) readFile(raw json.RawMessage) Result {
	var a readFileArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Path == "" {
		return errorResult(fmt.Errorf("%w: read_file", ErrBadArgs))
	}
	resolved, err := resolveContained(f.roots, a.Path)
	if err != nil {
		return errorResult(err)
	}
	if f.roots.IsProtected(resolved) {
		return errorResult(ErrProtectedPath)
	}
	if err := checkFileSize(resolved); err != nil {
		return errorResult(err)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return errorResult(err)
	}

	f.mu.Lock()
	f.readHash[resolved] = hashBytes(data)
	f.mu.Unlock()

	lines := strings.Split(string(data), "\n")
	start := a.Offset
	if start < 0 {
		start = 0
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if a.Limit > 0 && start+a.Limit < end {
		end = start + a.Limit
	}
	return Result{Content: strings.Join(lines[start:end], "\n")}
}

// --- write_file ---

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (f *FSFamily) writeFile(raw json.RawMessage) Result {
	var a writeFileArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Path == "" {
		return errorResult(fmt.Errorf("%w: write_file", ErrBadArgs))
	}
	resolved, err := resolveContained(f.roots, a.Path)
	if err != nil {
		return errorResult(err)
	}
	if f.roots.IsProtected(resolved) {
		return errorResult(ErrProtectedPath)
	}

	// Review fix 3: hold the per-path lock across the WHOLE
	// read+checkStale+write sequence — without it, two concurrent writers
	// can each independently read+checkStale against the same on-disk
	// snapshot and then race unsynchronized os.WriteFile calls, producing
	// a torn/corrupted file rather than a clean serialized outcome.
	pl := f.lockPath(resolved)
	pl.Lock()
	defer pl.Unlock()

	// Staleness guard applies only when the file already exists (a
	// brand-new file has nothing to have gone stale against) — the same
	// convention this harness's own Write tool uses.
	if existing, err := os.ReadFile(resolved); err == nil {
		if err := f.checkStale(resolved, existing); err != nil {
			return errorResult(err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return errorResult(err)
	}
	if err := os.WriteFile(resolved, []byte(a.Content), 0o644); err != nil {
		return errorResult(err)
	}

	f.mu.Lock()
	f.readHash[resolved] = hashBytes([]byte(a.Content))
	f.mu.Unlock()
	return Result{Content: "wrote " + a.Path}
}

// checkStale enforces the read-before-write guard: resolved must have been
// read this session (ErrNotRead) and its recorded hash must match onDisk's
// current content (ErrStale).
func (f *FSFamily) checkStale(resolved string, onDisk []byte) error {
	f.mu.Lock()
	last, ok := f.readHash[resolved]
	f.mu.Unlock()
	if !ok {
		return ErrNotRead
	}
	if last != hashBytes(onDisk) {
		return ErrStale
	}
	return nil
}

// --- edit_file ---

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (f *FSFamily) editFile(raw json.RawMessage) Result {
	var a editFileArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Path == "" {
		return errorResult(fmt.Errorf("%w: edit_file", ErrBadArgs))
	}
	resolved, err := resolveContained(f.roots, a.Path)
	if err != nil {
		return errorResult(err)
	}
	if f.roots.IsProtected(resolved) {
		return errorResult(ErrProtectedPath)
	}

	// Same per-path-lock reasoning as write_file (review fix 3) — applied
	// here too for consistency even though edit_file's substring match
	// incidentally makes a torn write less likely to go unnoticed.
	pl := f.lockPath(resolved)
	pl.Lock()
	defer pl.Unlock()

	data, err := os.ReadFile(resolved)
	if err != nil {
		return errorResult(err)
	}
	if err := f.checkStale(resolved, data); err != nil {
		return errorResult(err)
	}

	content := string(data)
	count := strings.Count(content, a.OldString)
	if count == 0 {
		return errorResult(ErrOldStringNotFound)
	}
	if count > 1 && !a.ReplaceAll {
		return errorResult(ErrOldStringNotUnique)
	}

	var updated string
	if a.ReplaceAll {
		updated = strings.ReplaceAll(content, a.OldString, a.NewString)
	} else {
		updated = strings.Replace(content, a.OldString, a.NewString, 1)
	}

	if err := os.WriteFile(resolved, []byte(updated), 0o644); err != nil {
		return errorResult(err)
	}
	f.mu.Lock()
	f.readHash[resolved] = hashBytes([]byte(updated))
	f.mu.Unlock()
	return Result{Content: "edited " + a.Path}
}

// --- list_dir ---

type listDirArgs struct {
	Path string `json:"path"`
}

func (f *FSFamily) listDir(raw json.RawMessage) Result {
	var a listDirArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Path == "" {
		return errorResult(fmt.Errorf("%w: list_dir", ErrBadArgs))
	}
	resolved, err := resolveContained(f.roots, a.Path)
	if err != nil {
		return errorResult(err)
	}
	if f.roots.IsProtected(resolved) {
		return errorResult(ErrProtectedPath)
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return errorResult(err)
	}
	var lines []string
	for _, e := range entries {
		if isProtectedName(e.Name()) {
			continue
		}
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		lines = append(lines, e.Name()+suffix)
	}
	sort.Strings(lines)
	return Result{Content: strings.Join(lines, "\n")}
}

func isProtectedName(name string) bool {
	for _, p := range ProtectedDirs {
		if name == p {
			return true
		}
	}
	return false
}

// --- glob ---

type globArgs struct {
	Pattern string `json:"pattern"`
}

func (f *FSFamily) glob(raw json.RawMessage) Result {
	var a globArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Pattern == "" {
		return errorResult(fmt.Errorf("%w: glob", ErrBadArgs))
	}
	patParts := strings.Split(a.Pattern, "/")

	var matches []string
	for _, root := range f.roots.DedupedAll() {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // best-effort walk, unreadable entries are skipped
			}
			if d.IsDir() {
				if path != root && isProtectedName(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil // never follow symlinks out (§5a "Bound")
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			nameParts := strings.Split(rel, string(filepath.Separator))
			if matchGlobParts(patParts, nameParts) {
				matches = append(matches, path)
			}
			return nil
		})
	}
	sort.Strings(matches)
	return Result{Content: strings.Join(matches, "\n")}
}

// matchGlobParts matches a slash-split glob pattern against a slash-split
// path, supporting "**" as a whole path segment matching zero or more
// path segments (classic recursive-glob semantics), and any other segment
// via filepath.Match (single-segment wildcards only).
func matchGlobParts(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		if matchGlobParts(pattern[1:], name) {
			return true
		}
		if len(name) == 0 {
			return false
		}
		return matchGlobParts(pattern, name[1:])
	}
	if len(name) == 0 {
		return false
	}
	ok, err := filepath.Match(pattern[0], name[0])
	if err != nil || !ok {
		return false
	}
	return matchGlobParts(pattern[1:], name[1:])
}

// --- grep ---

// grepMaxMatches bounds result size so a broad pattern over a large tree
// can't blow out the model's context.
const grepMaxMatches = 500

type grepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (f *FSFamily) grep(raw json.RawMessage) Result {
	var a grepArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Pattern == "" {
		return errorResult(fmt.Errorf("%w: grep", ErrBadArgs))
	}
	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return errorResult(fmt.Errorf("%w: %v", ErrBadArgs, err))
	}

	var searchRoots []string
	if a.Path != "" {
		resolved, err := resolveContained(f.roots, a.Path)
		if err != nil {
			return errorResult(err)
		}
		// Review fix 2: the existing per-descendant isProtectedName skip
		// below only guards protected dirs found WHILE walking — it never
		// covers the case where the caller-supplied path/) is ITSELF a
		// protected root (grep(path=".git")), since WalkDir's root arg is
		// never checked against isProtectedName (only "path != root"
		// entries are). Check the resolved search root explicitly.
		if f.roots.IsProtected(resolved) {
			return errorResult(ErrProtectedPath)
		}
		searchRoots = []string{resolved}
	} else {
		searchRoots = f.roots.DedupedAll()
	}

	var matches []string
	for _, root := range searchRoots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr
			}
			if len(matches) >= grepMaxMatches {
				return filepath.SkipAll
			}
			if d.IsDir() {
				if path != root && isProtectedName(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if err := checkFileSize(path); err != nil {
				return nil // oversized file: skip silently, same "safe direction" as an unreadable file
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil //nolint:nilerr
			}
			for i, line := range strings.Split(string(data), "\n") {
				if re.MatchString(line) {
					matches = append(matches, fmt.Sprintf("%s:%d:%s", path, i+1, line))
					if len(matches) >= grepMaxMatches {
						break
					}
				}
			}
			return nil
		})
	}
	return Result{Content: strings.Join(matches, "\n")}
}
