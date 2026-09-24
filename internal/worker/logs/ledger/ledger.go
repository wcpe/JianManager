// Package ledger 实现 FR-474 采集账本：按 (log_source_id, source_generation)
// 保存四水位、投递状态、恢复分段引用、缺口与轮转关联。
// 字段与状态语义以 docs/specs/worker-log-platform-contract/spec.md 为准。
package ledger

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// SourceKey 账本主键：逻辑日志源 + 源分段 generation。
type SourceKey struct {
	LogSourceID      string `json:"log_source_id"`
	SourceGeneration string `json:"source_generation"`
}

func (k SourceKey) String() string { return k.LogSourceID + "/" + k.SourceGeneration }

// SegmentKind 源分段物理形态。
type SegmentKind string

const (
	SegmentLive    SegmentKind = "live"    // latest.log 等当前写入文件
	SegmentRotated SegmentKind = "rotated" // 轮转后的明文分段
	SegmentGzip    SegmentKind = "gzip"    // 压缩归档分段
	SegmentStdio   SegmentKind = "stdio"   // STDIO_PRIMARY 受管 Raw
)

// Segment 账本中的一个物理源分段。
type Segment struct {
	Path            string      `json:"path"`
	Kind            SegmentKind `json:"kind"`
	StartPos        uint64      `json:"start_pos"`                   // 相对逻辑源的起始位置
	EndPos          uint64      `json:"end_pos"`                     // 已登记的结束位置（exclusive）
	IdentityBytes   int64       `json:"identity_bytes,omitempty"`    // live 文件稳定前缀长度
	IdentitySHA256  string      `json:"identity_sha256,omitempty"`   // 检测同路径替换，不能参与 event_id
	ArchiveObjectID string      `json:"archive_object_id,omitempty"` // provenance only
	Imported        bool        `json:"imported"`
	ImportError     string      `json:"import_error,omitempty"`
	ObservedSize    int64       `json:"observed_size,omitempty"`
	ObservedModNano int64       `json:"observed_mod_unix_nano,omitempty"`
	ParserVersion   string      `json:"parser_version"`
}

// RotationLink latest.log → rotated → .gz 的逻辑源连续关联。
type RotationLink struct {
	FromPath string `json:"from_path"`
	ToPath   string `json:"to_path"`
	// EndPosFrom 关联建立时前段结束位置；ArchiveImporter 接管前必须已登记。
	EndPosFrom uint64 `json:"end_pos_from"`
	// Generation 轮转/压缩沿用被接管内容的 generation。
	Generation string `json:"generation"`
	LinkedAt   int64  `json:"linked_at"`
}

