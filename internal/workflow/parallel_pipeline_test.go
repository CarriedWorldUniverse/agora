package workflow

import (
	"context"
	"testing"
	"time"
)

func TestRun_Parallel_FansOutAndCollects(t *testing.T) {
	script := readTestdata(t, "parallel.star")
	invoker := &echoInvoker{}
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-parallel", Script: script,
		Args:    map[string]any{"labels": []any{"a", "b", "c"}},
		Clock:   fakeClock{t: time.Unix(1, 0).UTC()},
		Invoker: invoker, Journal: NewMemJournalStore(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := mustResult(t, out)
	results, ok := res["results"].([]any)
	if !ok || len(results) != 3 {
		t.Fatalf("results = %+v; want 3 entries", res["results"])
	}
	want := map[string]bool{"fan:a": true, "fan:b": true, "fan:c": true}
	for _, r := range results {
		s, _ := r.(string)
		if !want[s] {
			t.Fatalf("unexpected parallel result %q in %v", s, results)
		}
		delete(want, s)
	}
	if len(want) != 0 {
		t.Fatalf("missing parallel results: %v", want)
	}
	if calls := invoker.callLog(); len(calls) != 3 {
		t.Fatalf("invoker saw %d calls; want 3: %v", len(calls), calls)
	}
}

func TestRun_Pipeline_ChainsStagesPerItemIndependently(t *testing.T) {
	script := readTestdata(t, "pipeline.star")
	invoker := &echoInvoker{}
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-pipeline", Script: script,
		Args:    map[string]any{"items": []any{"x", "y"}},
		Clock:   fakeClock{t: time.Unix(1, 0).UTC()},
		Invoker: invoker, Journal: NewMemJournalStore(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := mustResult(t, out)
	results, ok := res["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results = %+v; want 2 entries", res["results"])
	}
	want := map[string]bool{"s2:s1:x": true, "s2:s1:y": true}
	for _, r := range results {
		s, _ := r.(string)
		if !want[s] {
			t.Fatalf("unexpected pipeline result %q in %v", s, results)
		}
		delete(want, s)
	}
	if len(want) != 0 {
		t.Fatalf("missing pipeline results: %v", want)
	}
	if calls := invoker.callLog(); len(calls) != 4 {
		t.Fatalf("invoker saw %d calls; want 4 (2 items * 2 stages): %v", len(calls), calls)
	}
}

func TestRun_Parallel_PerCallItemCapEnforced(t *testing.T) {
	script := readTestdata(t, "parallel.star")
	out, err := Run(context.Background(), RunOptions{
		RunID: "run-parallel-cap", Script: script,
		Args:           map[string]any{"labels": []any{"a", "b", "c"}},
		Clock:          fakeClock{t: time.Unix(1, 0).UTC()},
		Invoker:        &echoInvoker{},
		Journal:        NewMemJournalStore(),
		PerCallItemCap: 2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusErrored {
		t.Fatalf("Status = %q; want errored (3 items > cap 2)", out.Status)
	}
}
