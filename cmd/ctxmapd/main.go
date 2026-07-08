// ctxmapd — the ctxmap control MCP server (spec §7).
//
// Speaks MCP over stdio (newline-delimited JSON-RPC 2.0) and exposes the
// control surface that lets an outside agent evaluate the harness BY USING
// the harness: session_start (map_enabled ablation switch), prompt, map_query,
// map_inspect, map_stats, render_preview, store_audit, session_end.
//
// Config (env):
//   CTXMAP_BASE_URL       backend OpenAI-compatible base URL (default litellm on robo-dog)
//   CTXMAP_API_KEY        backend key (default "dummy")
//   CTXMAP_EXTRACT_MODEL  gguf path for the extraction model (Qwen3-1.7B-Q8)
//   CTXMAP_KIND_MODEL     gguf path for the kind model (Qwen3-4B-Q8; empty = reuse)
//   CTXMAP_DATA_DIR       where session stores + transcripts live (default ~/.ctxmap)
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CarriedWorldUniverse/agora/internal/backend/openai"
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

// lockedProposer serializes Propose calls: two sessions must not drive the
// shared llama models concurrently.
type lockedProposer struct {
	mu sync.Mutex
	p  *extractor.Extractor
}

func (l *lockedProposer) Propose(cur extractor.Turn, ctx []extractor.Turn, gl map[string]string) ([]extractor.FactProposal, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.p.Propose(cur, ctx, gl)
}

type sessionEntry struct {
	sess *harness.Session
	st   *store.Store
	rend *render.Renderer
}

type server struct {
	mu       sync.Mutex
	sessions map[string]*sessionEntry
	prop     *lockedProposer // lazy-loaded
	dataDir  string
}

func main() {
	dataDir := env("CTXMAP_DATA_DIR", filepath.Join(os.Getenv("HOME"), ".ctxmap"))
	os.MkdirAll(dataDir, 0o700)
	s := &server{sessions: map[string]*sessionEntry{}, dataDir: dataDir}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req rpcReq
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		if req.Method == "notifications/initialized" || strings.HasPrefix(req.Method, "notifications/") {
			continue // notifications get no response
		}
		resp := s.handle(&req)
		if resp != nil {
			out.Encode(resp)
		}
	}
}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *server) handle(req *rpcReq) *rpcResp {
	resp := &rpcResp{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "ctxmapd", "version": "0.1.0"},
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": toolList()}
	case "tools/call":
		var p struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"arguments"`
		}
		json.Unmarshal(req.Params, &p)
		text, err := s.call(p.Name, p.Args)
		if err != nil {
			resp.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ERROR: " + err.Error()}}, "isError": true}
		} else {
			resp.Result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}

func toolList() []map[string]any {
	obj := func(props string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":` + props + `}`)
	}
	return []map[string]any{
		{"name": "session_start", "description": "Start a harness session. map_enabled=false is the ablation/standard-comparator mode. Returns session_id.",
			"inputSchema": obj(`{"model":{"type":"string","description":"backend model id, default ornith"},"map_enabled":{"type":"boolean","default":true},"tail_turns":{"type":"integer","description":"raw transcript tail length; large value approximates a standard full-history harness"},"system_prompt":{"type":"string"}}`)},
		{"name": "prompt", "description": "Run one full turn through the harness. Returns answer plus the map's side effects (facts extracted, notices, tokens, recall calls).",
			"inputSchema": obj(`{"session_id":{"type":"string"},"message":{"type":"string"},"wait_extraction":{"type":"boolean","default":true,"description":"block until async extraction lands (bench probes need a settled store)"}}`)},
		{"name": "map_query", "description": "Query the session's fact store by text or entity slug.",
			"inputSchema": obj(`{"session_id":{"type":"string"},"query":{"type":"string"},"entity":{"type":"string"}}`)},
		{"name": "map_inspect", "description": "Audit one fact back to its provenance (statement, status, trust, links, evidence turn).",
			"inputSchema": obj(`{"session_id":{"type":"string"},"fact_id":{"type":"string"}}`)},
		{"name": "map_stats", "description": "Store counts by status/kind, epoch, session turn count.",
			"inputSchema": obj(`{"session_id":{"type":"string"}}`)},
		{"name": "render_preview", "description": "The exact core+subgraph text the next turn would inject, without spending a model call.",
			"inputSchema": obj(`{"session_id":{"type":"string"},"message":{"type":"string","description":"hypothetical next user message used for retrieval seeding"}}`)},
		{"name": "store_audit", "description": "Run the deterministic store invariants (spec §5). Empty result = clean.",
			"inputSchema": obj(`{"session_id":{"type":"string"}}`)},
		{"name": "consolidate", "description": "Open a new epoch: re-render the core block from current VERIFIED facts (knowingly pays one cold prefill).",
			"inputSchema": obj(`{"session_id":{"type":"string"}}`)},
		{"name": "session_end", "description": "Close a session (drains extraction queue).",
			"inputSchema": obj(`{"session_id":{"type":"string"}}`)},
	}
}

