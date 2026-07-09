// Package bench implements the ctxmap repeatable workload scheme (spec §9.1).
//
// A workload is a versioned scripted session: ordered turns, some carrying
// machine-checkable probes. The runner drives a harness.Session directly
// (in-process), evaluates probes, and appends one row per (workload, rep)
// to bench.db keyed by a config fingerprint. History is the asset: two runs
// are comparable iff their fingerprints differ in exactly the dimension
// under test.
package bench

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/agora/internal/harness"
	"github.com/CarriedWorldUniverse/agora/internal/store"

	_ "github.com/mattn/go-sqlite3"
)

// ---- workload format ----

type Probe struct {
	// Type: "contains" (answer must contain Want, case-insensitive),
	// "not_contains", "fact_in_store" (a fact matching Want by token overlap
	// exists, non-retracted), "no_live_contradiction" (store audit finds no
	// live core contradiction).
	Type string `json:"type"`
	Want string `json:"want,omitempty"`
}

type WorkloadTurn struct {
	Message string  `json:"message"`
	Probe   *Probe  `json:"probe,omitempty"`
	PadTo   int     `json:"pad_to,omitempty"` // approx tokens of filler appended to this message (overflow tier)
}

type Workload struct {
	ID           string         `json:"id"`
	Version      int            `json:"version"`
	PressureTier string         `json:"pressure_tier"` // "fits-in-window" | "overflow"
	Turns        []WorkloadTurn `json:"turns"`
}

func LoadWorkload(path string) (*Workload, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w Workload
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// ---- fingerprint ----

// Fingerprint identifies a bench configuration. Every field participates in
// the hash; runs at different fingerprints are not directly comparable
// except along the single dimension under test.
type Fingerprint struct {
	HarnessRev     string `json:"harness_rev"`
	ExtractModel   string `json:"extract_model"`
	KindModel      string `json:"kind_model"`
	PromptHash     string `json:"prompt_hash"` // extraction+kind prompt hash
	BackendModel   string `json:"backend_model"`
	MapEnabled     bool   `json:"map_enabled"`
	TailTurns      int    `json:"tail_turns"`
	AssemblyBudget int    `json:"assembly_budget"`
}

func (f Fingerprint) Hash() string {
	b, _ := json.Marshal(f)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}

// ---- results ----

type ProbeResult struct {
	Turn   int    `json:"turn"`
	Type   string `json:"type"`
	Want   string `json:"want"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// TurnStat decomposes per-rep totals: the token-economics headline lives in
// PROBE-turn prompt sizes (map-on: a few k; standard: ~budget), which rep
// totals hide — both sides pay the same unavoidable padded-message costs
// during the bulk phase.
type TurnStat struct {
	Turn         int     `json:"turn"`
	InputTokens  int     `json:"in"`
	OutputTokens int     `json:"out"`
	CachedTokens int     `json:"cached"` // prefix-cache hits within InputTokens
	WallSeconds  float64 `json:"s"`
	Padded       bool    `json:"padded,omitempty"`
	Probe        bool    `json:"probe,omitempty"`
}

type RunRecord struct {
	BenchVersion int           `json:"bench_version"`
	WorkloadID   string        `json:"workload_id"`
	Rep          int           `json:"rep"`
	Fingerprint  string        `json:"fingerprint"`
	FingerprintJSON string     `json:"fingerprint_json"`
	Probes       []ProbeResult `json:"probes"`
	PassRate     float64       `json:"pass_rate"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	CachedTokens int           `json:"cached_tokens"`
	RecallCalls  int           `json:"recall_calls"`
	FactCount    int           `json:"fact_count"`
	WallSeconds  float64       `json:"wall_seconds"`
	TurnStats    []TurnStat    `json:"turn_stats"`
}

type DB struct{ db *sql.DB }

func OpenDB(path string) (*DB, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS runs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		bench_version INTEGER, workload_id TEXT, rep INTEGER,
		fingerprint TEXT, fingerprint_json TEXT,
		probes TEXT, pass_rate REAL,
		input_tokens INTEGER, output_tokens INTEGER,
		recall_calls INTEGER, fact_count INTEGER, wall_seconds REAL,
		created TEXT, turn_stats TEXT, cached_tokens INTEGER
	)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	// migrations for older DBs; harmless if the columns exist
	db.Exec(`ALTER TABLE runs ADD COLUMN turn_stats TEXT`)
	db.Exec(`ALTER TABLE runs ADD COLUMN cached_tokens INTEGER`)
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }

