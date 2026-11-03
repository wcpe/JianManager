package ingest

import (
	"crypto/sha256"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 仅保留近期逐条 fsync 成功的身份凭据；不是全源正文缓存，淘汰只会多留 WAL 正文。
const bodyReferenceProofLimit = 1024

func (m *Manager) rememberBodyReferenceProof(key string, events []logtypes.Event) {
	if len(events) > bodyReferenceProofLimit {
		events = events[len(events)-bodyReferenceProofLimit:]
	}
	proofs := make(map[[sha256.Size]byte]struct{}, len(events))
	for i := range events {
		proofs[eventSetDigest(events[i:i+1])] = struct{}{}
	}
	m.bodyReferenceMu.Lock()
	if m.bodyReferenceProofs == nil {
		m.bodyReferenceProofs = make(map[string]map[[sha256.Size]byte]struct{})
	}
	m.bodyReferenceProofs[key] = proofs
	m.bodyReferenceMu.Unlock()
}
