// Package bus wires the nexus WebSocket transport into agora's inbox.
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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/CarriedWorldUniverse/nexus/runtime/aspect/wsasp"
	"github.com/CarriedWorldUniverse/nexus/runtime/keyfile"
	"github.com/CarriedWorldUniverse/nexus/shared/schemas"

	"github.com/CarriedWorldUniverse/agora/internal/inbox"
)

// Config bundles what the Bus needs to come online. KeyfilePath is the
// canonical input; everything else (NexusURL, AspectName, JWT) is
// derived from the validation handshake.
type Config struct {
	KeyfilePath string
	CursorDir   string // dir for the wsasp cursor file; default ~/.agora
	Logger      *slog.Logger
	Inbox       *inbox.Inbox

	// OnChat, if set, is invoked synchronously on every chat.deliver
	// alongside the inbox.Push. Used by the TUI to render the message
	// in the chat panel without having to drain the inbox (which would
	// rob the engine of its work). Optional — leave nil for headless
	// callers.
	OnChat func(it inbox.Item)
}

// Bus is the agora-side handle to the WS transport. Constructed by
// Connect, driven by Run, addressed by callers via SendChat / ReactTo.
type Bus struct {
	cfg        Config
	client     *wsasp.Client
	aspectName string
}

// Connect validates the keyfile against the nexus it points at,
// builds the wsasp client, and returns a Bus ready to Run. Does NOT
// open the WS yet — that happens in Run.
func Connect(ctx context.Context, cfg Config) (*Bus, error) {
	if cfg.KeyfilePath == "" {
		return nil, errors.New("bus: KeyfilePath required")
	}
	if cfg.Inbox == nil {
		return nil, errors.New("bus: Inbox required")
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

	b := &Bus{cfg: cfg, aspectName: vr.AspectName}

	wsCfg := wsasp.Config{
		URL:        vr.NexusURL,
		AuthToken:  vr.SessionJWT,
		AspectName: vr.AspectName,
		CursorFile: cursorFile,
		OnDeliver:  b.onDeliver,
		Register: schemas.RegisterRequest{
			Name:        vr.AspectName,
			ContextMode: schemas.ContextThread,
			Provider:    vr.Provider,
			PID:         os.Getpid(),
			StartedAt:   time.Now().UTC(),
			Model:       vr.Model,
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

// SendChat forwards to wsasp for outbound chat (spec §8.1 routing).
func (b *Bus) SendChat(ctx context.Context, content string, replyTo int64, topic string) (int64, error) {
	return b.client.SendChat(ctx, content, replyTo, topic)
}

// ReactTo forwards to wsasp.
func (b *Bus) ReactTo(ctx context.Context, msgID int64, emoji string) error {
	return b.client.ReactTo(ctx, msgID, emoji)
}

// onDeliver is the wsasp.Config.OnDeliver callback. Each chat.deliver
// frame becomes one inbox.Item with Source: "chat".
func (b *Bus) onDeliver(msg wsasp.DeliveredMessage) {
	received, err := time.Parse(time.RFC3339, msg.ReceivedAt)
	if err != nil {
		received = time.Now().UTC()
	}
	it := inbox.Item{
		Source:     inbox.SourceChat,
		From:       msg.From,
		Content:    msg.Content,
		MsgID:      msg.ID,
		ReplyTo:    msg.ReplyTo,
		ThreadRoot: msg.ThreadRoot,
		Reason:     msg.Reason,
		ReceivedAt: received,
	}
	b.cfg.Inbox.Push(it)
	if b.cfg.OnChat != nil {
		b.cfg.OnChat(it)
	}
	b.cfg.Logger.Debug("inbox push (chat)",
		"from", msg.From,
		"msg_id", msg.ID,
		"reason", msg.Reason,
		"replay", msg.Replay)
}
