package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CarriedWorldUniverse/agora/contracts"
	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
	"github.com/CarriedWorldUniverse/agora/internal/persistence"
	"github.com/CarriedWorldUniverse/agora/internal/toolrunner"
	"github.com/CarriedWorldUniverse/agora/internal/tui"
	"github.com/CarriedWorldUniverse/agora/internal/turnengine"
	"github.com/CarriedWorldUniverse/bridle/provider/claudesdk"
)

// newInProcessBackend builds the PRODUCTION in-process turn engine
// (agora-engine-blueprint.md U-E1, Phase 4, agora-spec-io.md §0a) — what
// bare `agora` falls back to when no daemon is listening (see dialBackend
// in main.go): a turnengine.Manager over the REAL claude-sdk funnel-mode
// provider, wrapped in an io.Session/Attachment exactly like a
// daemon-hosted thread would be, then wrapped in a tui.Backend
// (tui.NewLocalBackend).
//
// provider := claudesdk.New() is the REAL subscription lane (funnel mode,
// blueprint's locked decisions #2/#3: ambient credentials only, per-turn
// sidecar spawn). Constructing it does NOT itself spawn the
// bridle-claude-sidecar or touch ambient credentials — that only happens
// the first time a turn actually runs (Manager.Run -> Harness.RunTurn),
// which is U-F1's job to smoke-test manually against a live subscription.
// An `agora doctor`-style preflight that checks the sidecar + ambient
// creds resolve BEFORE the first turn is a separate, later follow-on
// (blueprint Phase 0 U-A2) — noted, not built here.
func newInProcessBackend(ctx context.Context, threadID string, attach agoraio.AttachRequest) (tui.Backend, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("inprocess: getwd: %w", err)
	}
	roots, err := toolrunner.NewRoots(cwd)
	if err != nil {
		return nil, fmt.Errorf("inprocess: build roots for %q: %w", cwd, err)
	}

	store, err := newInProcessStore()
	if err != nil {
		return nil, fmt.Errorf("inprocess: open thread store: %w", err)
	}
	if err := ensureThreadCreated(store, threadID, roots.WorkingDir); err != nil {
		return nil, fmt.Errorf("inprocess: create thread %q: %w", threadID, err)
	}

	provider := claudesdk.New()
	mgr := turnengine.NewManager(threadID, provider,
		turnengine.WithRoots(roots),
		turnengine.WithStore(store),
		// Interactive sessions distill each turn into durable facts using the
		// active model itself (ctxmap fact extraction) — off by default in the
		// engine, on here.
		turnengine.WithContextExtraction(true),
	)

	sess := agoraio.NewSession(ctx, threadID, mgr)
	att := sess.Attach(agoraio.AttachInfo{
		ClientID:     attach.ClientID,
		Kind:         attach.Kind,
		Capabilities: attach.Capabilities,
	}, attach.Replay)

	return &inProcessBackend{Backend: tui.NewLocalBackend(sess, att), store: store}, nil
}

// inProcessBackend wraps the localBackend so the in-process ThreadStore's
// lifecycle is owned here: Close tears down the engine/session FIRST (so the
// Manager stops using the store), THEN closes the store if it holds a
// resource (LocalStore's *sql.DB). Without this the sqlite handle leaks until
// process exit — harmless on unix, but on Windows an open file can't be
// removed, which broke t.TempDir cleanup in the fallback test. contracts.
// ThreadStore has no Close(), so this is an io.Closer type-assertion (a
// MemStore, which isn't a Closer, is a no-op here).
type inProcessBackend struct {
	tui.Backend
	store     contracts.ThreadStore
	closeOnce sync.Once
	closeErr  error
}

func (b *inProcessBackend) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.Backend.Close()
		if c, ok := b.store.(io.Closer); ok {
			if cerr := c.Close(); cerr != nil && b.closeErr == nil {
				b.closeErr = cerr
			}
		}
	})
	return b.closeErr
}

