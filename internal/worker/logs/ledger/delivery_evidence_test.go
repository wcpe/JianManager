package ledger

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func requestBatchReferenceAt(batches []DeliveryBatch, position uint64) string {
	for _, state := range []logtypes.DeliveryState{logtypes.DeliveryRequestDone, logtypes.DeliveryUnknown} {
		for _, batch := range batches {
			if batch.Start <= position && position <= batch.End && batch.State == state {
				return string(state)
			}
		}
	}
	for _, batch := range batches {
		if batch.Start <= position && position <= batch.End {
			return string(batch.State)
		}
	}
	return "fixture-no-evidence"
}

func diagnosticEvidenceAt(entry *Entry, position uint64) string {
	for _, span := range RequestDoneRangesOf(entry) {
		if span.From <= position && position <= span.To {
			return string(logtypes.DeliveryRequestDone)
		}
	}
	evidence := DeliveryEvidenceOf(entry)
	for _, batch := range evidence {
		if batch.Start <= position && position <= batch.End && batch.State == logtypes.DeliveryUnknown {
			return string(logtypes.DeliveryUnknown)
		}
	}
	for _, batch := range evidence {
		if batch.Start <= position && position <= batch.End {
			return string(batch.State)
		}
	}
	return "fixture-no-evidence"
}

