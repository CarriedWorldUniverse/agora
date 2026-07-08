// ctxbench — run a ctxmap workload against a live backend and append to bench.db.
//
//	ctxbench -workload workloads/correction-fits.json -reps 3 -map=true [-tail 8]
//
// Env: same CTXMAP_* vars as ctxmapd.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CarriedWorldUniverse/agora/internal/backend/openai"
	"github.com/CarriedWorldUniverse/agora/internal/bench"
	"github.com/CarriedWorldUniverse/agora/internal/embed"
	"github.com/CarriedWorldUniverse/agora/internal/extractor"
	"github.com/CarriedWorldUniverse/agora/internal/harness"
	"github.com/CarriedWorldUniverse/agora/internal/render"
	"github.com/CarriedWorldUniverse/agora/internal/store"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	wlPath := flag.String("workload", "", "workload json path")
	reps := flag.Int("reps", 1, "repetitions")
	mapOn := flag.Bool("map", true, "map enabled")
	tail := flag.Int("tail", 8, "tail turns")
	model := flag.String("model", "ornith", "backend model")
	budget := flag.Int("budget", 200000, "assembly budget (approx tokens)")
	dbPath := flag.String("db", filepath.Join(os.Getenv("HOME"), ".ctxmap", "bench.db"), "bench.db path")
	flag.Parse()
	if *wlPath == "" {
		fmt.Fprintln(os.Stderr, "need -workload")
		os.Exit(2)
	}

	w, err := bench.LoadWorkload(*wlPath)
	if err != nil {
		panic(err)
	}
	db, err := bench.OpenDB(*dbPath)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	rev := "unknown"
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		rev = strings.TrimSpace(string(out))
	}
	// a dirty tree must not share a fingerprint with its base commit —
	// pre/post-change rows would silently aggregate (found when reconciler-v2
	// test rows landed on the old rev's fingerprint)
	if err := exec.Command("git", "diff", "--quiet", "HEAD").Run(); err != nil {
		rev += "-dirty"
	}
	exPath := env("CTXMAP_EXTRACT_MODEL", filepath.Join(os.Getenv("HOME"), "models/Qwen3-1.7B-Q8_0.gguf"))
	kindPath := env("CTXMAP_KIND_MODEL", filepath.Join(os.Getenv("HOME"), "models/Qwen3-4B-Q8_0.gguf"))
	fp := bench.Fingerprint{
		HarnessRev: rev, ExtractModel: filepath.Base(exPath), KindModel: filepath.Base(kindPath),
		BackendModel: *model, MapEnabled: *mapOn, TailTurns: *tail, AssemblyBudget: *budget,
	}

	var prop harness.Proposer
	var emb *embed.Llama
	var judge harness.PairJudge
	if *mapOn {
		ex, err := extractor.New(extractor.Config{ExtractModelPath: exPath, KindModelPath: kindPath, Threads: 12})
		if err != nil {
			panic(err)
		}
		defer ex.Close()
		prop = ex
		judge = ex
		if mp := env("CTXMAP_EMBED_MODEL", filepath.Join(os.Getenv("HOME"), "models/nomic-embed-text-v1.5.Q8_0.gguf")); mp != "" {
			if e, err := embed.NewLlama(mp, 8); err == nil {
				emb = e
				defer e.Close()
			}
		}
	}
	prov := openai.NewWithBaseURL(env("CTXMAP_API_KEY", "dummy"), env("CTXMAP_BASE_URL", "http://100.92.111.3:4000/v1"))

	mk := func() (*harness.Session, *store.Store, func(), error) {
		st, err := store.Open(":memory:")
		if err != nil {
			return nil, nil, nil, err
		}
		rend, err := render.New(st)
		if err != nil {
			return nil, nil, nil, err
		}
		sess := harness.NewSession(harness.Config{Model: *model, MapEnabled: *mapOn, TailTurns: *tail, AssemblyBudget: *budget}, prov, st, rend, prop)
		if emb != nil && judge != nil {
			sess.SetReconciler(emb, judge)
		}
		return sess, st, func() { sess.Close(); st.Close() }, nil
	}

	for rep := 0; rep < *reps; rep++ {
		rec, err := bench.Run(context.Background(), w, fp, rep, mk)
		if err != nil {
			fmt.Printf("rep %d: ERROR %v\n", rep, err)
			continue
		}
		if err := db.Append(*rec); err != nil {
			panic(err)
		}
		var probeSummary []string
		for _, p := range rec.Probes {
			mark := "PASS"
			if !p.Pass {
				mark = "FAIL(" + p.Detail + ")"
			}
			probeSummary = append(probeSummary, fmt.Sprintf("t%d:%s=%s", p.Turn, p.Type, mark))
		}
		fmt.Printf("rep %d: pass=%.0f%% %s tokens(in=%d out=%d) recalls=%d facts=%d %.0fs\n",
			rep, rec.PassRate*100, strings.Join(probeSummary, " "), rec.InputTokens, rec.OutputTokens,
			rec.RecallCalls, rec.FactCount, rec.WallSeconds)
	}
	rates, _ := db.PassRates(w.ID)
	fmt.Printf("\nhistorical pass rates for %s by fingerprint:\n", w.ID)
	for f, r := range rates {
		marker := ""
		if f == fp.Hash() {
			marker = "  <- this config"
		}
		fmt.Printf("  %s: %.0f%%%s\n", f, r*100, marker)
	}
}
