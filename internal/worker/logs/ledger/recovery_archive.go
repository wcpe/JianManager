package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// RecoveryArchiveRecord 保存完整 CLEANED 审计；历史存储不属于活动账本的常量空间承诺。
type RecoveryArchiveRecord struct {
	Key         SourceKey   `json:"key"`
	Ref         RecoveryRef `json:"ref"`
	Fingerprint string      `json:"fingerprint"`
}

// RecoveryArchiveReceipt 是仓储确认持久归档成功后返回的记录级回执。
type RecoveryArchiveReceipt struct {
	Key         SourceKey `json:"key"`
	SegmentID   string    `json:"segment_id"`
	Fingerprint string    `json:"fingerprint"`
	ArchiveID   string    `json:"archive_id"`
}

// RecoveryArchiveStore 独立持久保存审计并支持 SegmentID 幂等重绑；失败不得返回成功回执。
type RecoveryArchiveStore interface {
	ArchiveRecovery(context.Context, []RecoveryArchiveRecord) ([]RecoveryArchiveReceipt, error)
	LookupRecovery(context.Context, SourceKey, string) (RecoveryArchiveRecord, RecoveryArchiveReceipt, bool, error)
}

type recoveryUseKey struct {
	Key       SourceKey
	SegmentID string
}

// RecoveryArchiveFingerprint 绑定源身份和完整恢复引用，拒绝用候选下标或可变修订号确认归档。
func RecoveryArchiveFingerprint(key SourceKey, ref RecoveryRef) string {
	data, err := json.Marshal(struct {
		Key SourceKey   `json:"key"`
		Ref RecoveryRef `json:"ref"`
	}{Key: key, Ref: ref})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// ValidateRecoveryArchiveRecord 校验完整审计与候选指纹，不能把非 CLEANED 状态归档后摘除。
func ValidateRecoveryArchiveRecord(record RecoveryArchiveRecord) error {
	if record.Key.LogSourceID == "" || record.Key.SourceGeneration == "" || !recoveryArchiveProofValid(record.Ref) || record.Fingerprint == "" || record.Fingerprint != RecoveryArchiveFingerprint(record.Key, record.Ref) {
		return fmt.Errorf("ledger: invalid recovery archive record")
	}
	return nil
}

// SetRecoveryArchiveStore 绑定独立历史仓储，供锁外归档和已清理 SegmentID 的幂等重绑使用。
func (l *Ledger) SetRecoveryArchiveStore(store RecoveryArchiveStore) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recoveryArchive = store
	l.recoveryArchiveEpoch++
}

// AcquireRecoveryUse 为真实恢复消费者或租约登记使用权；释放函数可重复调用。
// 摘除与登记共享短锁，已摘除的引用不能被新的 hold 或消费者成功复活。
func (l *Ledger) AcquireRecoveryUse(key SourceKey, segmentID, consumer string) (func(), error) {
	if l == nil || strings.TrimSpace(consumer) == "" {
		return nil, fmt.Errorf("ledger: recovery consumer is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry == nil {
		return nil, fmt.Errorf("ledger: source %s not registered", key)
	}
	found := false
	for _, ref := range entry.RecoveryRefs {
		if ref.SegmentID == segmentID {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("ledger: recovery segment %q not found", segmentID)
	}
	if l.recoveryUses == nil {
		l.recoveryUses = make(map[recoveryUseKey]uint64)
	}
	useKey := recoveryUseKey{Key: key, SegmentID: segmentID}
	l.recoveryUses[useKey]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.recoveryUses[useKey] <= 1 {
				delete(l.recoveryUses, useKey)
			} else {
				l.recoveryUses[useKey]--
			}
		})
	}, nil
}