func TestDeliveryEvidenceExactIntervalsEquivalentBeforeAfterPruneAndRestart(t *testing.T) {
	l := New()
	l.SetDeliveryBatchPrune(DeliveryBatchPruneConfig{Enabled: false})
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	batches := []DeliveryBatch{
		{Start: 0, End: 10, State: logtypes.DeliveryUnknown},
		{Start: 10, End: 20, State: logtypes.DeliveryNotSent},
		{Start: 30, End: 40, State: logtypes.DeliveryReplayRequired},
		{Start: 50, End: 60, State: logtypes.DeliveryRequestDone},
		{Start: 5, End: 8, State: logtypes.DeliveryRequestDone},
		{Start: 40, End: 45, State: logtypes.DeliveryUnknown},
		{Start: 70, End: 70, State: logtypes.DeliveryUnknown},
		{Start: 75, End: 80, State: logtypes.DeliveryState("fixture-future-state")},
		{Start: 82, End: 100, State: logtypes.DeliveryNotSent},
		{Start: 84, End: 96, State: logtypes.DeliveryUnknown},
		{Start: 88, End: 90, State: logtypes.DeliveryRequestDone},
	}
	for _, batch := range batches {
		if err := l.RecordDelivery(key, batch.Start, batch.End, batch.State); err != nil {
			t.Fatal(err)
		}
	}
	before := l.Get(key)
	for position := uint64(0); position <= 100; position++ {
		if want, got := requestBatchReferenceAt(batches, position), diagnosticEvidenceAt(before, position); want != got {
			t.Fatalf("压缩诊断证据偏离原真实批次：pos=%d want=%s got=%s", position, want, got)
		}
	}
	for _, position := range []uint64{0, 4, 9, 10, 11, 20, 21, 25, 46, 69, 70, 75, 80} {
		if got := diagnosticEvidenceAt(before, position); got == string(logtypes.DeliveryRequestDone) {
			t.Fatalf("非成功请求被晋升ACK：pos=%d", position)
		}
	}
	if got := diagnosticEvidenceAt(before, 6); got != string(logtypes.DeliveryRequestDone) {
		t.Fatal("首个UNKNOWN遮住后来的真实成功回执")
	}
	l.mu.Lock()
	l.entries[key].Positions = logtypes.Positions{Read: 100, Durable: 100, Delivery: 100, Reclaim: 100}
	l.mu.Unlock()
	l.SetDeliveryBatchPrune(DefaultDeliveryBatchPruneConfig())
	if _, err := l.TryReclaim(key); err != nil {
		t.Fatal(err)
	}
	if len(l.Get(key).DeliveryBatches) != 0 {
		t.Fatal("夹具没有真正裁剪历史批次")
	}
	for round := 0; round <= 3; round++ {
		after := l.Get(key)
		for position := uint64(0); position <= 100; position++ {
			if want, got := diagnosticEvidenceAt(before, position), diagnosticEvidenceAt(after, position); want != got {
				t.Fatalf("第%d轮裁剪/恢复改变精确判态：pos=%d want=%s got=%s", round, position, want, got)
			}
		}
		data, err := json.Marshal(l.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var entries []Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		l = New()
		if err := l.Restore(entries); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOldPrunedDeliveryPrefixDoesNotFabricateRequestDoneEvidence(t *testing.T) {
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	if err := l.Restore([]Entry{{Key: key, Positions: logtypes.Positions{Read: 100, Reclaim: 100, Delivery: 100}}}); err != nil {
		t.Fatal(err)
	}
	entry := l.Get(key)
	if entry.Positions.Delivery != 100 || len(RequestDoneRangesOf(entry)) != 0 || len(DeliveryEvidenceOf(entry)) != 0 || diagnosticEvidenceAt(entry, 50) != "fixture-no-evidence" {
		t.Fatalf("旧已裁状态从水位伪造了成功证据：%+v", entry)
	}
}

func TestRestoreRetainedBatchesRebuildsOnlyRealRequestDoneEvidence(t *testing.T) {
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l := New()
	if err := l.Restore([]Entry{{Key: key, DeliveryBatches: []DeliveryBatch{
		{Start: 0, End: 10, State: logtypes.DeliveryUnknown},
		{Start: 11, End: 20, State: logtypes.DeliveryRequestDone},
		{Start: 30, End: 40, State: logtypes.DeliveryRequestDone},
	}}}); err != nil {
		t.Fatal(err)
	}
	entry := l.Get(key)
	if !reflect.DeepEqual(entry.RequestDoneRanges, []PositionRange{{From: 11, To: 20}, {From: 30, To: 40}}) || diagnosticEvidenceAt(entry, 25) != "fixture-no-evidence" || diagnosticEvidenceAt(entry, 10) != string(logtypes.DeliveryUnknown) {
		t.Fatalf("恢复时扩大或补造ACK：%+v", entry)
	}
	if l.Revision(key) == 0 {
		t.Fatal("恢复快照未登记修订，后续持久化可能跳过完整新字段")
	}
}

func TestDeliveryEvidenceSinglePointAndUint64BoundaryRemainExact(t *testing.T) {
	entry := &Entry{}
	for _, batch := range []DeliveryBatch{
		{Start: 1, End: 2, State: logtypes.DeliveryUnknown},
		{Start: 2, End: 3, State: logtypes.DeliveryNotSent},
		{Start: math.MaxUint64 - 2, End: math.MaxUint64, State: logtypes.DeliveryUnknown},
	} {
		preserveDeliveryEvidence(entry, batch)
	}
	if diagnosticEvidenceAt(entry, 3) != string(logtypes.DeliveryNotSent) || diagnosticEvidenceAt(entry, 0) != "fixture-no-evidence" || diagnosticEvidenceAt(entry, math.MaxUint64) != string(logtypes.DeliveryUnknown) {
		t.Fatalf("边界位置证据丢失或溢出：%+v", entry.DeliveryEvidence)
	}
}

func TestDeliveryEvidenceRetainsUnknownAfterEarlierNotSent(t *testing.T) {
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	for _, state := range []logtypes.DeliveryState{logtypes.DeliveryNotSent, logtypes.DeliveryUnknown} {
		if err := l.RecordDelivery(key, 0, 100, state); err != nil {
			t.Fatal(err)
		}
	}
	var notSent, unknown bool
	for _, batch := range DeliveryEvidenceOf(l.Get(key)) {
		if batch.Start <= 1 && batch.End >= 99 {
			notSent = notSent || batch.State == logtypes.DeliveryNotSent
			unknown = unknown || batch.State == logtypes.DeliveryUnknown
		}
	}
	if !notSent || !unknown || len(RequestDoneRangesOf(l.Get(key))) != 0 {
		t.Fatalf("真实UNKNOWN证据被较早未发送结果吞掉：%+v", l.Get(key).DeliveryEvidence)
	}
}

func TestDeliveryEvidenceCompacts1000AdjacentSuccessfulBatches(t *testing.T) {
	for _, state := range []logtypes.DeliveryState{logtypes.DeliveryRequestDone, logtypes.DeliveryUnknown, logtypes.DeliveryNotSent, logtypes.DeliveryReplayRequired} {
		t.Run(string(state), func(t *testing.T) {
			l := New()
			key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
			l.Ensure(key, logtypes.SourceIdentity{})
			for i := uint64(0); i < 1000; i++ {
				if err := l.RecordDelivery(key, i*10, (i+1)*10, state); err != nil {
					t.Fatal(err)
				}
			}
			entry := l.Get(key)
			if !reflect.DeepEqual(entry.DeliveryEvidence, []DeliveryBatch{{Start: 0, End: 10000, State: state}}) {
				t.Fatalf("同态证据未归并成一段：%+v", entry.DeliveryEvidence)
			}
			if state == logtypes.DeliveryRequestDone {
				if !reflect.DeepEqual(entry.RequestDoneRanges, []PositionRange{{From: 0, To: 10000}}) {
					t.Fatalf("真实ACK没有精确归并：%+v", entry.RequestDoneRanges)
				}
			} else if len(entry.RequestDoneRanges) != 0 {
				t.Fatalf("非成功同态批次生成ACK：%+v", entry.RequestDoneRanges)
			}
		})
	}
}

func TestDeliveryEvidenceTrueHolesAndDifferentStatesAreNotCapped(t *testing.T) {
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	states := []logtypes.DeliveryState{logtypes.DeliveryRequestDone, logtypes.DeliveryUnknown, logtypes.DeliveryNotSent, logtypes.DeliveryReplayRequired, "fixture-future-state"}
	var batches []DeliveryBatch
	for i := uint64(0); i < 257; i++ {
		batch := DeliveryBatch{Start: i * 4, End: i*4 + 1, State: states[i%uint64(len(states))]}
		batches = append(batches, batch)
		if err := l.RecordDelivery(key, batch.Start, batch.End, batch.State); err != nil {
			t.Fatal(err)
		}
	}
	entry := l.Get(key)
	if len(entry.DeliveryEvidence) != len(batches) {
		t.Fatalf("为绝对条数上限丢掉真实空洞或状态：want=%d got=%d", len(batches), len(entry.DeliveryEvidence))
	}
	for pos := uint64(0); pos < 257*4; pos++ {
		if want, got := requestBatchReferenceAt(batches, pos), diagnosticEvidenceAt(entry, pos); want != got {
			t.Fatalf("真实空洞或状态被合并抹平：pos=%d want=%s got=%s", pos, want, got)
		}
	}
}

func TestDeliveryEvidenceMergesOnlySameStateRegardlessOfArrivalOrder(t *testing.T) {
	batches := []DeliveryBatch{
		{Start: 5, End: 20, State: logtypes.DeliveryNotSent},
		{Start: 0, End: 10, State: logtypes.DeliveryUnknown},
		{Start: 10, End: 30, State: logtypes.DeliveryUnknown},
		{Start: 19, End: 25, State: logtypes.DeliveryNotSent},
		{Start: 15, End: 16, State: logtypes.DeliveryRequestDone},
	}
	for _, order := range [][]int{{0, 1, 2, 3, 4}, {4, 3, 2, 1, 0}, {2, 0, 4, 1, 3}} {
		entry := &Entry{}
		for _, i := range order {
			preserveDeliveryEvidence(entry, batches[i])
		}
		want := []DeliveryBatch{
			{Start: 0, End: 30, State: logtypes.DeliveryUnknown},
			{Start: 5, End: 25, State: logtypes.DeliveryNotSent},
			{Start: 15, End: 16, State: logtypes.DeliveryRequestDone},
		}
		if got := DeliveryEvidenceOf(entry); !reflect.DeepEqual(got, want) {
			t.Fatalf("到达顺序改变跨状态精确证据：order=%v want=%+v got=%+v", order, want, got)
		}
		for pos := uint64(0); pos <= 32; pos++ {
			if want, got := requestBatchReferenceAt(batches, pos), diagnosticEvidenceAt(entry, pos); want != got {
				t.Fatalf("到达顺序改变判态：order=%v pos=%d want=%s got=%s", order, pos, want, got)
			}
		}
	}
}
