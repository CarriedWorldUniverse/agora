package workflow

import (
	"encoding/json"
	"fmt"
	"sort"

	"go.starlark.net/starlark"
)

// jsonRawToGo unmarshals raw (a JSON document) into the same
// nil/bool/float64/string/[]any/map[string]any universe toGo/toStarlark
// use. An empty/nil raw decodes to a nil Go value (the engine's convention
// for "no result" — spec §2: ctx.agent() "Returns None if the agent
// dies/skipped").
func jsonRawToGo(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("workflow: decode JSON value: %w", err)
	}
	return v, nil
}

// toGo converts a starlark.Value into a plain Go value built only from
// nil/bool/int64/float64/string/[]any/map[string]any — the JSON-shaped
// universe canon.go and encoding/json both understand. Dict key order is
// NOT preserved (Go maps don't preserve it); anywhere that order matters
// for determinism, canonicalJSON re-sorts explicitly (ground rule: sort
// keys, never trust map iteration order).
func toGo(v starlark.Value) (any, error) {
	switch t := v.(type) {
	case starlark.NoneType, nil:
		return nil, nil
	case starlark.Bool:
		return bool(t), nil
	case starlark.Int:
		i, ok := t.Int64()
		if !ok {
			return nil, fmt.Errorf("workflow: integer %s does not fit in int64", t.String())
		}
		return i, nil
	case starlark.Float:
		return float64(t), nil
	case starlark.String:
		return string(t), nil
	case *starlark.List:
		out := make([]any, 0, t.Len())
		for e := range t.Elements() {
			ev, err := toGo(e)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
		return out, nil
	case starlark.Tuple:
		out := make([]any, 0, len(t))
		for e := range t.Elements() {
			ev, err := toGo(e)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
		return out, nil
	case *starlark.Dict:
		out := make(map[string]any, t.Len())
		for _, item := range t.Items() {
			k, ok := starlark.AsString(item[0])
			if !ok {
				return nil, fmt.Errorf("workflow: dict key %s is not a string", item[0].String())
			}
			vv, err := toGo(item[1])
			if err != nil {
				return nil, err
			}
			out[k] = vv
		}
		return out, nil
	default:
		return nil, fmt.Errorf("workflow: value of type %s is not JSON-representable", v.Type())
	}
}

// toStarlark converts a plain Go value (as produced by toGo, or by
// encoding/json.Unmarshal into any) into a starlark.Value.
func toStarlark(v any) (starlark.Value, error) {
	switch t := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(t), nil
	case string:
		return starlark.String(t), nil
	case int:
		return starlark.MakeInt(t), nil
	case int64:
		return starlark.MakeInt64(t), nil
	case float64:
		return starlark.Float(t), nil
	case []any:
		elems := make([]starlark.Value, len(t))
		for i, e := range t {
			ev, err := toStarlark(e)
			if err != nil {
				return nil, err
			}
			elems[i] = ev
		}
		return starlark.NewList(elems), nil
	case map[string]any:
		d := starlark.NewDict(len(t))
		keys := sortedKeys(t)
		for _, k := range keys {
			vv, err := toStarlark(t[k])
			if err != nil {
				return nil, err
			}
			if err := d.SetKey(starlark.String(k), vv); err != nil {
				return nil, err
			}
		}
		return d, nil
	default:
		return nil, fmt.Errorf("workflow: cannot convert Go value of type %T to starlark", v)
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Deterministic order into the starlark dict — ground rule again: never
	// let map iteration order leak into anything observable (here, the
	// order a script would see via ctx.args.items()/keys()).
	sort.Strings(keys)
	return keys
}