// Gap 采集/投递缺口：容量暂停、坏记录或权限失败时登记，禁止静默丢弃。
type Gap struct {
	StartPos   uint64 `json:"start_pos"`
	EndPos     uint64 `json:"end_pos"`
	Reason     string `json:"reason"`
	Detail     string `json:"detail,omitempty"`
	Resolved   bool   `json:"resolved,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

func (l *Ledger) UnresolvedGapCount(key SourceKey) int {
	entry := l.Get(key)
	if entry == nil {
		return 0
	}
	count := 0
	for _, gap := range entry.Gaps {
		if !gap.Resolved {
			count++
		}
	}
	return count
}

func (l *Ledger) ResolveGapsThrough(key SourceKey, position uint64, resolution string) (int, error) {
	if resolution == "" {
		return 0, fmt.Errorf("ledger: gap resolution is required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, err := l.require(key)
	if err != nil {
		return 0, err
	}
	resolved := 0
	for index := range entry.Gaps {
		if !entry.Gaps[index].Resolved && entry.Gaps[index].EndPos <= position {
			entry.Gaps[index].Resolved = true
			entry.Gaps[index].Resolution = resolution
			resolved++
		}
	}
	return resolved, nil
}

// RecoveryRef 受管恢复分段在账本中的引用。
type RecoveryRef struct {
	SegmentID     string                        `json:"segment_id"`
	Path          string                        `json:"path"`
	State         logtypes.RecoverySegmentState `json:"state"`
	ReleaseReason logtypes.ReleaseReason        `json:"release_reason,omitempty"`
	// ResponsibilityReceiver 责任接收者（RELEASED 前必填）。
	ResponsibilityReceiver string `json:"responsibility_receiver,omitempty"`
	// CoversFrom/CoversTo 覆盖的 WAL 前缀（exclusive end）。
	CoversFrom uint64 `json:"covers_from"`
	CoversTo   uint64 `json:"covers_to"`
	HasHold    bool   `json:"has_hold"`
	// RetentionDeadlineUnixSec 保留约束；未解除责任前不得普通到期清理。
	RetentionDeadlineUnixSec int64 `json:"retention_deadline_unix_sec,omitempty"`
}

// DeliveryBatch 批次级请求结果；位置与状态分开保存。
type DeliveryBatch struct {
	Start uint64                 `json:"start"`
	End   uint64                 `json:"end"`
	State logtypes.DeliveryState `json:"state"`
}

// Entry 单一 (log_source_id, source_generation) 账本条目。
type Entry struct {
	Key       SourceKey               `json:"key"`
	Identity  logtypes.SourceIdentity `json:"identity"`
	Positions logtypes.Positions      `json:"positions"`

	// DeliveryState 最近批次投递状态；不能用位置编码 UNKNOWN。
	DeliveryState logtypes.DeliveryState `json:"delivery_state"`
	// DeliveryBatches 批次结果；delivery_position 只反映连续前缀。
	DeliveryBatches []DeliveryBatch `json:"delivery_batches"`

	Segments     []Segment      `json:"segments"`
	Rotations    []RotationLink `json:"rotations"`
	Gaps         []Gap          `json:"gaps"`
	RecoveryRefs []RecoveryRef  `json:"recovery_refs"`
	ErrorCount   int            `json:"error_count"`
	IngestSeq    uint64         `json:"ingest_seq"`
	// AcquirePaused 容量/预算耗尽时置位；暂停期间不得静默丢数据。
	AcquirePaused bool   `json:"acquire_paused"`
	PauseReason   string `json:"pause_reason,omitempty"`
}

// Ledger 线程安全采集账本。
type Ledger struct {
	mu      sync.RWMutex
	entries map[SourceKey]*Entry
	// nowUnix 可注入时钟，便于测试。
	nowUnix func() int64
}

// New 创建空账本。
func New() *Ledger {
	return &Ledger{
		entries: make(map[SourceKey]*Entry),
		nowUnix: func() int64 { return 0 },
	}
}

// Snapshot 返回全部账本条目副本，供 Worker 持久化和崩溃恢复使用。
func (l *Ledger) Snapshot() []Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Entry, 0, len(l.entries))
	for _, entry := range l.entries {
		if entry != nil {
			out = append(out, *entry.clone())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key.String() < out[j].Key.String() })
	return out
}

// Restore 用持久快照恢复账本。快照是 Worker 自己写出的受信格式，仍执行关键字段校验。
func (l *Ledger) Restore(entries []Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, entry := range entries {
		if entry.Key.LogSourceID == "" || entry.Key.SourceGeneration == "" {
			return fmt.Errorf("ledger: invalid restore key %q", entry.Key.String())
		}
		copy := entry.clone()
		// Recompute the derived delivery prefix after replay. A single-byte
		// source line delimiter between adjacent event spans is known coverage,
		// while larger holes remain unresolved and block the prefix.
		copy.Positions.Delivery = contiguousDeliveryEnd(copy.DeliveryBatches, copy.Positions.Reclaim)
		l.entries[entry.Key] = copy
	}
	return nil
}

// SetClock 注入时钟（测试用）。
func (l *Ledger) SetClock(fn func() int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nowUnix = fn
}

// Ensure 注册或取回账本条目。同一 key 幂等。
func (l *Ledger) Ensure(key SourceKey, identity logtypes.SourceIdentity) *Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[key]; ok {
		return e.clone()
	}
	if identity.LogSourceID == "" {
		identity.LogSourceID = key.LogSourceID
	}
	if identity.SourceGeneration == "" {
		identity.SourceGeneration = key.SourceGeneration
	}
	e := &Entry{
		Key:           key,
		Identity:      identity,
		DeliveryState: logtypes.DeliveryNotSent,
	}
	l.entries[key] = e
	return e.clone()
}

// Get 返回条目副本；不存在时返回 nil。
func (l *Ledger) Get(key SourceKey) *Entry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.entries[key]
	if !ok {
		return nil
	}
	return e.clone()
}

// AdvanceRead 推进运行时读指针。崩溃后允许回退重建。
func (l *Ledger) AdvanceRead(key SourceKey, pos uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	// read_position 可回退：仅记录本次指针，不强制单调。
	e.Positions.Read = pos
	return nil
}

// AdvanceDurable 推进 durable_position。同一 generation 内单调前进。
func (l *Ledger) AdvanceDurable(key SourceKey, pos uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	if pos < e.Positions.Durable {
		return fmt.Errorf("ledger: durable_position must be monotonic within generation: %d < %d", pos, e.Positions.Durable)
	}
	e.Positions.Durable = pos
	return nil
}

// RecordDelivery 登记批次请求级结果。
// HTTP 2xx → REQUEST_DONE；响应丢失 → UNKNOWN（保留恢复责任，不推进 reclaim）。
// delivery_position 取“所有字节均有请求结果”的连续前缀末端，乱序响应不得越过未解决空洞。
func (l *Ledger) RecordDelivery(key SourceKey, start, end uint64, state logtypes.DeliveryState) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	if end < start {
		return fmt.Errorf("ledger: invalid delivery batch [%d,%d)", start, end)
	}
	if state == "" {
		return fmt.Errorf("ledger: delivery state must not be empty; UNKNOWN is a state not a position")
	}
	e.DeliveryBatches = append(e.DeliveryBatches, DeliveryBatch{Start: start, End: end, State: state})
	e.DeliveryState = state
	e.Positions.Delivery = contiguousDeliveryEnd(e.DeliveryBatches, e.Positions.Reclaim)
	return nil
}

// ResolveDeliveryThroughRecovery records that a previously UNKNOWN batch was
// reconciled from a durable projection/recovery source. It preserves the
// original UNKNOWN audit entry and does not fabricate an HTTP success.
func (l *Ledger) ResolveDeliveryThroughRecovery(key SourceKey, start, end uint64) error {
	return l.RecordDelivery(key, start, end, logtypes.DeliveryReplayRequired)
}

// contiguousDeliveryEnd 计算已知请求结果覆盖的连续前缀末端。
// 乱序响应不得用最大位置越过未解决空洞。
func contiguousDeliveryEnd(batches []DeliveryBatch, floor uint64) uint64 {
	if len(batches) == 0 {
		return floor
	}
	type span struct{ start, end uint64 }
	spans := make([]span, 0, len(batches))
	for _, b := range batches {
		if b.End <= b.Start {
			continue
		}
		spans = append(spans, span{b.Start, b.End})
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].end < spans[j].end
		}
		return spans[i].start < spans[j].start
	})
	pos := floor
	for _, s := range spans {
		if s.start > pos {
			if s.start == pos+1 {
				pos = s.start
			} else {
				break // 空洞：不越过
			}
		}
		if s.end > pos {
			pos = s.end
		}
	}
	return pos
}

// RegisterSegment 登记物理源分段。
func (l *Ledger) RegisterSegment(key SourceKey, seg Segment) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for i, s := range e.Segments {
		if s.Path == seg.Path {
			e.Segments[i] = seg
			return nil
		}
	}
	e.Segments = append(e.Segments, seg)
	return nil
}

// LinkRotation 登记 latest.log → rotated/.gz 关联。generation 必须沿用被接管内容。
func (l *Ledger) LinkRotation(key SourceKey, fromPath, toPath string, endPosFrom uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for i, r := range e.Rotations {
		if r.FromPath == fromPath && r.ToPath == toPath {
			// 已关联：generation 不变；旧分段 EOF 成功 durable 后允许单调扩展已覆盖前缀。
			if endPosFrom > r.EndPosFrom {
				e.Rotations[i].EndPosFrom = endPosFrom
			}
			return nil
		}
	}
	e.Rotations = append(e.Rotations, RotationLink{
		FromPath:   fromPath,
		ToPath:     toPath,
		EndPosFrom: endPosFrom,
		Generation: key.SourceGeneration,
		LinkedAt:   l.nowUnix(),
	})
	// 更新分段结束位置。
	for i, s := range e.Segments {
		if s.Path == fromPath && endPosFrom > s.EndPos {
			e.Segments[i].EndPos = endPosFrom
		}
	}
	return nil
}

// RotationLinked 报告目标路径是否已作为逻辑源轮转结果登记。
func (l *Ledger) RotationLinked(key SourceKey, path string) (RotationLink, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, ok := l.entries[key]
	if !ok {
		return RotationLink{}, false
	}
	for _, r := range e.Rotations {
		if r.ToPath == path || r.FromPath == path {
			return r, true
		}
	}
	// 也按 segment 登记判断。
	for _, s := range e.Segments {
		if s.Path == path && s.Imported {
			return RotationLink{ToPath: path, Generation: key.SourceGeneration, EndPosFrom: s.StartPos}, true
		}
	}
	return RotationLink{}, false
}

// RecordGap 登记缺口。容量暂停/坏记录不得静默丢弃。
func (l *Ledger) RecordGap(key SourceKey, start, end uint64, reason, detail string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	e.Gaps = append(e.Gaps, Gap{StartPos: start, EndPos: end, Reason: reason, Detail: detail})
	e.ErrorCount++
	return nil
}

// PauseAcquire 容量耗尽：暂停低优先级采集并暴露原因。
func (l *Ledger) PauseAcquire(key SourceKey, reason string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	e.AcquirePaused = true
	e.PauseReason = reason
	return nil
}

// ResumeAcquire 恢复采集。
func (l *Ledger) ResumeAcquire(key SourceKey) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for _, gap := range e.Gaps {
		if !gap.Resolved {
			return fmt.Errorf("ledger: unresolved gaps still block acquisition")
		}
	}
	e.AcquirePaused = false
	e.PauseReason = ""
	return nil
}

// RegisterRecovery 登记受管恢复分段引用。
func (l *Ledger) RegisterRecovery(key SourceKey, ref RecoveryRef) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for _, r := range e.RecoveryRefs {
		if r.SegmentID == ref.SegmentID {
			if r.Path != ref.Path || r.CoversFrom != ref.CoversFrom || r.CoversTo != ref.CoversTo {
				return fmt.Errorf("ledger: recovery segment %s identity conflict", ref.SegmentID)
			}
			// Idempotent restart binding must not erase an advanced state, hold,
			// receiver or release proof.
			return nil
		}
	}
	e.RecoveryRefs = append(e.RecoveryRefs, ref)
	return nil
}

// TransitionRecovery 推进恢复分段责任状态。
// 进入 RELEASED 前必须登记合法 release_reason 与责任接收者（F-004）。
// CLEANED 只能从 RELEASED 进入；hold 中禁止释放。
func (l *Ledger) TransitionRecovery(key SourceKey, segmentID string, to logtypes.RecoverySegmentState, reason logtypes.ReleaseReason, receiver string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for i, r := range e.RecoveryRefs {
		if r.SegmentID != segmentID {
			continue
		}
		if r.HasHold && (to == logtypes.RecoveryReleased || to == logtypes.RecoveryCleaned) {
			return fmt.Errorf("ledger: cannot release recovery segment %s while hold is set", segmentID)
		}
		if recoveryRank(to) < recoveryRank(r.State) {
			return fmt.Errorf("ledger: recovery transition cannot move backward %s → %s", r.State, to)
		}
		if to == r.State {
			return nil
		}
		switch to {
		case logtypes.RecoveryReleased:
			if !logtypes.ValidReleaseReason(reason) {
				return fmt.Errorf("ledger: invalid release_reason %q", reason)
			}
			if receiver == "" || receiver == segmentID || strings.HasPrefix(receiver, "self:") {
				return fmt.Errorf("ledger: invalid responsibility receiver %q", receiver)
			}
			if r.State != logtypes.RecoveryWALResponsibilityXfer {
				return fmt.Errorf("ledger: cannot release recovery segment from state %s without WAL_RESPONSIBILITY_TRANSFERRED", r.State)
			}
		case logtypes.RecoveryCleaned:
			if r.State != logtypes.RecoveryReleased {
				return fmt.Errorf("ledger: CLEANED only allowed from RELEASED, got %s", r.State)
			}
			// 清理不要求新 reason；沿用已登记证明。
		case logtypes.RecoveryWALResponsibilityXfer:
			if r.State != logtypes.RecoveryDurableVerified && r.State != logtypes.RecoveryStaged && r.State != logtypes.RecoveryWALResponsibilityXfer {
				return fmt.Errorf("ledger: invalid recovery transition %s → %s", r.State, to)
			}
		}
		r.State = to
		if reason != "" {
			r.ReleaseReason = reason
		}
		if receiver != "" {
			r.ResponsibilityReceiver = receiver
		}
		e.RecoveryRefs[i] = r
		return nil
	}
	return fmt.Errorf("ledger: recovery segment %q not found", segmentID)
}

func recoveryRank(state logtypes.RecoverySegmentState) int {
	switch state {
	case logtypes.RecoveryStaged:
		return 1
	case logtypes.RecoveryDurableVerified:
		return 2
	case logtypes.RecoveryWALResponsibilityXfer:
		return 3
	case logtypes.RecoveryReleased:
		return 4
	case logtypes.RecoveryCleaned:
		return 5
	default:
		return 0
	}
}

// SetRecoveryHold 设置/清除恢复分段 hold；有 hold 时禁止 reclaim。
func (l *Ledger) SetRecoveryHold(key SourceKey, segmentID string, hasHold bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return err
	}
	for i, r := range e.RecoveryRefs {
		if r.SegmentID == segmentID {
			r.HasHold = hasHold
			e.RecoveryRefs[i] = r
			return nil
		}
	}
	return fmt.Errorf("ledger: recovery segment %q not found", segmentID)
}

// TryReclaim 按 logtypes.CanReclaim 门禁推进 reclaim_position。
// HTTP 2xx / REQUEST_DONE 单独不构成回收依据。
func (l *Ledger) TryReclaim(key SourceKey) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return 0, err
	}
	// 选取覆盖当前 reclaim 前缀的恢复分段。
	var ref *RecoveryRef
	for i := range e.RecoveryRefs {
		r := &e.RecoveryRefs[i]
		if r.CoversFrom <= e.Positions.Reclaim && r.CoversTo > e.Positions.Reclaim {
			ref = r
			break
		}
	}
	if ref == nil {
		// 无恢复分段时：仅当 CanReclaim 在“无 seg”语义下不放行。
		// 契约要求受管恢复副本证明；没有分段不得回收。
		return e.Positions.Reclaim, fmt.Errorf("ledger: reclaim blocked: no recovery segment covers position %d", e.Positions.Reclaim)
	}
	// CanReclaim 是回收门禁：STAGED/DURABLE_VERIFIED/hold 一律拒绝。
	if !logtypes.CanReclaim(e.Positions, ref.State, ref.ReleaseReason, ref.HasHold) {
		return e.Positions.Reclaim, fmt.Errorf("ledger: reclaim blocked by CanReclaim: state=%s reason=%q hold=%v delivery=%d reclaim=%d",
			ref.State, ref.ReleaseReason, ref.HasHold, e.Positions.Delivery, e.Positions.Reclaim)
	}
	target := ref.CoversTo
	if e.Positions.Delivery > 0 && e.Positions.Delivery < target && ref.State == logtypes.RecoveryWALResponsibilityXfer {
		// 责任已转移：可覆盖未确认事件，允许 reclaim 推进到分段覆盖末端。
		target = ref.CoversTo
	}
	if target <= e.Positions.Reclaim {
		return e.Positions.Reclaim, nil
	}
	e.Positions.Reclaim = target
	return e.Positions.Reclaim, nil
}

// IncrementIngestSeq 递增本地单调接纳序号（重放沿用原值由调用方保证）。
func (l *Ledger) IncrementIngestSeq(key SourceKey) (uint64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, err := l.require(key)
	if err != nil {
		return 0, err
	}
	e.IngestSeq++
	return e.IngestSeq, nil
}

func (l *Ledger) require(key SourceKey) (*Entry, error) {
	e, ok := l.entries[key]
	if !ok {
		return nil, fmt.Errorf("ledger: source %s not registered", key)
	}
	return e, nil
}

func (e *Entry) clone() *Entry {
	if e == nil {
		return nil
	}
	cp := *e
	cp.DeliveryBatches = append([]DeliveryBatch(nil), e.DeliveryBatches...)
	cp.Segments = append([]Segment(nil), e.Segments...)
	cp.Rotations = append([]RotationLink(nil), e.Rotations...)
	cp.Gaps = append([]Gap(nil), e.Gaps...)
	cp.RecoveryRefs = append([]RecoveryRef(nil), e.RecoveryRefs...)
	return &cp
}
