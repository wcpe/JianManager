package ledger

import (
	"math"
	"path/filepath"
)

// ConfirmedFingerprint 将内容摘要绑定到具体分段、逻辑/物理采点和实际采样窗口。
// 这些字段只用于恢复核验，不参与事件身份或内容哈希推导。
type ConfirmedFingerprint struct {
	SegmentPath           string `json:"segment_path"`
	SegmentStartPos       uint64 `json:"segment_start_pos"`
	SegmentIdentityBytes  int64  `json:"segment_identity_bytes"`
	SegmentIdentitySHA256 string `json:"segment_identity_sha256"`
	LogicalPos            uint64 `json:"logical_pos"`
	PhysicalOffset        int64  `json:"physical_offset"`
	WindowBytes           int64  `json:"window_bytes"`
	SHA256                string `json:"sha256"`
}

// SetConfirmedFingerprint 只登记可由现存分段映射和身份核对的指纹；重复登记不增加修订。
func (l *Ledger) SetConfirmedFingerprint(key SourceKey, fingerprint ConfirmedFingerprint) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry == nil || !fingerprintMatchesSegment(entry.Segments, fingerprint) {
		return false
	}
	if entry.ConfirmedFingerprint != nil && *entry.ConfirmedFingerprint == fingerprint && entry.PositionsFingerprint == fingerprint.SHA256 && entry.PositionsFingerprintAt == fingerprint.LogicalPos {
		return true
	}
	entry.ConfirmedFingerprint = &fingerprint
	entry.PositionsFingerprint = fingerprint.SHA256
	entry.PositionsFingerprintAt = fingerprint.LogicalPos
	l.touch(key)
	return true
}

// ConfirmedFingerprint 只返回仍与当前分段证据相符的绑定；旧平面摘要不被自动升级。
func (l *Ledger) ConfirmedFingerprint(key SourceKey) (ConfirmedFingerprint, bool) {
	if l == nil {
		return ConfirmedFingerprint{}, false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	entry := l.entries[key]
	if entry == nil || entry.ConfirmedFingerprint == nil || !fingerprintMatchesSegment(entry.Segments, *entry.ConfirmedFingerprint) {
		return ConfirmedFingerprint{}, false
	}
	return *entry.ConfirmedFingerprint, true
}

func fingerprintMatchesSegment(segments []Segment, fingerprint ConfirmedFingerprint) bool {
	if fingerprint.SegmentPath == "" || fingerprint.SegmentIdentityBytes <= 0 || fingerprint.SegmentIdentitySHA256 == "" || fingerprint.SHA256 == "" || fingerprint.PhysicalOffset <= 0 || fingerprint.WindowBytes <= 0 || fingerprint.WindowBytes > fingerprint.PhysicalOffset {
		return false
	}
	for _, segment := range segments {
		if filepath.Clean(segment.Path) != filepath.Clean(fingerprint.SegmentPath) || segment.StartPos != fingerprint.SegmentStartPos || !segment.PhysicalMappingKnown || segment.IdentityBytes != fingerprint.SegmentIdentityBytes || segment.IdentitySHA256 != fingerprint.SegmentIdentitySHA256 || fingerprint.LogicalPos < segment.StartPos || fingerprint.LogicalPos > segment.EndPos {
			continue
		}
		delta := fingerprint.LogicalPos - segment.StartPos
		if segment.PhysicalStart > math.MaxInt64 || delta > math.MaxInt64-segment.PhysicalStart {
			return false
		}
		return int64(segment.PhysicalStart+delta) == fingerprint.PhysicalOffset
	}
	return false
}
