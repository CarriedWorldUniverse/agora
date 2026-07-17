package workflow

import "testing"

func TestCanonicalJSON_SortsKeysDeterministically(t *testing.T) {
	a := map[string]any{"z": 1, "a": 2, "m": map[string]any{"y": 1, "b": 2}}
	b := map[string]any{"a": 2, "m": map[string]any{"b": 2, "y": 1}, "z": 1}

	ja, err := canonicalJSON(a)
	if err != nil {
		t.Fatalf("canonicalJSON(a): %v", err)
	}
	jb, err := canonicalJSON(b)
	if err != nil {
		t.Fatalf("canonicalJSON(b): %v", err)
	}
	if string(ja) != string(jb) {
		t.Fatalf("two maps with the same content but different construction order produced different canonical JSON:\n%s\n%s", ja, jb)
	}
	want := `{"a":2,"m":{"b":2,"y":1},"z":1}`
	if string(ja) != want {
		t.Fatalf("canonicalJSON = %s; want %s", ja, want)
	}
}

func TestContentHash_StableAcrossMapConstructionOrder(t *testing.T) {
	a := map[string]any{"prompt": "p", "model": "m", "effort": "high"}
	b := map[string]any{"effort": "high", "prompt": "p", "model": "m"}

	ha, err := contentHash(a)
	if err != nil {
		t.Fatalf("contentHash(a): %v", err)
	}
	hb, err := contentHash(b)
	if err != nil {
		t.Fatalf("contentHash(b): %v", err)
	}
	if ha != hb {
		t.Fatalf("contentHash differs across map construction order: %s vs %s", ha, hb)
	}

	c := map[string]any{"prompt": "p2", "model": "m", "effort": "high"}
	hc, err := contentHash(c)
	if err != nil {
		t.Fatalf("contentHash(c): %v", err)
	}
	if hc == ha {
		t.Fatalf("contentHash did not change when prompt content changed")
	}
}
