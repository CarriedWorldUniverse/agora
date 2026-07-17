package workflow

import (
	"encoding/json"
	"fmt"

	"github.com/CarriedWorldUniverse/agora/contracts"
	"go.starlark.net/starlark"
)

// Phase is one entry of workflow_meta(phases=[...]) — spec §1: "progress
// groups for the UI + per-phase defaults", §2a: "the phase's defaults from
// meta.phases (calls tagged phase="Verify" inherit that phase's
// model/effort)".
type Phase struct {
	Title  string
	Model  string
	Effort contracts.Effort
}

// Meta is the parsed result of a script's workflow_meta(...) call.
// Spec §1.
type Meta struct {
	Name        string
	Description string
	Phases      []Phase
	// ArgsSchema is stored opaque — spec §1 lists it as "optional JSON
	// schema for args validation"; this unit stores it but does not
	// enforce it (no JSON-schema validator dependency in scope — see the
	// build report's deferral note). A future unit can validate Args
	// against it before Run without any change to this shape.
	ArgsSchema json.RawMessage
}

// phase looks up m.Phases by Title for §2a resolution, ok=false if absent.
func (m *Meta) phase(title string) (Phase, bool) {
	if m == nil || title == "" {
		return Phase{}, false
	}
	for _, p := range m.Phases {
		if p.Title == title {
			return p, true
		}
	}
	return Phase{}, false
}

// metaKey is the thread-local key workflowMetaBuiltin stashes the parsed
// Meta under, so Run can read it back after ExecFile without re-decoding a
// starlark Value (see doc.go for why: workflow_meta's return value only
// needs to satisfy "assignable to a script-level variable named meta", not
// round-trip through a Go decoder — the builtin captures the Go-shaped
// Meta itself at call time, when it still has typed arguments).
const metaKey = "workflow_meta_result"

// workflowMetaBuiltin implements the predeclared workflow_meta(...)
// function. It both returns a starlark value (so `meta = workflow_meta(...)`
// works like any other script-level assignment) and stashes the parsed Go
// Meta on the thread for the engine to retrieve after ExecFile.
func workflowMetaBuiltin(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name        string
		description string
		phases      *starlark.List
		argsSchema  starlark.Value = starlark.None
	)
	if err := starlark.UnpackArgs("workflow_meta", args, kwargs,
		"name", &name,
		"description?", &description,
		"phases?", &phases,
		"args_schema?", &argsSchema,
	); err != nil {
		return nil, err
	}
	if name == "" {
		return nil, ErrMetaEmptyName
	}

	m := &Meta{Name: name, Description: description}

	if phases != nil {
		for e := range phases.Elements() {
			d, ok := e.(*starlark.Dict)
			if !ok {
				return nil, fmt.Errorf("workflow: workflow_meta phases entries must be dicts, got %s", e.Type())
			}
			var p Phase
			if v, found, _ := d.Get(starlark.String("title")); found {
				s, _ := starlark.AsString(v)
				p.Title = s
			}
			if v, found, _ := d.Get(starlark.String("model")); found {
				s, _ := starlark.AsString(v)
				p.Model = s
			}
			if v, found, _ := d.Get(starlark.String("effort")); found {
				s, _ := starlark.AsString(v)
				p.Effort = contracts.Effort(s)
			}
			m.Phases = append(m.Phases, p)
		}
	}

	if argsSchema != starlark.None {
		goVal, err := toGo(argsSchema)
		if err != nil {
			return nil, fmt.Errorf("workflow: workflow_meta args_schema: %w", err)
		}
		b, err := canonicalJSON(goVal)
		if err != nil {
			return nil, fmt.Errorf("workflow: workflow_meta args_schema encode: %w", err)
		}
		m.ArgsSchema = b
	}

	thread.SetLocal(metaKey, m)

	// The script-visible return value: a small read-only struct-shaped
	// dict good enough for a script to introspect its own meta (e.g.
	// `meta.name`) without the engine needing to decode it back.
	d := starlark.NewDict(2)
	if err := d.SetKey(starlark.String("name"), starlark.String(name)); err != nil {
		return nil, err
	}
	if err := d.SetKey(starlark.String("description"), starlark.String(description)); err != nil {
		return nil, err
	}
	d.Freeze()
	return d, nil
}
