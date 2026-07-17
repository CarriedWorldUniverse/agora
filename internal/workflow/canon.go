package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// canonicalJSON serializes v deterministically: any map[string]any (the
// shape json.Unmarshal produces for a JSON object, and what convert.go's
// toGo returns for a starlark dict) has its keys sorted before encoding.
// Ground rule (repo-wide): "sort keys, never iterate a map into
// serialized/journalled output" — encoding/json's own map handling already
// sorts string keys, but toGo/hashing feed values through here explicitly
// so the rule holds even if that map ever gets built by hand.
func canonicalJSON(v any) ([]byte, error) {
	norm, err := canonicalize(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(norm)
}

// maxCanonicalizeDepth bounds canonicalize's native-Go recursion over a
// script-controlled value — the same "unbounded native-Go recursion ->
// process crash" backstop as convert.go's maxConvertDepth (a deeply
// self-nesting value passed as an agent schema/args or main()'s return
// value reaches here too). Kept at the same 10_000 bound for one shared,
// easy-to-reason-about story across both native-recursion entry points.
const maxCanonicalizeDepth = 10_000

// canonicalize walks v, turning any map[string]any into an
// orderedObject (a slice of key/value pairs sorted by key) that
// orderedObject.MarshalJSON then emits in that fixed order — sidestepping
// any reliance on encoding/json's incidental map-sorting behavior.
func canonicalize(v any) (any, error) {
	return canonicalizeDepth(v, 0)
}

func canonicalizeDepth(v any, depth int) (any, error) {
	if depth > maxCanonicalizeDepth {
		return nil, fmt.Errorf("%w: value nesting exceeds %d", ErrMaxDepthExceeded, maxCanonicalizeDepth)
	}
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		obj := make(orderedObject, 0, len(keys))
		for _, k := range keys {
			cv, err := canonicalizeDepth(t[k], depth+1)
			if err != nil {
				return nil, err
			}
			obj = append(obj, kv{k, cv})
		}
		return obj, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			cv, err := canonicalizeDepth(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = cv
		}
		return out, nil
	default:
		return v, nil
	}
}

type kv struct {
	K string
	V any
}

type orderedObject []kv

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, p := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(p.K)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(p.V)
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// contentHash returns the hex sha256 of v's canonical JSON encoding — the
// cache key spec §4 calls prompt_hash/payload_hash.
func contentHash(v any) (string, error) {
	b, err := canonicalJSON(v)
	if err != nil {
		return "", fmt.Errorf("workflow: canonicalize for hash: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
