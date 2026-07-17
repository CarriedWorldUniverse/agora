package ctxmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

func rawArgs(t *testing.T, m map[string]string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func userMsg(seq int64, text string) contracts.ThreadItem {
	return contracts.ThreadItem{Seq: seq, Type: contracts.TIUserMessage, Payload: text}
}

func agentMsg(seq int64, text string) contracts.ThreadItem {
	return contracts.ThreadItem{Seq: seq, Type: contracts.TIAgentMessage, Payload: text}
}

func readCall(t *testing.T, seq int64, id, path string) contracts.ThreadItem {
	return contracts.ThreadItem{Seq: seq, Type: contracts.TIToolCall, Payload: ToolCallPayload{
		ToolName: "read_file", ID: id, Args: rawArgs(t, map[string]string{"path": path}),
	}}
}

func readResult(seq int64, id, content string) contracts.ThreadItem {
	return contracts.ThreadItem{Seq: seq, Type: contracts.TIToolResult, Payload: ToolResultPayload{
		ToolCallID: id, ToolName: "read_file", Content: content,
	}}
}

func writeCall(t *testing.T, seq int64, id, path, content string) contracts.ThreadItem {
	return contracts.ThreadItem{Seq: seq, Type: contracts.TIToolCall, Payload: ToolCallPayload{
		ToolName: "write_file", ID: id, Args: rawArgs(t, map[string]string{"path": path, "content": content}),
	}}
}

func editCall(t *testing.T, seq int64, id, path string) contracts.ThreadItem {
	return contracts.ThreadItem{Seq: seq, Type: contracts.TIToolCall, Payload: ToolCallPayload{
		ToolName: "apply_patch", ID: id, Args: rawArgs(t, map[string]string{"path": path}),
	}}
}

func cmdCall(t *testing.T, seq int64, id, cmd string) contracts.ThreadItem {
	return contracts.ThreadItem{Seq: seq, Type: contracts.TIToolCall, Payload: ToolCallPayload{
		ToolName: "run_command", ID: id, Args: rawArgs(t, map[string]string{"cmd": cmd}),
	}}
}

func cmdResult(seq int64, id, out string) contracts.ThreadItem {
	return contracts.ThreadItem{Seq: seq, Type: contracts.TIToolResult, Payload: ToolResultPayload{
		ToolCallID: id, ToolName: "run_command", Content: out,
	}}
}

func testModel() contracts.ModelInfo {
	return contracts.ModelInfo{ID: "test-model", ContextWindow: 1_000_000}
}

func joinContents(msgs []contracts.AssembledMessage) string {
	var parts []string
	for _, m := range msgs {
		parts = append(parts, m.Content)
	}
	return strings.Join(parts, "\n---\n")
}

// --- supersession / staleness goldens (deterministic) -----------------

func TestAssemble_OlderReadIsSupersededByNewerRead(t *testing.T) {
	m := NewManager(DefaultConfig(), testModel())
	items := []contracts.ThreadItem{
		readCall(t, 1, "c1", "a.py"),
		readResult(2, "c1", "VERSION ONE"),
		readCall(t, 3, "c2", "a.py"),
		readResult(4, "c2", "VERSION TWO"),
	}
	out, err := m.Assemble("t1", items)
	if err != nil {
		t.Fatal(err)
	}
	joined := joinContents(out)
	if strings.Contains(joined, "VERSION ONE") {
		t.Fatalf("older read must be superseded, not rendered verbatim:\n%s", joined)
	}
	if !strings.Contains(joined, "VERSION TWO") {
		t.Fatalf("newest read must render in full:\n%s", joined)
	}
	if !strings.Contains(joined, "superseded") {
		t.Fatalf("superseded copy must carry the supersession marker:\n%s", joined)
	}
}

func TestAssemble_WriteArgsSupersedeEarlierRead(t *testing.T) {
	// §2: "assistant write_file args are not a special case to trim, they
	// are working-set entries like any read — one live copy per key."
	m := NewManager(DefaultConfig(), testModel())
	items := []contracts.ThreadItem{
		readCall(t, 1, "c1", "a.py"),
		readResult(2, "c1", "OLD CONTENT"),
		writeCall(t, 3, "c2", "a.py", "NEW CONTENT"),
	}
	out, err := m.Assemble("t1", items)
	if err != nil {
		t.Fatal(err)
	}
	joined := joinContents(out)
	if strings.Contains(joined, "OLD CONTENT") {
		t.Fatalf("the read must be superseded by the later write's args:\n%s", joined)
	}
	if !strings.Contains(joined, "NEW CONTENT") {
		t.Fatalf("the write's content is the newest truth and must render:\n%s", joined)
	}
}

func TestAssemble_EditInvalidatesLiveCopy_RendersStaleStub(t *testing.T) {
	m := NewManager(DefaultConfig(), testModel())
	items := []contracts.ThreadItem{
		readCall(t, 1, "c1", "a.py"),
		readResult(2, "c1", "ORIGINAL CONTENT"),
		editCall(t, 3, "c2", "a.py"),
	}
	out, err := m.Assemble("t1", items)
	if err != nil {
		t.Fatal(err)
	}
	joined := joinContents(out)
	if strings.Contains(joined, "ORIGINAL CONTENT") {
		t.Fatalf("edit must invalidate the live copy — stale content must never render as truth:\n%s", joined)
	}
	if !strings.Contains(joined, "modified since") {
		t.Fatalf("expected the re-read stub:\n%s", joined)
	}
}

func TestAssemble_FSWatcherStalenessInvalidatesReadOutsideEditTool(t *testing.T) {
	m := NewManager(DefaultConfig(), testModel())
	m.ApplyFSChange(contracts.FSChange{Path: "a.py", Kind: "modified", ContentHash: "different-hash"})
	items := []contracts.ThreadItem{
		readCall(t, 1, "c1", "a.py"),
		readResult(2, "c1", "ORIGINAL CONTENT"),
	}
	out, err := m.Assemble("t1", items)
	if err != nil {
		t.Fatal(err)
	}
	joined := joinContents(out)
	if strings.Contains(joined, "ORIGINAL CONTENT") {
		t.Fatalf("an out-of-band fs-watcher change must invalidate the read, even without an edit tool call:\n%s", joined)
	}
}

func TestAssemble_IsDeterministic(t *testing.T) {
	items := []contracts.ThreadItem{
		userMsg(1, "please read a.py"),
		readCall(t, 2, "c1", "a.py"),
		readResult(3, "c1", "content of a"),
		agentMsg(4, "done"),
	}
	var prev string
	for i := 0; i < 5; i++ {
		m := NewManager(DefaultConfig(), testModel())
		out, err := m.Assemble("t1", items)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(out)
		if i > 0 && string(got) != prev {
			t.Fatalf("iteration %d: assembly not deterministic for stable input", i)
		}
		prev = string(got)
	}
}

// --- Ledger/cwlog-style benchmark harness ------------------------------

// TestBenchmark_CwlogStyleDeterministicCuration is the "cwlog-style
// benchmark harness that RUNS (deterministic input -> deterministic
// curation decisions)" DoD item: a synthetic multi-file, multi-step
// tool-dominated session (the shape of the arm-F evidence base, §0), run
// twice, must reach byte-identical curation decisions.
func TestBenchmark_CwlogStyleDeterministicCuration(t *testing.T) {
	items := buildCwlogBenchmarkSession(t, 50)

	run := func() ([]contracts.AssembledMessage, EvictionResult) {
		cfg := DefaultConfig()
		cfg.HotSteps = 2
		m := NewManager(cfg, contracts.ModelInfo{ID: "bench", ContextWindow: 20_000}) // small window: forces eviction
		out, err := m.Assemble("bench-thread", items)
		if err != nil {
			t.Fatal(err)
		}
		return out, EvictionResult{Demoted: demotedKeysFromEvents(m.DrainEvents())}
	}

	out1, ev1 := run()
	out2, ev2 := run()

	j1, _ := json.Marshal(out1)
	j2, _ := json.Marshal(out2)
	if string(j1) != string(j2) {
		t.Fatal("benchmark session did not curate deterministically across two runs")
	}
	if len(ev1.Demoted) == 0 {
		t.Fatal("benchmark session (small window, many files) should have triggered at least one eviction episode")
	}
	if len(ev1.Demoted) != len(ev2.Demoted) {
		t.Fatalf("eviction decisions differ across runs: %v vs %v", ev1.Demoted, ev2.Demoted)
	}
}

func demotedKeysFromEvents(evs []contracts.Event) []Key {
	var out []Key
	for _, e := range evs {
		if e.Type != contracts.EvCurationDemoted {
			continue
		}
		var p CurationDemotedPayload
		_ = json.Unmarshal(e.Payload, &p)
		for _, ks := range p.Keys {
			out = append(out, Key{Domain: "file", ID: strings.TrimPrefix(ks, "file:")})
		}
	}
	return out
}

// buildCwlogBenchmarkSession synthesizes a deterministic tool-dominated
// session: nFiles distinct files each read once, one file re-read (to
// exercise supersession), one file edited (to exercise staleness), and a
// stream of unkeyed command results (to exercise the recent-window cap).
func buildCwlogBenchmarkSession(t *testing.T, nFiles int) []contracts.ThreadItem {
	t.Helper()
	var items []contracts.ThreadItem
	var seq int64
	next := func() int64 { seq++; return seq }

	for i := 0; i < nFiles; i++ {
		path := "pkg/file" + itoa(i) + ".go"
		id := "call" + itoa(i)
		items = append(items,
			readCall(t, next(), id, path),
			readResult(next(), id, strings.Repeat("x", 500)+" body of "+path),
		)
	}
	// Re-read file0 with different content (supersession golden).
	items = append(items,
		readCall(t, next(), "reread0", "pkg/file0.go"),
		readResult(next(), "reread0", strings.Repeat("y", 500)+" UPDATED body of pkg/file0.go"),
	)
	// Edit file1 (staleness golden).
	items = append(items, editCall(t, next(), "edit1", "pkg/file1.go"))
	// A run of command output, more than keep_others, to exercise tier 4.
	for i := 0; i < 5; i++ {
		id := "cmd" + itoa(i)
		items = append(items, cmdCall(t, next(), id, "go test ./..."), cmdResult(next(), id, "ok  pkg  0.0"+itoa(i)+"s"))
	}
	return items
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
