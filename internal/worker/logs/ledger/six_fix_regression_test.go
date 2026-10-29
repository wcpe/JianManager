package ledger

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestFingerprintOnlyChangeAdvancesRevisionAndIsIdempotent(t *testing.T) {
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	before := l.Revision(key)
	if !l.SetPositionsFingerprint(key, 1, "fixture-digest") {
		t.Fatal("未登记指纹")
	}
	after := l.Revision(key)
	if after <= before {
		t.Fatalf("独立指纹变化未触发持久化修订：%d → %d", before, after)
	}
	if !l.SetPositionsFingerprint(key, 1, "fixture-digest") || l.Revision(key) != after {
		t.Fatal("相同指纹必须幂等且不增长修订")
	}
}

func TestRequestDoneEvidenceDoesNotPromoteUnknownAndSurvivesPrune(t *testing.T) {
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	for _, batch := range []DeliveryBatch{{Start: 0, End: 10, State: logtypes.DeliveryUnknown}, {Start: 10, End: 20, State: logtypes.DeliveryNotSent}, {Start: 20, End: 30, State: logtypes.DeliveryRequestDone}} {
		if err := l.RecordDelivery(key, batch.Start, batch.End, batch.State); err != nil {
			t.Fatal(err)
		}
	}
	entry := l.Get(key)
	if entry.Positions.Delivery != 30 {
		t.Fatalf("请求结果前缀语义改变：%d", entry.Positions.Delivery)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		RequestDoneRanges []PositionRange `json:"request_done_ranges"`
		DeliveryEvidence  []DeliveryBatch `json:"delivery_evidence"`
	}
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evidence.RequestDoneRanges, []PositionRange{{From: 20, To: 30}}) {
		t.Fatalf("只有真实REQUEST_DONE可生成ACK证据：%+v", evidence.RequestDoneRanges)
	}
	if len(evidence.DeliveryEvidence) != 3 || evidence.DeliveryEvidence[0].State != logtypes.DeliveryUnknown {
		t.Fatalf("UNKNOWN诊断缺少独立精确区间证据：%+v", evidence.DeliveryEvidence)
	}
}

