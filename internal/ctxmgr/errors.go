package ctxmgr

import "errors"

// Sentinel errors.
var (
	// ErrKeyNotFound: Readmit/Touch referenced a key the ledger has never
	// seen (nothing to re-admit — same as a never-seen file, §3b "falling
	// off the tracked layer loses nothing durable").
	ErrKeyNotFound = errors.New("ctxmgr: key not in ledger")
	// ErrNoDiskReader: a stale, disk-backed key needed re-admission (§3b
	// re-admission source 2) but no DiskReader was configured.
	ErrNoDiskReader = errors.New("ctxmgr: stale disk-backed key needs re-admission but no DiskReader is configured")
)
