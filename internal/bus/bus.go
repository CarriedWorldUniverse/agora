// Package bus wires the nexus WebSocket transport into agora.
//
// Owns the long-lived wsasp.Client and the keyfile validation step.
// The OnDeliver callback installed on the wsasp client pushes each
// chat.deliver frame into the shared inbox with Source: "chat" (spec
// §14). Out-going chat (spec §8) flows the other way via the same
// client (SendChat / ReactTo / ReadThread).
//
// Reconnect, cursor persistence, and outbound buffering during a
// disconnect are all owned by wsasp.Client — agora just plumbs config
// in and consumes the OnDeliver stream.
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/nexus/nexus/frames"
	"github.com/CarriedWorldUniverse/nexus/runtime/aspect/wsasp"
	"github.com/CarriedWorldUniverse/nexus/runtime/keyfile"
	"github.com/CarriedWorldUniverse/nexus/shared/schemas"
	"github.com/google/uuid"
)

// ChatItem is the payload bus.Config.OnChat receives per chat.deliver
// frame. Decoupled from any agora-internal queue type so callers
// (main.go, UI) can map it into whatever shape they need (funnel
// inbox push, UI render event, etc.).
type ChatItem struct {
	From       string
	Content    string
	MsgID      int64
	ReplyTo    int64
	ThreadRoot int64
	Reason     string
	ReceivedAt time.Time
}

// Config bundles what the Bus needs to come online. KeyfilePath is the
// canonical input; everything else (NexusURL, AspectName, JWT) is
// derived from the validation handshake.
type Config struct {
	KeyfilePath string
	CursorDir   string // dir for the wsasp cursor file; default ~/.agora
	Logger      *slog.Logger

	// OnChat, if set, is invoked synchronously on every chat.deliver.
	// The caller decides what to do with the item (push to funnel,
	// render in UI, etc.). Required for any caller that wants
	// inbound chat (which is all of them in practice).
	OnChat func(it ChatItem)

	// OnEscalationRequest, if set, is invoked when the broker pushes an
	// escalation.request frame (a native-API aspect's funnel asking a
	// human to approve/deny a tool call). OPTIONAL: nil means the frame
	// is ignored. agora wires this to surface an approval modal in the
	// TUI. requestID is the request envelope's correlation id; the
	// caller echoes it back via SendEscalationDecision so the blocked
	// aspect's Request resolves against the right pending call.
	OnEscalationRequest func(it EscalationItem)
}

// EscalationItem is the payload bus.Config.OnEscalationRequest receives
// per escalation.request frame. Decoupled from the wire payload so the
// UI layer doesn't import nexus frames directly.
type EscalationItem struct {
	RequestID string
	Aspect    string
	Tool      string
	Args      json.RawMessage
	Reason    string
}

// Bus is the agora-side handle to the WS transport. Constructed by
// Connect, driven by Run, addressed by callers via SendChat / ReactTo.
type Bus struct {
	cfg          Config
	client       *wsasp.Client
	aspectName   string
	sessionID    string
	provider     string
	model        string
	systemPrompt string
}

// Connect validates the keyfile against the nexus it points at,
// builds the wsasp client, and returns a Bus ready to Run. Does NOT
// open the WS yet — that happens in Run.
func Connect(ctx context.Context, cfg Config) (*Bus, error) {
	if cfg.KeyfilePath == "" {
		return nil, errors.New("bus: KeyfilePath required")
	}
	if cfg.OnChat == nil {
		return nil, errors.New("bus: OnChat required")
	}
	if cfg.Logger == nil {
		return nil, errors.New("bus: Logger required")
	}

	kf, err := keyfile.Load(cfg.KeyfilePath)
	if err != nil {
		return nil, fmt.Errorf("bus: load keyfile: %w", err)
	}

	kc := keyfile.NewClient()
	vr, err := kc.Validate(ctx, kf)
	if err != nil {
		return nil, fmt.Errorf("bus: validate keyfile: %w", err)
	}
	cfg.Logger.Info("keyfile validated",
		"aspect", vr.AspectName,
		"nexus_url", vr.NexusURL,
		"jwt_expires_at", vr.SessionExpiresAt.Format(time.RFC3339))

	cursorDir := cfg.CursorDir
	if cursorDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("bus: resolve home: %w", err)
		}
		cursorDir = filepath.Join(home, ".agora")
	}
	if err := os.MkdirAll(cursorDir, 0o700); err != nil {
		return nil, fmt.Errorf("bus: mkdir cursor dir: %w", err)
	}
	cursorFile := wsasp.CursorFileForAspect(cursorDir)

	sessionID := uuid.NewString()
	b := &Bus{
		cfg:          cfg,
		aspectName:   vr.AspectName,
		sessionID:    sessionID,
		provider:     vr.Provider,
		model:        vr.Model,
		systemPrompt: composeSystemPrompt(vr),
	}

	wsCfg := wsasp.Config{
		URL:        vr.NexusURL,
		AuthToken:  vr.SessionJWT,
		AspectName: vr.AspectName,
		CursorFile: cursorFile,
		OnDeliver:  b.onDeliver,
		// Only register the escalation callback when a consumer asked for
		// it — nil here means wsasp ignores escalation.request frames
		// (matches wsasp's documented "common case").
		OnEscalationRequest: b.escalationRequestCallback(),
		Register: schemas.RegisterRequest{
			Name:        vr.AspectName,
			ContextMode: schemas.ContextGlobal,
			Provider:    vr.Provider,
			PID:         os.Getpid(),
			StartedAt:   time.Now().UTC(),
			Model:       vr.Model,
			SessionID:   sessionID,
		},
	}
	wc, err := wsasp.NewClient(wsCfg)
	if err != nil {
		return nil, fmt.Errorf("bus: build wsasp: %w", err)
	}
	b.client = wc
	return b, nil
}

