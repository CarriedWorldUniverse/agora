package memory

import "time"

// Type is a memory file's frontmatter `type`. Spec §1: "frontmatter
// {name, description, type: user|feedback|project|reference}".
type Type string

const (
	TypeUser      Type = "user"
	TypeFeedback  Type = "feedback"
	TypeProject   Type = "project"
	TypeReference Type = "reference"
)

// Valid reports whether t is one of the four allowed frontmatter types.
func (t Type) Valid() bool {
	switch t {
	case TypeUser, TypeFeedback, TypeProject, TypeReference:
		return true
	default:
		return false
	}
}

// Frontmatter is a memory file's `{name, description, type}` header.
// Spec §1.
type Frontmatter struct {
	// Name is the human title shown as the index link text.
	Name string `yaml:"name"`
	// Description is the fact's one-line hook, shown in the index after
	// the em dash (§1: "one line per memory ... — hook").
	Description string `yaml:"description"`
	// Type classifies the memory; must be Valid().
	Type Type `yaml:"type"`
}

// Entry is one fully-read `<slug>.md` memory file.
type Entry struct {
	// Slug is the filename stem (the `name` a memory.* tool call passes),
	// e.g. "slug" for "slug.md".
	Slug        string
	Frontmatter Frontmatter
	// Body is the fact content following the frontmatter block (+ why/
	// how-to-apply for feedback/project memories, per §1).
	Body string
}

// IndexEntry is one MEMORY.md index line's parsed/derivable content —
// enough to render `- [title](file.md) — hook` and to order the index
// newest-first for §2 budget truncation.
type IndexEntry struct {
	Slug  string
	Title string
	Hook  string
	Type  Type
	// ModTime is the backing file's modification time, used to order the
	// index newest-first (§2: "truncate whole lines, newest-first
	// survives"). Store derives this from the filesystem; callers building
	// IndexEntry values directly (e.g. tests) set it explicitly rather than
	// relying on wall-clock time.
	ModTime time.Time
}

// File is the on-disk filename for slug ("<slug>.md").
func (e IndexEntry) File() string {
	return e.Slug + ".md"
}
