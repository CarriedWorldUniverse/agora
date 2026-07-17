package ctxmgr

import (
	"encoding/json"
	"sort"

	"github.com/CarriedWorldUniverse/agora/contracts"
)

// CurationDemotedPayload is the thread.curation.demoted event body (context
// spec §2 contract 4; curation spec §7 point 2 — "LRU episodes are not
// compaction: no information leaves the thread, only the view").
// contracts.Event.Payload carries this JSON-encoded when
// Type == contracts.EvCurationDemoted.
type CurationDemotedPayload struct {
	Keys        []string `json:"keys"`
	TokensFreed int64    `json:"tokens_freed"`
}

// CurationReadmittedPayload is the thread.curation.readmitted event body.
type CurationReadmittedPayload struct {
	Key string `json:"key"`
}

// NewCurationDemotedEvent builds the wire event for one eviction episode.
// keys is sorted for determinism (ground rule 3) regardless of input order.
func NewCurationDemotedEvent(threadID string, keys []Key, freedBytes int) contracts.Event {
	ks := make([]string, 0, len(keys))
	for _, k := range keys {
		ks = append(ks, k.String())
	}
	sort.Strings(ks)
	payload, _ := json.Marshal(CurationDemotedPayload{
		Keys:        ks,
		TokensFreed: int64(freedBytes) / BytesPerToken,
	})
	return contracts.Event{
		Type:     contracts.EvCurationDemoted,
		ThreadID: threadID,
		Payload:  payload,
	}
}

// NewCurationReadmittedEvent builds the wire event for one re-admission.
func NewCurationReadmittedEvent(threadID string, k Key) contracts.Event {
	payload, _ := json.Marshal(CurationReadmittedPayload{Key: k.String()})
	return contracts.Event{
		Type:     contracts.EvCurationReadmitted,
		ThreadID: threadID,
		Payload:  payload,
	}
}
