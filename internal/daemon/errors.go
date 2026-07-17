package daemon

import "errors"

// Sentinel errors. Every one of these is a REFUSAL (fail closed), matching
// the house discipline of the packages this one assembles (pod/errors.go,
// planning/errors.go).
var (
	// ErrUnknownThread is returned by Session when threadID has no
	// registered meta (the store has never heard of it).
	ErrUnknownThread = errors.New("daemon: unknown thread")

	// ErrNoEngineFactory is returned by NewDaemon-produced daemons that were
	// never given an EngineFactory — a daemon cannot run a thread's turns
	// without one.
	ErrNoEngineFactory = errors.New("daemon: no engine factory configured")

	// ErrExpectedAttach mirrors io.ErrExpectedAttach for daemon's own
	// connection-serving loop (serve.go) — a session-protocol connection's
	// first frame must be an attach.
	ErrExpectedAttach = errors.New("daemon: session protocol: first frame must be attach")

	// ErrUnknownDevice is returned when a connecting client's declared
	// client_id does not resolve to an enrolled, non-revoked device in the
	// daemon's registry. Fail-closed: an unrecognized/revoked device is
	// refused rather than trusted with whatever capabilities it claims on
	// the wire (doc.go's CRITICAL note).
	ErrUnknownDevice = errors.New("daemon: unknown or revoked device")
)
