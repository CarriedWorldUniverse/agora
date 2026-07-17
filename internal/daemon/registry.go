package daemon

import (
	"fmt"

	agoraio "github.com/CarriedWorldUniverse/agora/internal/io"
)

// Session resolves threadID to its Session, minting one on first use over
// the thread's persisted meta (io/protocol.go's SessionLookup seam this
// package is the real implementation of). Once minted, a Session lives for
// this Daemon's lifetime (until Close).
func (d *Daemon) Session(threadID string) (*agoraio.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s, ok := d.sessions[threadID]; ok {
		return s, nil
	}
	meta, err := d.store.Meta(threadID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUnknownThread, threadID, err)
	}
	if d.engineFor == nil {
		return nil, ErrNoEngineFactory
	}
	engine := d.engineFor(threadID, meta)
	sess := agoraio.NewSession(d.baseCtx, threadID, engine)
	d.sessions[threadID] = sess
	return sess, nil
}
