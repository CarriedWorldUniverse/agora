// Package daemon is the assembly around the merged internal/io machinery:
// a thread registry (SessionLookup), Session-per-thread lifecycle, and the
// real internal/approval, internal/planning, internal/persistence,
// internal/ctxmgr, internal/pod, internal/remote, internal/subagent seams
// wired around a pluggable Engine.
//
// Build unit: U18 (docs/spec/agora-spec-build.md §1).
// Spec: docs/spec/agora-spec-io.md §0a.
//
// This package deliberately does NOT reuse internal/io's ServeConn for its
// real (non-pipe) connection serving: ServeConn forwards a connecting
// client's wire-declared AttachRequest.Capabilities verbatim into
// io.Session.Attach (internal/io/protocol.go), which is exactly the
// wire-declared-capability trust this unit's design blueprint flags as
// something a real daemon must not do — capabilities must come from the
// AUTHENTICATED device's registry grant (internal/remote.AttachInfo), never
// from the client. Since io's frame codec is unexported (io is a merged
// package this unit does not touch), daemon implements its own minimal
// connection-serving loop (serve.go) against the EXPORTED protocol types
// (io.AttachRequest/ClientFrame/ServerFrame) that performs that
// authentication before ever calling io.Session.Attach. Everything else
// (Session, Attachment, RunPipe, ListenUnix/DialUnix, HandleWS/DialWS) is
// reused as-is.
package daemon
