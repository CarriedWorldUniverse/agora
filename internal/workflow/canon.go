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

// canonicalize walks v, turning any map[string]any into an
// orderedObject (a slice of key/value pairs sorted by key) that
// orderedObject.MarshalJSON then emits in that fixed order — sidestepping
// any reliance on encoding/json's incidental map-sorting behavior.
func canonicalize(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		obj := make(orderedObject, 0, len(keys))
		for _, k := range keys {
			cv, err := canonicalize(t[k])
			if err != nil {
				return nil, err
			}
			obj = append(obj, kv{k, cv})
		}
		return obj, nil
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			cv, err := canonicalize(e)
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