func TestPhysicalMappingAndFingerprintSnapshotRestoreThreeRounds(t *testing.T) {
	data := []byte(`[{"key":{"log_source_id":"fixture-source","source_generation":"fixture-generation"},"segments":[{"path":"fixture/live.log","kind":"live","start_pos":100,"end_pos":120,"physical_start":25,"physical_mapping_known":true,"identity_bytes":8,"identity_sha256":"identity","parser_version":"fixture"}],"confirmed_fingerprint":{"segment_path":"fixture/live.log","segment_start_pos":100,"segment_identity_bytes":8,"segment_identity_sha256":"identity","logical_pos":120,"physical_offset":45,"window_bytes":8,"sha256":"digest"}}]`)
	for round := 1; round <= 3; round++ {
		var entries []Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		l := New()
		if err := l.Restore(entries); err != nil {
			t.Fatal(err)
		}
		var err error
		data, err = json.Marshal(l.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var state []struct {
			Segments []struct {
				PhysicalStart uint64 `json:"physical_start"`
				Known         bool   `json:"physical_mapping_known"`
			} `json:"segments"`
			Fingerprint *struct {
				LogicalPos     uint64 `json:"logical_pos"`
				PhysicalOffset int64  `json:"physical_offset"`
			} `json:"confirmed_fingerprint"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		if len(state) != 1 || len(state[0].Segments) != 1 || state[0].Segments[0].PhysicalStart != 25 || !state[0].Segments[0].Known || state[0].Fingerprint == nil || state[0].Fingerprint.LogicalPos != 120 || state[0].Fingerprint.PhysicalOffset != 45 {
			t.Fatalf("第%d轮恢复丢失截断映射或分段指纹：%s", round, data)
		}
	}
}

func TestFoldedGapAdjacentMergePreservesNewExactEvidence(t *testing.T) {
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l := New()
	if err := l.Restore([]Entry{{Key: key, Gaps: []Gap{{StartPos: 10, EndPos: 120, Reason: GapReasonDeliverError, FoldSpans: []PositionRange{{From: 10, To: 20}, {From: 110, To: 120}}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordGap(key, 120, 140, GapReasonDeliverError, "fixture-adjacent-failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AbandonGapsThrough(key, 140, GapAbandonment{Operator: "fixture-operator"}); err != nil {
		t.Fatal(err)
	}
	var newlyFailed, healthy bool
	for _, abandonment := range l.Get(key).Abandonments {
		newlyFailed = newlyFailed || abandonment.From <= 135 && 135 <= abandonment.To
		healthy = healthy || abandonment.From <= 50 && 50 <= abandonment.To
	}
	if !newlyFailed || healthy {
		t.Fatalf("相邻合并丢失新失败精确区间或扩大到健康位置：%+v", l.Get(key).Abandonments)
	}
}

func TestUnknownFoldEvidenceCannotAbandonHealthyBytes(t *testing.T) {
	var entries []Entry
	if err := json.Unmarshal([]byte(`[{"key":{"log_source_id":"fixture-source","source_generation":"fixture-generation"},"gaps":[{"start_pos":10,"end_pos":120,"reason":"fixture-failure","fold_spans_unknown":true}]}]`), &entries); err != nil {
		t.Fatal(err)
	}
	l := New()
	if err := l.Restore(entries); err != nil {
		t.Fatal(err)
	}
	key := entries[0].Key
	resolved, err := l.AbandonGapsThrough(key, 120, GapAbandonment{Operator: "fixture-operator"})
	if err == nil || resolved != 0 || len(l.Get(key).Abandonments) != 0 || l.Get(key).Gaps[0].Resolved {
		t.Fatalf("缺失折叠证据却生成永久丢失凸包：resolved=%d err=%v entry=%+v", resolved, err, l.Get(key))
	}
}

func TestFoldedGapUnknownPreflightIsAtomicAndSurvivesMerges(t *testing.T) {
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l := New()
	if err := l.Restore([]Entry{{Key: key, Gaps: []Gap{
		{StartPos: 0, EndPos: 5, Reason: GapReasonDeliverError},
		{StartPos: 10, EndPos: 120, Reason: GapReasonDeliverError, FoldSpansUnknown: true},
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := l.RecordGap(key, 120, 140, GapReasonDeliverError, "fixture-adjacent"); err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < DefaultMaxUnresolvedGapsPerSource; i++ {
		if err := l.RecordGap(key, 200+i*100, 209+i*100, GapReasonDeliverError, "fixture-fold"); err != nil {
			t.Fatal(err)
		}
	}
	before := l.Get(key)
	var unknown bool
	for _, gap := range before.Gaps {
		unknown = unknown || gap.FoldSpansUnknown
		if gap.FoldSpansUnknown && len(gapEvidenceSpans(gap)) != 0 {
			t.Fatal("缺失精度的折叠缺口不得展开凸包证据")
		}
	}
	if !unknown {
		t.Fatal("相邻合并或再次折叠丢失Unknown标记")
	}
	resolved, err := l.AbandonGapsThrough(key, 10000, GapAbandonment{Operator: "fixture-operator"})
	if err == nil || resolved != 0 || !reflect.DeepEqual(l.Get(key), before) {
		t.Fatalf("整批预检失败留下部分裁定或凭据：resolved=%d err=%v", resolved, err)
	}
}

func TestFoldSpanCapCannotCreateHullAbandonmentEvidence(t *testing.T) {
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l := New()
	l.Ensure(key, logtypes.SourceIdentity{})
	for i := uint64(0); i < DefaultMaxUnresolvedGapsPerSource+DefaultMaxFoldSpansPerGap+10; i++ {
		if err := l.RecordGap(key, i*100+10, i*100+19, GapReasonDeliverError, "fixture-disjoint"); err != nil {
			t.Fatal(err)
		}
	}
	before := l.Get(key)
	for _, gap := range before.Gaps {
		if gap.FoldSpanMerges > 0 && !gap.FoldSpansUnknown {
			t.Fatalf("折叠精度已丢失却未标记Unknown：%+v", gap)
		}
		if gap.FoldSpansUnknown && len(gapEvidenceSpans(gap)) > 0 {
			t.Fatal("子区间预算耗尽后凸包被当成精确证据")
		}
	}
	resolved, err := l.AbandonGapsThrough(key, 20000, GapAbandonment{Operator: "fixture-operator"})
	if err == nil || resolved != 0 || !reflect.DeepEqual(l.Get(key), before) {
		t.Fatalf("子区间预算耗尽后仍生成凸包裁定：resolved=%d err=%v", resolved, err)
	}
}

func TestLegacyCoarseFoldEvidenceRemainsUnknownAfterThreeRestarts(t *testing.T) {
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	entries := []Entry{{Key: key, Gaps: []Gap{{
		StartPos: 10, EndPos: 120, Reason: GapReasonDeliverError,
		FoldSpans: []PositionRange{{From: 10, To: 120}}, FoldSpanMerges: 1,
	}}}}
	for round := 1; round <= 3; round++ {
		l := New()
		if err := l.Restore(entries); err != nil {
			t.Fatal(err)
		}
		before := l.Get(key)
		if !before.Gaps[0].FoldSpansUnknown || len(gapEvidenceSpans(before.Gaps[0])) != 0 {
			t.Fatalf("第%d轮把旧退化子区间恢复成精确证据：%+v", round, before.Gaps[0])
		}
		resolved, err := l.AbandonGapsThrough(key, 120, GapAbandonment{Operator: "fixture-operator"})
		if err == nil || resolved != 0 || !reflect.DeepEqual(before, l.Get(key)) {
			t.Fatalf("第%d轮从旧凸包生成永久丢失裁定：resolved=%d err=%v", round, resolved, err)
		}
		data, err := json.Marshal(l.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
	}
}

// 2026-10-29：原 TestRecoveryRefsLongRunRequiresDurableArchiveProtocol（含两个子用例）随
// 受管恢复分段机制删除 —— RecoveryRefs / ArchiveCleanedRecovery / RecoveryArchiveStore 均已不存在。
// 原关切是「恢复段元数据不得随重启无界增长」：那个元数据本身已不存在，故关切失去对象。
// 现在唯一会随重启/长跑增长的是 DeliveryBatches，它的有界性由 delivery_batch_prune.go 负责。
