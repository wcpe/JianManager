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

func (m *Manager) bodyReferencePredicate(key string) func(logtypes.Event) bool {
	m.bodyReferenceMu.Lock()
	known := m.bodyReferenceProofs[key]
	proofs := make(map[[sha256.Size]byte]struct{}, len(known))
	for digest := range known {
		proofs[digest] = struct{}{}
	}
	m.bodyReferenceMu.Unlock()
	if len(proofs) == 0 || m.events == nil {
		return func(logtypes.Event) bool { return false }
	}
	// 打开同一不可变段快照以确认文件与完整字节界仍可取；缺失或损坏不降正文引用。
	snapshot, err := m.events.Snapshot(key)
	if err != nil {
		return func(logtypes.Event) bool { return false }
	}
	available := snapshot.Count() > 0
	_ = snapshot.Close()
	return func(ev logtypes.Event) bool {
		if !available {
			return false
		}
		_, ok := proofs[eventSetDigest([]logtypes.Event{ev})]
		return ok
	}
}