// Run drives the WS lifecycle until ctx is cancelled.
func (b *Bus) Run(ctx context.Context) error {
	b.cfg.Logger.Info("bus running", "aspect", b.aspectName)
	return b.client.Run(ctx)
}

// AspectName is the canonical aspect id pulled from validation.
func (b *Bus) AspectName() string { return b.aspectName }

// Provider is the bridle provider id ("claude-code", "claude-api",
// "openai-api", "ollama-local") pulled from validation.
func (b *Bus) Provider() string { return b.provider }

// Model is the provider-specific model id from validation.
func (b *Bus) Model() string { return b.model }

// Connected reports whether the WS is currently open. Delegates to
// wsasp.Client.Connected; cheap to poll. Used by the TUI status line.
func (b *Bus) Connected() bool { return b.client.Connected() }

// SystemPrompt is the composed personality bundle: central nexus_md
// ⊕ aspect personality (composed, or nexus_md ⊕ soul_md ⊕ primer_md
// fallback). Empty if the Nexus didn't return any of those (legacy
// or unprovisioned aspect). Mirrors agentfunnel's composeSystemPrompt.
func (b *Bus) SystemPrompt() string { return b.systemPrompt }

// composeSystemPrompt layers a validation result into the four-section
// concat per spec §3 (personality decomposition):
//
//	central.nexus_md ⊕ aspect.nexus_md ⊕ aspect.soul_md ⊕ aspect.primer_md
//
// When personality.composed is non-empty (Part 7 renderer populated
// it), uses central + composed instead — the renderer must NOT
// double-bake central into composed (no-double-bake invariant pinned
// in nexus/frame/embed_personality_test.go).
//
// Mirrored from runtime/cmd/agentfunnel/main.go.composeSystemPrompt;
// duplicated rather than imported because agentfunnel's main isn't a
// library. Keep in sync if either side changes.
func composeSystemPrompt(res *keyfile.ValidationResult) string {
	if res == nil {
		return ""
	}
	parts := make([]string, 0, 4)
	if res.CentralNexusMD != "" {
		parts = append(parts, res.CentralNexusMD)
	}
	if res.Personality.Composed != "" {
		parts = append(parts, res.Personality.Composed)
	} else {
		if res.Personality.NexusMD != "" {
			parts = append(parts, res.Personality.NexusMD)
		}
		if res.Personality.SoulMD != "" {
			parts = append(parts, res.Personality.SoulMD)
		}
		if res.Personality.PrimerMD != "" {
			parts = append(parts, res.Personality.PrimerMD)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// SendChat forwards to wsasp for outbound chat (spec §8.1 routing).
func (b *Bus) SendChat(ctx context.Context, content string, replyTo int64, topic string) (int64, error) {
	return b.client.SendChat(ctx, content, replyTo, topic)
}

// ReactTo forwards to wsasp.
func (b *Bus) ReactTo(ctx context.Context, msgID int64, emoji string) error {
	return b.client.ReactTo(ctx, msgID, emoji)
}

// Deregister sends a best-effort deregister frame so nexus knows the
// aspect is leaving cleanly. Fire-and-forget by design — the WS is
// usually about to close, so waiting for the ack would block exit on
// a network round-trip. Reason is plain text, displayed in nexus's
// roster history.
func (b *Bus) Deregister(ctx context.Context, reason string) error {
	env, err := frames.NewRequest(frames.KindDeregister, frames.DeregisterPayload{
		DeregisterRequest: schemas.DeregisterRequest{
			Name:      b.aspectName,
			SessionID: b.sessionID,
			Reason:    reason,
		},
	})
	if err != nil {
		return fmt.Errorf("build deregister frame: %w", err)
	}
	return b.client.SendBestEffort(ctx, env)
}

// onDeliver is the wsasp.Config.OnDeliver callback. Each chat.deliver
// frame becomes one ChatItem surfaced via Config.OnChat.
func (b *Bus) onDeliver(msg wsasp.DeliveredMessage) {
	received, err := time.Parse(time.RFC3339, msg.ReceivedAt)
	if err != nil {
		received = time.Now().UTC()
	}
	if b.cfg.OnChat != nil {
		b.cfg.OnChat(ChatItem{
			From:       msg.From,
			Content:    msg.Content,
			MsgID:      msg.ID,
			ReplyTo:    msg.ReplyTo,
			ThreadRoot: msg.ThreadRoot,
			Reason:     msg.Reason,
			ReceivedAt: received,
		})
	}
	b.cfg.Logger.Debug("chat.deliver",
		"from", msg.From,
		"msg_id", msg.ID,
		"reason", msg.Reason,
		"replay", msg.Replay)
}

// escalationRequestCallback returns the wsasp.Config.OnEscalationRequest
// func, or nil when the consumer didn't wire one. Returning nil (rather
// than a func that no-ops) keeps wsasp's "callback nil → ignore frame"
// fast path intact.
func (b *Bus) escalationRequestCallback() func(frames.EscalationRequestPayload, string) {
	if b.cfg.OnEscalationRequest == nil {
		return nil
	}
	return b.onEscalationRequest
}

// onEscalationRequest is the wsasp.Config.OnEscalationRequest callback.
// Each broker-pushed escalation.request becomes one EscalationItem
// surfaced via Config.OnEscalationRequest. requestID is the request
// envelope's correlation id (env.ID); the caller must echo it back via
// SendEscalationDecision so the blocked aspect's Request resolves.
func (b *Bus) onEscalationRequest(payload frames.EscalationRequestPayload, requestID string) {
	b.cfg.Logger.Info("escalation.request",
		"aspect", payload.Aspect,
		"tool", payload.Tool,
		"request_id", requestID)
	if b.cfg.OnEscalationRequest != nil {
		b.cfg.OnEscalationRequest(EscalationItem{
			RequestID: requestID,
			Aspect:    payload.Aspect,
			Tool:      payload.Tool,
			Args:      payload.Args,
			Reason:    payload.Reason,
		})
	}
}

// BuildEscalationDecision constructs the escalation.decision envelope
// the operator sends back to the broker. Exported so the decision-frame
// shape is unit-testable without a live websocket.
//
// CRITICAL wire contract (verified against nexus broker/escalation.go):
// the correlation id goes in the PAYLOAD field RequestID and the
// envelope InReplyTo MUST stay empty. The broker's read loop routes any
// frame whose envelope InReplyTo is set through routeResponse (a
// broker-side pending map) BEFORE the escalation handler runs — and
// there is no broker-side pending entry, so such a frame is dropped.
// handleEscalationDecisionFrame reads payload.RequestID and stamps
// InReplyTo only on the frame it FORWARDS to the aspect. So we use
// frames.New (not frames.NewResponse) and never touch env.InReplyTo.
func BuildEscalationDecision(aspect, decision, operator, note, requestID string) (frames.Envelope, error) {
	return frames.New(frames.KindEscalationDecision, frames.EscalationDecisionPayload{
		Aspect:    aspect,
		Decision:  decision,
		Operator:  operator,
		Note:      note,
		RequestID: requestID,
	})
}

// SendEscalationDecision sends the operator's answer to an
// escalation.request back to the broker, which routes it to the blocked
// aspect. decision must be frames.EscalationApprove or
// frames.EscalationDeny; note is optional free text surfaced to the
// model. Best-effort send (mirrors Deregister): the escalation is an
// urgent low-volume signal and the aspect's own Request fails on
// disconnect, so replaying a stale decision after reconnect would be
// wrong.
func (b *Bus) SendEscalationDecision(ctx context.Context, aspect, decision, operator, note, requestID string) error {
	env, err := BuildEscalationDecision(aspect, decision, operator, note, requestID)
	if err != nil {
		return fmt.Errorf("build escalation.decision frame: %w", err)
	}
	return b.client.SendBestEffort(ctx, env)
}