func (d *DB) Append(r RunRecord) error {
	probes, _ := json.Marshal(r.Probes)
	ts, _ := json.Marshal(r.TurnStats)
	_, err := d.db.Exec(`INSERT INTO runs (bench_version,workload_id,rep,fingerprint,fingerprint_json,probes,pass_rate,input_tokens,output_tokens,recall_calls,fact_count,wall_seconds,created,turn_stats,cached_tokens)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.BenchVersion, r.WorkloadID, r.Rep, r.Fingerprint, r.FingerprintJSON, string(probes),
		r.PassRate, r.InputTokens, r.OutputTokens, r.RecallCalls, r.FactCount, r.WallSeconds,
		time.Now().UTC().Format(time.RFC3339), string(ts), r.CachedTokens)
	return err
}

// PassRates returns per-fingerprint mean pass rate for a workload —
// the comparison surface for the merge gate.
func (d *DB) PassRates(workloadID string) (map[string]float64, error) {
	rows, err := d.db.Query(`SELECT fingerprint, AVG(pass_rate) FROM runs WHERE workload_id=? GROUP BY fingerprint`, workloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var fp string
		var avg float64
		rows.Scan(&fp, &avg)
		out[fp] = avg
	}
	return out, nil
}

// ---- runner ----

type SessionFactory func() (*harness.Session, *store.Store, func(), error)

// Run drives one workload once through a fresh session and returns the record.
func Run(ctx context.Context, w *Workload, fp Fingerprint, rep int, mk SessionFactory) (*RunRecord, error) {
	sess, st, cleanup, err := mk()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	rec := &RunRecord{
		BenchVersion: 1, WorkloadID: w.ID, Rep: rep,
		Fingerprint: fp.Hash(),
	}
	fpj, _ := json.Marshal(fp)
	rec.FingerprintJSON = string(fpj)

	t0 := time.Now()
	for i, wt := range w.Turns {
		msg := wt.Message
		if wt.PadTo > 0 {
			msg += "\n\n" + filler(w.ID, i, wt.PadTo)
		}
		// Per-turn timeout + one retry: a single dropped backend response must
		// fail the rep, never wedge the campaign (learned the hard way —
		// campaign 1 hung 2h on one stalled stream with no client timeout).
		// Timeout scales with the WORKLOAD tier, not the current turn's padding:
		// an unpadded probe turn still prefills the whole padded history (a
		// 5-min budget on a 20-min prefill cost longhaul rep 0).
		d := turnTimeout(w.PressureTier)
		tTurn := time.Now()
		res, err := turnWithTimeout(ctx, sess, msg, d)
		if err != nil {
			res, err = turnWithTimeout(ctx, sess, msg, d)
		}
		if err != nil {
			return nil, fmt.Errorf("turn %d: %w", i+1, err)
		}
		rec.InputTokens += res.InputTokens
		rec.OutputTokens += res.OutputTokens
		rec.CachedTokens += res.CachedTokens
		rec.RecallCalls += res.RecallCalls
		rec.TurnStats = append(rec.TurnStats, TurnStat{
			Turn: i + 1, InputTokens: res.InputTokens, OutputTokens: res.OutputTokens,
			CachedTokens: res.CachedTokens,
			WallSeconds:  time.Since(tTurn).Seconds(), Padded: wt.PadTo > 0, Probe: wt.Probe != nil,
		}) // res token counts are per-turn (TurnResult resets each Turn call)

		if wt.Probe != nil {
			sess.WaitExtraction() // probes see a settled store
			pr := evalProbe(wt.Probe, res.Answer, st)
			pr.Turn = i + 1
			rec.Probes = append(rec.Probes, pr)
		}
	}
	rec.WallSeconds = time.Since(t0).Seconds()

	if all, err := st.QueryText("", 100000); err == nil {
		rec.FactCount = len(all)
	}
	pass := 0
	for _, p := range rec.Probes {
		if p.Pass {
			pass++
		}
	}
	if len(rec.Probes) > 0 {
		rec.PassRate = float64(pass) / float64(len(rec.Probes))
	}
	return rec, nil
}

// turnTimeout scales with the workload's pressure tier: overflow-tier
// prefills are legitimately slow (cold ~200k-token prefill takes minutes on
// the GB10), and EVERY turn of an overflow workload carries that history.
func turnTimeout(tier string) time.Duration {
	if tier == "overflow" {
		return 20 * time.Minute
	}
	return 5 * time.Minute
}

func turnWithTimeout(ctx context.Context, sess *harness.Session, msg string, d time.Duration) (*harness.TurnResult, error) {
	tctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return sess.Turn(tctx, msg)
}

func evalProbe(p *Probe, answer string, st *store.Store) ProbeResult {
	pr := ProbeResult{Type: p.Type, Want: p.Want}
	switch p.Type {
	case "contains":
		pr.Pass = strings.Contains(strings.ToLower(answer), strings.ToLower(p.Want))
		if !pr.Pass {
			pr.Detail = truncate(answer, 160)
		}
	case "not_contains":
		pr.Pass = !strings.Contains(strings.ToLower(answer), strings.ToLower(p.Want))
		if !pr.Pass {
			pr.Detail = truncate(answer, 160)
		}
	case "fact_in_store":
		facts, _ := st.QueryText("", 100000)
		for _, f := range facts {
			if f.Status != store.StatusRetracted && overlapF1(p.Want, f.Statement) >= 0.5 {
				pr.Pass = true
				pr.Detail = f.ID
				break
			}
		}
	case "no_live_contradiction":
		pr.Pass = true
		for _, issue := range st.Audit() {
			if strings.Contains(issue, "live contradiction") {
				pr.Pass = false
				pr.Detail = issue
			}
		}
	default:
		pr.Detail = "unknown probe type"
	}
	return pr
}

// filler generates deterministic realistic-shaped padding (tool-output-like
// noise) — seeded by workload+turn so reps are byte-identical. ~4 chars/token.
func filler(seed string, turn, tokens int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", seed, turn)))
	state := uint64(h[0])<<56 | uint64(h[1])<<48 | uint64(h[2])<<40 | uint64(h[3])<<32 |
		uint64(h[4])<<24 | uint64(h[5])<<16 | uint64(h[6])<<8 | uint64(h[7])
	words := []string{"chunk", "buffer", "loaded", "status", "ok", "queued", "worker", "tick",
		"mesh", "region", "cache", "hit", "miss", "flush", "sync", "batch", "node", "warn",
		"retry", "done", "idle", "scan", "index", "merge", "skip", "pass", "frame", "sample"}
	var b strings.Builder
	b.WriteString("[attached log excerpt]\n")
	n := tokens
	for n > 0 {
		state = state*6364136223846793005 + 1442695040888963407
		w := words[state%uint64(len(words))]
		b.WriteString(w)
		n--
		if state%9 == 0 {
			b.WriteString("\n")
		} else {
			b.WriteString(" ")
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func overlapF1(a, b string) float64 {
	toks := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.Fields(strings.ToLower(s)) {
			w = strings.Trim(w, ".,;:!?\"'()[]")
			if len(w) > 3 {
				out[w] = true
			}
		}
		return out
	}
	A, B := toks(a), toks(b)
	if len(A) == 0 || len(B) == 0 {
		return 0
	}
	inter := 0
	for w := range A {
		if B[w] {
			inter++
		}
	}
	if inter == 0 {
		return 0
	}
	p, r := float64(inter)/float64(len(B)), float64(inter)/float64(len(A))
	return 2 * p * r / (p + r)
}
