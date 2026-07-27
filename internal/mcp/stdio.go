package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// protocolVersion is the MCP protocol revision agora's stdio client
// negotiates in `initialize`. Servers that speak a different revision are
// expected to still respond (the field is advisory in practice); a hard
// mismatch surfaces as a normal RPC error.
const protocolVersion = "2024-11-05"

// ToolCaller is the tool-INVOCATION half of a live MCP client, beyond the
// Client interface's {ListTools, Close}. A stdio/http client implements it;
// Source type-asserts a Manager's Client up to it to run tools/call.
type ToolCaller interface {
	// CallTool invokes rawTool (the server's own tool name, not the
	// mcp__-qualified visible name) with JSON arguments and returns the
	// concatenated text content, whether the server flagged the result an
	// error, and any transport/protocol error.
	CallTool(ctx context.Context, rawTool string, args json.RawMessage) (text string, isError bool, err error)
}

// StdioClient speaks MCP JSON-RPC over a subprocess's stdin/stdout,
// newline-delimited (one JSON object per line). Requests are serialized by
// a monotonic id; a single reader goroutine demuxes responses to per-id
// channels so notifications interleaved with responses don't desync the
// stream.
type StdioClient struct {
	cmd    *exec.Cmd
	stdin  *json.Encoder
	stdinW interface{ Close() error }
	toolTO time.Duration

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResponse
	closed  bool

	readerDone chan struct{}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      *int   `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("mcp rpc error %d: %s", e.Code, e.Message) }

var _ Client = (*StdioClient)(nil)
var _ ToolCaller = (*StdioClient)(nil)

// newStdioClient spawns cfg's command and completes the MCP initialize
// handshake (initialize request + notifications/initialized). The returned
// client is ready for ListTools/CallTool.
func newStdioClient(ctx context.Context, cfg ServerConfig) (*StdioClient, error) {
	if cfg.Command == "" {
		return nil, ErrMissingCommand
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = stdioEnv(cfg)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr // server diagnostics visible; not part of the RPC stream
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp stdio: start %q: %w", cfg.Command, err)
	}

	c := &StdioClient{
		cmd:        cmd,
		stdin:      json.NewEncoder(stdin),
		stdinW:     stdin,
		toolTO:     cfg.ToolTimeout,
		pending:    map[int]chan rpcResponse{},
		readerDone: make(chan struct{}),
	}
	go c.readLoop(bufio.NewReaderSize(stdout, 1<<20))

	if err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// stdioEnv builds the child env: the host env, plus cfg.Env overrides, plus
// cfg.EnvVars host-forwards (names to copy from the host env if present).
func stdioEnv(cfg ServerConfig) []string {
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	for _, ref := range cfg.EnvVars {
		if v, ok := os.LookupEnv(ref.Name); ok {
			env = append(env, ref.Name+"="+v)
		}
	}
	return env
}

func (c *StdioClient) initialize(ctx context.Context) error {
	_, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "agora", "version": "0.1"},
	})
	if err != nil {
		return fmt.Errorf("mcp stdio: initialize: %w", err)
	}
	// initialized is a notification (no id, no response expected).
	return c.notify("notifications/initialized", nil)
}

// ListTools issues tools/list (paginating on nextCursor) and maps the
// result into contracts.ToolSpec carrying the RAW tool names (qualification
// is Source's job via naming.AssignNames).
func (c *StdioClient) ListTools(ctx context.Context) ([]contracts.ToolSpec, error) {
	var specs []contracts.ToolSpec
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("mcp stdio: decode tools/list: %w", err)
		}
		for _, t := range res.Tools {
			specs = append(specs, contracts.ToolSpec{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
		if res.NextCursor == "" {
			return specs, nil
		}
		cursor = res.NextCursor
	}
}

// CallTool issues tools/call and concatenates the text content parts.
func (c *StdioClient) CallTool(ctx context.Context, rawTool string, args json.RawMessage) (string, bool, error) {
	if c.toolTO > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.toolTO)
		defer cancel()
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": rawTool, "arguments": args})
	if err != nil {
		return "", false, err
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", false, fmt.Errorf("mcp stdio: decode tools/call: %w", err)
	}
	var text string
	for _, part := range res.Content {
		if part.Type == "text" {
			text += part.Text
		}
	}
	return text, res.IsError, nil
}

// call sends a request and blocks for its matching response (or ctx).
func (c *StdioClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp stdio: client closed")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	err := c.stdin.Encode(rpcRequest{JSONRPC: "2.0", ID: &id, Method: method, Params: params})
	c.mu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp stdio: write %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case <-c.readerDone:
		return nil, fmt.Errorf("mcp stdio: connection closed awaiting %s", method)
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (c *StdioClient) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("mcp stdio: client closed")
	}
	return c.stdin.Encode(rpcRequest{JSONRPC: "2.0", Method: method, Params: params})
}

// readLoop demuxes one JSON object per line to the pending request by id;
// id-less messages (server notifications) are ignored. Closes readerDone on
// EOF so in-flight callers unblock instead of hanging.
func (c *StdioClient) readLoop(r *bufio.Reader) {
	defer close(c.readerDone)
	dec := json.NewDecoder(r)
	for {
		var resp rpcResponse
		if err := dec.Decode(&resp); err != nil {
			return // EOF or malformed stream: end; callers unblock via readerDone
		}
		if resp.ID == nil {
			continue // a notification/request from the server — v1 ignores
		}
		c.mu.Lock()
		ch, ok := c.pending[*resp.ID]
		if ok {
			delete(c.pending, *resp.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

// Close shuts stdin (signalling the server to exit), waits briefly, then
// kills the process if it lingers. Idempotent.
func (c *StdioClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	_ = c.stdinW.Close()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
	return nil
}
