package ingest

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

func TestIndexManagerConfirmedPhysicalFingerprintAndGapThreeOpens(t *testing.T) {
	// 2026-11-02：本用例验的是「**跨重启**不丢物理指纹 / 精确缺口折叠区间」。
	//
	// 但采集索引已在 2026-10-30 按用户裁定（机械盘不落盘）改成 `stateindex.OpenInMemory`
	// （见 index_store.go 的 openIndex 注释）：**没有磁盘文件可供重开**，
	// 本用例的前提（“重新 Open 同一个库三次”）整条消失。
	//
	// 同一批字段的映射逻辑已经有纯内存形态的用例覆盖（不跨进程）：
	//   · TestEventStatusFactsSurviveIndexRoundTrip（event_status 相关）
	//   · 以及 stateindex 包自己的 Apply/Load 往返用例。
	// 故本条不再有意义，保留为明确跳过而不是默默删掉。
	t.Skip("采集索引已改纯内存（2026-10-30 机械盘不落盘裁定），没有磁盘库可“重新打开三次”；" +
		"跨重启不丢这一语义已不可测，见 index_store.go 的 openIndex 与 ExportIndexJSON 注释")
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	body := []byte(strings.Repeat("0123456789", 30) + "\n")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := SourceConfig{LogSourceID: "physical-index", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary, StorageNamespace: "physical-index", UTCDay: runtimeTestUTCDay()}
	m, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal()), Sources: []SourceConfig{cfg}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = crashClose(m) }()
	p := m.pipes[cfg.LogSourceID+"/g1"]
	if err := m.persist(); err != nil {
		t.Fatal(err)
	}
	identityHash := fmt.Sprintf("%x", sha256.Sum256(body[:64]))
	fingerprint := ledger.ConfirmedFingerprint{SegmentPath: path, SegmentStartPos: 100, SegmentIdentityBytes: 64, SegmentIdentitySHA256: identityHash, LogicalPos: 120, PhysicalOffset: 37, WindowBytes: 32, SHA256: fmt.Sprintf("%x", sha256.Sum256(body[5:37]))}
	segment := ledger.Segment{Path: path, Kind: ledger.SegmentLive, StartPos: 100, EndPos: 200, PhysicalStart: 17, PhysicalMappingKnown: true, IdentityBytes: 64, IdentitySHA256: identityHash}
	if err := p.Ledger().RegisterSegment(p.Key(), segment); err != nil {
		t.Fatal(err)
	}
	if err := p.Ledger().AdvanceRead(p.Key(), 200); err != nil {
		t.Fatal(err)
	}
	if err := p.Ledger().RecordDelivery(p.Key(), 0, 120, logtypes.DeliveryRequestDone); err != nil {
		t.Fatal(err)
	}
	if !p.Ledger().SetConfirmedFingerprint(p.Key(), fingerprint) {
		t.Fatal("真实绑定被拒绝")
	}
	for i := 0; i < 65; i++ {
		if err := p.Ledger().RecordGap(p.Key(), uint64(i*100+10), uint64(i*100+20), ledger.GapReasonDeliverError, "Manager真实折叠"); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.persist(); err != nil {
		t.Fatal(err)
	}
	if err := crashClose(m); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		store, err := stateindex.Open(m.indexPath())
		if err != nil {
			t.Fatal(err)
		}
		rows, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		saved, err := indexStateToState(rows)
		if err != nil {
			t.Fatal(err)
		}
		entry := saved.Sources[p.Key().String()].Ledger[0]
		if entry.ConfirmedFingerprint == nil || !reflect.DeepEqual(*entry.ConfirmedFingerprint, fingerprint) {
			t.Fatalf("第%d次重开丢绑定：%+v", round, entry.ConfirmedFingerprint)
		}
		if len(entry.Gaps[0].FoldSpans) != 2 {
			t.Fatalf("第%d次重开丢精确缺口", round)
		}
		restored := ledger.New()
		if err := restored.Restore([]ledger.Entry{entry}); err != nil {
			t.Fatal(err)
		}
		if actual, ok := restored.ConfirmedFingerprint(p.Key()); !ok || actual != fingerprint {
			t.Fatalf("第%d次重开绑定无法验证：%+v %v", round, actual, ok)
		}
		if physical := entry.Segments[0].PhysicalStart + (120 - entry.Segments[0].StartPos); physical != 37 {
			t.Fatalf("物理定位退化：%d", physical)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// 2026-10-29：`TestIndexRecoveryArchiveLedgerThousandBatchesThreeOpens` 随受管恢复分段机制
// 一并删除。它整条都在验「recovery_audit 仓储 + ArchiveCleanedRecovery 的分页/取消/重开语义」，
// 而那套仓储（`RecoveryAuditRow` / `AppendRecoveryAudit` / `ListRecoveryAudit`）已随机制删除
// —— 原关切「活动恢复历史不得随累计批次无界增长」失去了被测对象（那个历史本身不存在了）。
// 现在索引里唯一会随批次线性增长的是 delivery_batch，它的有界性由
// `TestDeliveryBatchPruneBoundsIndexGrowth` 与 `TestIndexDeliveryMixedEvidencePrunedThreeOpens` 守。

func TestIndexDeliveryMixedEvidencePrunedThreeOpens(t *testing.T) {
	key := ledger.SourceKey{LogSourceID: "mixed-evidence", SourceGeneration: "g1"}
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{})
	for _, batch := range []ledger.DeliveryBatch{{Start: 0, End: 100, State: logtypes.DeliveryNotSent}, {Start: 0, End: 100, State: logtypes.DeliveryUnknown}, {Start: 30, End: 40, State: logtypes.DeliveryRequestDone}} {
		if err := led.RecordDelivery(key, batch.Start, batch.End, batch.State); err != nil {
			t.Fatal(err)
		}
	}
	entry := led.Get(key)
	entry.DeliveryBatches = nil
	entry.Positions = logtypes.Positions{Read: 100, Durable: 100, Delivery: 100, Reclaim: 100}
	m := &Manager{}
	rows, err := m.stateToIndexState(&persistedState{Sources: map[string]persistedSource{key.String(): {Ledger: []ledger.Entry{*entry}}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.db")
	for round := 0; round < 3; round++ {
		s, err := stateindex.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if round == 0 {
			if _, err := s.Apply(rows); err != nil {
				t.Fatal(err)
			}
		}
		loaded, err := s.Load()
		if err != nil {
			t.Fatal(err)
		}
		saved, err := indexStateToState(loaded)
		if err != nil {
			t.Fatal(err)
		}
		facts := eventStatusFactsOfPersisted(saved.Sources[key.String()])
		if got := classifyEventStatus(facts, 10, 20); got.State != EventStateSentUnacked {
			t.Fatalf("第%d次重开真实UNKNOWN被旧未发送遮住：%+v", round+1, got)
		}
		if got := classifyEventStatus(facts, 31, 39); got.State != EventStateAcked {
			t.Fatalf("第%d次重开真实ACK被UNKNOWN遮住：%+v", round+1, got)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
