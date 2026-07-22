package toolrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"github.com/CarriedWorldUniverse/agora/internal/memory"
)

// memoryToolNames is the set MemoryFamily.Handles checks against.
var memoryToolNames = map[string]bool{
	contracts.ToolMemoryRead:   true,
	contracts.ToolMemoryWrite:  true,
	contracts.ToolMemoryList:   true,
	contracts.ToolMemoryDelete: true,
}

// MemoryFamily is the memory.* native tool family (agora-spec-memory.md
// §3): model-facing read/write/list/delete over one identity's memory
// store (internal/memory.Store). It is deliberately NOT constrained by a
// session's fs Roots the way FSFamily is — the memory dir sits outside the
// wd sandbox write scope by design (§3: "Write outside the memory dir via
// these tools = impossible by construction ... the family carries its own
// grant"): containment here is "only ever touches dir", enforced by
// internal/memory.Store itself (validateSlug + entryPath join), not by
// toolrunner's roots machinery.
type MemoryFamily struct {
	dir string
}

// NewMemoryFamily builds the memory family rooted at dir (typically
// memory.DefaultDir(home, identity) — see turnengine's defaultMemoryDir).
// dir is opened lazily: memory.NewStore(dir) — which creates dir if
// absent — is only called from inside a handler, when the model actually
// invokes a memory.* tool, so merely constructing a Family/Surface/Manager
// never touches disk.
func NewMemoryFamily(dir string) *MemoryFamily {
	return &MemoryFamily{dir: dir}
}

func (f *MemoryFamily) Name() string { return contracts.FamilyMemory }

func (f *MemoryFamily) Handles(name string) bool { return memoryToolNames[name] }

func (f *MemoryFamily) Specs() []contracts.ToolSpec {
	return []contracts.ToolSpec{
		{
			Name:        contracts.ToolMemoryRead,
			Description: "Read one saved memory's frontmatter + body by name.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "The memory's slug (filename stem, without .md)."},
				},
				"required": []string{"name"},
			}),
		},
		{
			Name:        contracts.ToolMemoryWrite,
			Description: "Save (create or overwrite) a memory: a one-fact note, automatically re-indexed in MEMORY.md.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "The memory's slug (filename stem, without .md)."},
					"frontmatter": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":        map[string]any{"type": "string", "description": "Display title, shown as the index link text."},
							"description": map[string]any{"type": "string", "description": "One-line hook, shown in the index after the em dash."},
							"type":        map[string]any{"type": "string", "description": "One of user|feedback|project|reference."},
						},
						"required": []string{"name", "description", "type"},
					},
					"body": map[string]any{"type": "string", "description": "The fact content (+ why/how-to-apply for feedback/project memories)."},
				},
				"required": []string{"name", "frontmatter", "body"},
			}),
		},
		{
			Name:        contracts.ToolMemoryList,
			Description: "List every saved memory (slug, title, type, hook), newest-first.",
			InputSchema: mustSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}),
		},
		{
			Name:        contracts.ToolMemoryDelete,
			Description: "Delete a saved memory by name.",
			InputSchema: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "The memory's slug (filename stem, without .md)."},
				},
				"required": []string{"name"},
			}),
		},
	}
}

func (f *MemoryFamily) Execute(ctx context.Context, call Call) (Result, error) {
	switch call.Name {
	case contracts.ToolMemoryRead:
		return f.read(call.Args), nil
	case contracts.ToolMemoryWrite:
		return f.write(call.Args), nil
	case contracts.ToolMemoryList:
		return f.list(call.Args), nil
	case contracts.ToolMemoryDelete:
		return f.delete(call.Args), nil
	default:
		return errorResult(fmt.Errorf("%w: %s", ErrUnknownTool, call.Name)), nil
	}
}

// openStore opens (creating dir if absent, per memory.NewStore's contract)
// this family's store. See NewMemoryFamily's doc comment for why this is
// deferred to call time rather than cached at construction.
func (f *MemoryFamily) openStore() (*memory.Store, error) {
	store, err := memory.NewStore(f.dir)
	if err != nil {
		return nil, fmt.Errorf("toolrunner: open memory store: %w", err)
	}
	return store, nil
}

// --- memory.read ---

type memoryReadArgs struct {
	Name string `json:"name"`
}

func (f *MemoryFamily) read(raw json.RawMessage) Result {
	var a memoryReadArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Name == "" {
		return errorResult(fmt.Errorf("%w: memory.read", ErrBadArgs))
	}
	store, err := f.openStore()
	if err != nil {
		return errorResult(err)
	}
	entry, err := store.Read(a.Name)
	if err != nil {
		return errorResult(err)
	}
	content := fmt.Sprintf("---\nname: %s\ndescription: %s\ntype: %s\n---\n\n%s",
		entry.Frontmatter.Name, entry.Frontmatter.Description, entry.Frontmatter.Type, entry.Body)
	return Result{Content: content}
}

// --- memory.write ---

type memoryWriteArgs struct {
	Name        string `json:"name"`
	Frontmatter struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Type        string `json:"type"`
	} `json:"frontmatter"`
	Body string `json:"body"`
}

func (f *MemoryFamily) write(raw json.RawMessage) Result {
	var a memoryWriteArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Name == "" {
		return errorResult(fmt.Errorf("%w: memory.write", ErrBadArgs))
	}
	store, err := f.openStore()
	if err != nil {
		return errorResult(err)
	}
	fm := memory.Frontmatter{
		Name:        a.Frontmatter.Name,
		Description: a.Frontmatter.Description,
		Type:        memory.Type(a.Frontmatter.Type),
	}
	if err := store.Write(a.Name, fm, a.Body); err != nil {
		return errorResult(err)
	}
	return Result{Content: "saved memory " + a.Name}
}

// --- memory.list ---

func (f *MemoryFamily) list(raw json.RawMessage) Result {
	store, err := f.openStore()
	if err != nil {
		return errorResult(err)
	}
	entries, err := store.List()
	if err != nil {
		return errorResult(err)
	}
	var sb strings.Builder
	for _, e := range entries {
		sb.WriteString(fmt.Sprintf("%s\t%s\t%s\t%s\n", e.Slug, e.Title, e.Type, e.Hook))
	}
	return Result{Content: sb.String()}
}

// --- memory.delete ---

type memoryDeleteArgs struct {
	Name string `json:"name"`
}

func (f *MemoryFamily) delete(raw json.RawMessage) Result {
	var a memoryDeleteArgs
	if err := json.Unmarshal(raw, &a); err != nil || a.Name == "" {
		return errorResult(fmt.Errorf("%w: memory.delete", ErrBadArgs))
	}
	store, err := f.openStore()
	if err != nil {
		return errorResult(err)
	}
	if err := store.Delete(a.Name); err != nil {
		return errorResult(err)
	}
	return Result{Content: "deleted memory " + a.Name}
}