// RecoveryArchiveCandidates 有界选择已不承担活动责任且无 hold/使用者的完整审计候选。
func (l *Ledger) RecoveryArchiveCandidates(key SourceKey, limit int) []RecoveryArchiveRecord {
	if l == nil || limit <= 0 {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	entry := l.entries[key]
	if entry == nil {
		return nil
	}
	var records []RecoveryArchiveRecord
	for _, ref := range entry.RecoveryRefs {
		if !l.recoveryArchiveEligibleLocked(entry, ref) {
			continue
		}
		records = append(records, RecoveryArchiveRecord{Key: key, Ref: ref, Fingerprint: RecoveryArchiveFingerprint(key, ref)})
		if len(records) == limit {
			break
		}
	}
	return records
}

// CommitRecoveryArchive 先验真全部回执，再短锁复核当前候选；并发 hold/租约变化只会多保留。
func (l *Ledger) CommitRecoveryArchive(records []RecoveryArchiveRecord, receipts []RecoveryArchiveReceipt) (int, error) {
	if l == nil {
		return 0, fmt.Errorf("ledger: nil ledger")
	}
	type archiveKey struct {
		Key       SourceKey
		SegmentID string
	}
	proofs := make(map[archiveKey]RecoveryArchiveReceipt, len(receipts))
	for _, receipt := range receipts {
		if receipt.ArchiveID == "" || receipt.Fingerprint == "" {
			return 0, fmt.Errorf("ledger: incomplete recovery archive receipt")
		}
		id := archiveKey{Key: receipt.Key, SegmentID: receipt.SegmentID}
		if previous, ok := proofs[id]; ok && previous != receipt {
			return 0, fmt.Errorf("ledger: conflicting recovery archive receipts")
		}
		proofs[id] = receipt
	}
	candidates := make(map[archiveKey]RecoveryArchiveRecord, len(records))
	for _, record := range records {
		if err := ValidateRecoveryArchiveRecord(record); err != nil {
			return 0, err
		}
		id := archiveKey{Key: record.Key, SegmentID: record.Ref.SegmentID}
		receipt, ok := proofs[id]
		if !ok || receipt.Fingerprint != record.Fingerprint {
			return 0, fmt.Errorf("ledger: missing or mismatched recovery archive receipt")
		}
		candidates[id] = record
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	removed := 0
	for id, record := range candidates {
		entry := l.entries[id.Key]
		if entry == nil {
			continue
		}
		for i, ref := range entry.RecoveryRefs {
			if ref.SegmentID != id.SegmentID || !l.recoveryArchiveEligibleLocked(entry, ref) || RecoveryArchiveFingerprint(id.Key, ref) != record.Fingerprint {
				continue
			}
			copy(entry.RecoveryRefs[i:], entry.RecoveryRefs[i+1:])
			entry.RecoveryRefs[len(entry.RecoveryRefs)-1] = RecoveryRef{}
			entry.RecoveryRefs = entry.RecoveryRefs[:len(entry.RecoveryRefs)-1]
			l.touch(id.Key)
			l.recoveryArchiveEpoch++
			removed++
			break
		}
	}
	return removed, nil
}

// ArchiveCleanedRecovery 按候选→锁外持久归档→回执复核摘除执行；失败保留完整活动引用。
func (l *Ledger) ArchiveCleanedRecovery(ctx context.Context, key SourceKey, limit int) (int, error) {
	if l == nil {
		return 0, fmt.Errorf("ledger: nil ledger")
	}
	l.mu.RLock()
	store := l.recoveryArchive
	l.mu.RUnlock()
	if store == nil {
		return 0, fmt.Errorf("ledger: recovery archive store is not configured")
	}
	records := l.RecoveryArchiveCandidates(key, limit)
	if len(records) == 0 {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	receipts, err := store.ArchiveRecovery(ctx, records)
	if err != nil {
		return 0, err
	}
	return l.CommitRecoveryArchive(records, receipts)
}

func recoveryArchiveProofValid(ref RecoveryRef) bool {
	return ref.SegmentID != "" && ref.State == logtypes.RecoveryCleaned && ref.CoversTo >= ref.CoversFrom && !ref.HasHold && logtypes.ValidReleaseReason(ref.ReleaseReason) && ref.ResponsibilityReceiver != "" && ref.ResponsibilityReceiver != ref.SegmentID && !strings.HasPrefix(ref.ResponsibilityReceiver, "self:")
}

func (l *Ledger) recoveryArchiveEligibleLocked(entry *Entry, ref RecoveryRef) bool {
	return recoveryArchiveProofValid(ref) && ref.CoversTo <= entry.Positions.Reclaim && l.recoveryUses[recoveryUseKey{Key: entry.Key, SegmentID: ref.SegmentID}] == 0
}

func lookupRecoveryArchive(store RecoveryArchiveStore, key SourceKey, ref RecoveryRef) (bool, error) {
	if store == nil {
		return false, nil
	}
	record, receipt, exists, err := store.LookupRecovery(context.Background(), key, ref.SegmentID)
	if err != nil || !exists {
		return false, err
	}
	if err := ValidateRecoveryArchiveRecord(record); err != nil {
		return false, err
	}
	if record.Key != key || record.Ref.SegmentID != ref.SegmentID || receipt.Key != key || receipt.SegmentID != ref.SegmentID || receipt.Fingerprint != record.Fingerprint || receipt.ArchiveID == "" {
		return false, fmt.Errorf("ledger: invalid recovery archive lookup receipt")
	}
	if record.Ref.Path != ref.Path || record.Ref.CoversFrom != ref.CoversFrom || record.Ref.CoversTo != ref.CoversTo {
		return false, fmt.Errorf("ledger: recovery segment %s identity conflict", ref.SegmentID)
	}
	return true, nil
}

func (l *Ledger) registerRecovery(key SourceKey, ref RecoveryRef) error {
	if l == nil {
		return fmt.Errorf("ledger: nil ledger")
	}
	for {
		l.mu.RLock()
		store, epoch := l.recoveryArchive, l.recoveryArchiveEpoch
		l.mu.RUnlock()
		archived, err := lookupRecoveryArchive(store, key, ref)
		if err != nil {
			return err
		}
		l.mu.Lock()
		if epoch != l.recoveryArchiveEpoch {
			l.mu.Unlock()
			continue
		}
		entry := l.entries[key]
		if entry == nil {
			l.mu.Unlock()
			return fmt.Errorf("ledger: source %s not registered", key)
		}
		for _, existing := range entry.RecoveryRefs {
			if existing.SegmentID != ref.SegmentID {
				continue
			}
			// 活动重绑保留既有保守修订契约，但不覆盖已推进状态和 hold。
			l.touch(key)
			if existing.Path != ref.Path || existing.CoversFrom != ref.CoversFrom || existing.CoversTo != ref.CoversTo {
				l.mu.Unlock()
				return fmt.Errorf("ledger: recovery segment %s identity conflict", ref.SegmentID)
			}
			l.mu.Unlock()
			return nil
		}
		if archived && ref.HasHold {
			l.mu.Unlock()
			return fmt.Errorf("ledger: cannot bind a new hold to archived recovery segment %s", ref.SegmentID)
		}
		if !archived {
			entry.RecoveryRefs = append(entry.RecoveryRefs, ref)
			l.touch(key)
		}
		l.mu.Unlock()
		return nil
	}
}