func (s *server) getSession(id string) (*sessionEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("no such session: %s", id)
	}
	return e, nil
}

func (s *server) loadExtractor() (*lockedProposer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prop != nil {
		return s.prop, nil
	}
	ex, err := extractor.New(extractor.Config{
		ExtractModelPath: env("CTXMAP_EXTRACT_MODEL", filepath.Join(os.Getenv("HOME"), "models/Qwen3-1.7B-Q8_0.gguf")),
		KindModelPath:    env("CTXMAP_KIND_MODEL", filepath.Join(os.Getenv("HOME"), "models/Qwen3-4B-Q8_0.gguf")),
		Threads:          12,
	})
	if err != nil {
		return nil, err
	}
	s.prop = &lockedProposer{p: ex}
	return s.prop, nil
}

func (s *server) call(name string, raw json.RawMessage) (string, error) {
	var a struct {
		SessionID      string `json:"session_id"`
		Message        string `json:"message"`
		Model          string `json:"model"`
		MapEnabled     *bool  `json:"map_enabled"`
		TailTurns      int    `json:"tail_turns"`
		SystemPrompt   string `json:"system_prompt"`
		WaitExtraction *bool  `json:"wait_extraction"`
		Query          string `json:"query"`
		Entity         string `json:"entity"`
		FactID         string `json:"fact_id"`
	}
	if len(raw) > 0 {
		json.Unmarshal(raw, &a)
	}

	switch name {
	case "session_start":
		mapOn := true
		if a.MapEnabled != nil {
			mapOn = *a.MapEnabled
		}
		model := a.Model
		if model == "" {
			model = "ornith"
		}
		var prop harness.Proposer
		if mapOn {
			lp, err := s.loadExtractor()
			if err != nil {
				return "", fmt.Errorf("extractor load: %w", err)
			}
			prop = lp
		}
		prov := openai.NewWithBaseURL(env("CTXMAP_API_KEY", "dummy"), env("CTXMAP_BASE_URL", "http://100.92.111.3:4000/v1"))
		// each session gets its own store file + transcript (bench isolation)
		tmpID := fmt.Sprintf("s%d", len(s.sessions)+1)
		stPath := filepath.Join(s.dataDir, tmpID+".db")
		os.Remove(stPath)
		st, err := store.Open(stPath)
		if err != nil {
			return "", err
		}
		rend, err := render.New(st)
		if err != nil {
			return "", err
		}
		sess := harness.NewSession(harness.Config{
			SystemPrompt: a.SystemPrompt, Model: model, MapEnabled: mapOn,
			TailTurns:      a.TailTurns,
			TranscriptPath: filepath.Join(s.dataDir, tmpID+".transcript.jsonl"),
		}, prov, st, rend, prop)
		s.mu.Lock()
		s.sessions[sess.ID()] = &sessionEntry{sess: sess, st: st, rend: rend}
		s.mu.Unlock()
		return jsonOut(map[string]any{"session_id": sess.ID(), "map_enabled": mapOn, "model": model}), nil

	case "prompt":
		e, err := s.getSession(a.SessionID)
		if err != nil {
			return "", err
		}
		res, err := e.sess.Turn(context.Background(), a.Message)
		if err != nil {
			return "", err
		}
		wait := true
		if a.WaitExtraction != nil {
			wait = *a.WaitExtraction
		}
		var extracted []string
		if wait {
			for _, id := range e.sess.WaitExtraction() {
				f, err := e.st.Get(id)
				if err == nil {
					extracted = append(extracted, fmt.Sprintf("[%s] (%s/%s) %s", f.ID, f.Kind, f.Status, f.Statement))
				}
			}
		}
		return jsonOut(map[string]any{
			"answer": res.Answer, "turn": res.TurnN, "facts_extracted": extracted,
			"notices": res.Notices, "input_tokens": res.InputTokens,
			"output_tokens": res.OutputTokens, "recall_calls": res.RecallCalls,
		}), nil

	case "map_query":
		e, err := s.getSession(a.SessionID)
		if err != nil {
			return "", err
		}
		var facts []*store.Fact
		if a.Entity != "" {
			facts, err = e.st.QueryEntity(a.Entity, 20)
		} else {
			facts, err = e.st.QueryText(a.Query, 20)
		}
		if err != nil {
			return "", err
		}
		var lines []string
		for _, f := range facts {
			lines = append(lines, fmt.Sprintf("[%s] (%s/%s/%s) %s", f.ID, f.Kind, f.Status, f.Trust, f.Statement))
		}
		if len(lines) == 0 {
			return "(no matches)", nil
		}
		return strings.Join(lines, "\n"), nil

	case "map_inspect":
		e, err := s.getSession(a.SessionID)
		if err != nil {
			return "", err
		}
		f, err := e.st.Get(a.FactID)
		if err != nil {
			return "", err
		}
		links, _ := e.st.Links(f.ID)
		return jsonOut(map[string]any{
			"id": f.ID, "statement": f.Statement, "kind": f.Kind, "status": f.Status,
			"trust": f.Trust, "stale": f.Stale, "pinned": f.Pinned, "entities": f.Entities,
			"parents": f.Parents, "provenance": f.Provenance, "links": links,
			"render_turns": f.RenderTurns, "created": f.Created,
		}), nil

	case "map_stats":
		e, err := s.getSession(a.SessionID)
		if err != nil {
			return "", err
		}
		counts := map[string]int{}
		all, _ := e.st.QueryText("", 100000)
		for _, f := range all {
			counts[string(f.Status)]++
			counts["kind:"+string(f.Kind)]++
			if f.Stale {
				counts["stale"]++
			}
		}
		core, _ := e.st.Core()
		return jsonOut(map[string]any{"counts": counts, "core_size": len(core), "epoch": e.rend.Epoch()}), nil

	case "render_preview":
		e, err := s.getSession(a.SessionID)
		if err != nil {
			return "", err
		}
		sub, _ := e.rend.RenderSubgraph(e.sess.RetrievePreview(a.Message))
		return e.rend.RenderCore() + "\n\n" + sub, nil

	case "store_audit":
		e, err := s.getSession(a.SessionID)
		if err != nil {
			return "", err
		}
		issues := e.st.Audit()
		if len(issues) == 0 {
			return "clean", nil
		}
		return strings.Join(issues, "\n"), nil

	case "consolidate":
		e, err := s.getSession(a.SessionID)
		if err != nil {
			return "", err
		}
		if err := e.rend.NewEpoch(); err != nil {
			return "", err
		}
		return fmt.Sprintf("epoch %d opened", e.rend.Epoch()), nil

	case "session_end":
		e, err := s.getSession(a.SessionID)
		if err != nil {
			return "", err
		}
		e.sess.Close()
		e.st.Close()
		s.mu.Lock()
		delete(s.sessions, a.SessionID)
		s.mu.Unlock()
		return "closed", nil
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

func jsonOut(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
