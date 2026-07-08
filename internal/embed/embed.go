// Package embed provides sentence embeddings for the reconciler (dedup +
// contradiction candidates by cosine similarity). P1 seam: hides which
// embedding model/runtime. v0: nomic-embed-text-v1.5 Q8 via the in-process
// llama.cpp binding, mean-pooled + L2-normalized.
//
// Storage note (P5): the spec named sqlite-vec, but a per-session store holds
// tens-to-hundreds of facts — brute-force cosine over a BLOB column is
// microseconds and zero dependencies. sqlite-vec earns its place only if
// store size demands it.
package embed

import (
	"fmt"
	"sync"

	"github.com/CarriedWorldUniverse/agora/internal/extractor/llama"
)

type Embedder interface {
	Embed(text string) ([]float32, error)
}

type Llama struct {
	mu  sync.Mutex
	m   *llama.Model
	ctx *llama.Context
}

func NewLlama(modelPath string, threads int) (*Llama, error) {
	m, err := llama.LoadModel(modelPath)
	if err != nil {
		return nil, fmt.Errorf("embed model: %w", err)
	}
	ctx, err := m.NewEmbedContext(2048, threads)
	if err != nil {
		return nil, fmt.Errorf("embed context: %w", err)
	}
	return &Llama{m: m, ctx: ctx}, nil
}

func (l *Llama) Embed(text string) ([]float32, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// nomic-embed expects a task prefix; facts are documents.
	return l.ctx.Embed("search_document: " + text)
}

func (l *Llama) Close() {
	l.ctx.Free()
	l.m.Free()
}

// Cos computes cosine similarity. Inputs are already L2-normalized by Embed,
// so this is a plain dot product; guards anyway for foreign vectors.
func Cos(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot
}