// HistoryEntry is one prior text message for the resume replay (NEX-798).
type HistoryEntry struct {
	Role string // "user" | "agent"
	Text string
}

// HistoryTail returns up to k trailing user/agent text messages from threadID's
// persisted history plus the count of elided earlier items — what main prints
// into terminal scrollback before the TUI starts, so a resumed session shows
// where the conversation left off. Tool/other items are skipped (they're
// noise at resume; the text thread is the story).
func (b *inProcessBackend) HistoryTail(threadID string, k int) (entries []HistoryEntry, elided int, err error) {
	it, err := b.store.Resume(threadID)
	if err != nil {
		return nil, 0, err
	}
	defer it.Close()
	for {
		item, ok := it.Next()
		if !ok {
			break
		}
		var role string
		switch item.Type {
		case contracts.TIUserMessage:
			role = "user"
		case contracts.TIAgentMessage:
			role = "agent"
		default:
			continue
		}
		var p struct {
			Text string `json:"text"`
		}
		raw, merr := json.Marshal(item.Payload)
		if merr != nil || json.Unmarshal(raw, &p) != nil || p.Text == "" {
			continue
		}
		entries = append(entries, HistoryEntry{Role: role, Text: p.Text})
	}
	if err := it.Err(); err != nil {
		return nil, 0, err
	}
	if len(entries) > k {
		elided = len(entries) - k
		entries = entries[len(entries)-k:]
	}
	return entries, elided, nil
}

// ThreadSummaries lists persisted threads for /resume (NEX-798): all of them
// when wd is empty, else only those whose working_dir matches.
func (b *inProcessBackend) ThreadSummaries(wd string) ([]contracts.ThreadMeta, error) {
	return b.store.List(contracts.ListFilter{WorkingDir: wd})
}

// ForkThread implements tui.ThreadForker (/fork): a thin pass-through to the
// store's Fork (internal/persistence — no copying, the child reads through
// the parent up to seq). Mirrors ThreadSummaries' shape one seam over.
func (b *inProcessBackend) ForkThread(threadID string, seq int64) (string, error) {
	meta, err := b.store.Fork(threadID, seq)
	if err != nil {
		return "", err
	}
	return meta.ThreadID, nil
}

// newInProcessStore opens (creating if absent) the operator's persistent,
// on-disk ThreadStore under ~/.agora/threads (agora-spec-persistence.md
// §1's LocalStore, rooted at the same state dir bare `agora`'s -state-dir
// flag already defaults to — see main.go's defaultSocketPath/-state-dir).
//
// U-E1 v1 note: this is the real file/JSONL store (persistence.NewLocalStore),
// not persistence.NewMemStore — threads created via the in-process launch
// path persist across restarts, same as a daemon-hosted thread would. The
// only difference from a daemon-hosted thread's durability is which
// process opened the store; the on-disk shape (and a later `agora daemon`
// pointed at the same ~/.agora root) is identical.
func newInProcessStore() (contracts.ThreadStore, error) {
	root := filepath.Join(userHomeOrDot(), ".agora")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", root, err)
	}
	store, err := persistence.NewLocalStore(root, persistence.Config{})
	if err != nil {
		return nil, err
	}
	return store, nil
}

// ensureThreadCreated makes sure threadID exists in store, calling Create
// exactly once for a never-before-seen thread and treating an
// already-created thread (persistence.ErrExists, or any prior Meta hit) as
// success — bare `agora -thread default` (the common case) reattaches to
// the SAME on-disk thread on every run, not a fresh one each time; Create
// is only meant to fire the very first time a given thread id is used.
func ensureThreadCreated(store contracts.ThreadStore, threadID, workingDir string) error {
	if _, err := store.Meta(threadID); err == nil {
		return nil
	}
	err := store.Create(contracts.ThreadMeta{
		ThreadID:   threadID,
		CreatedAt:  time.Now().UTC(),
		Profile:    "dev",
		WorkingDir: workingDir,
	})
	if err != nil && !errors.Is(err, persistence.ErrExists) {
		return err
	}
	return nil
}
