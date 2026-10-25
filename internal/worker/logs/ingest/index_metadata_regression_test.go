package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

func TestIndexGapFoldAndAuditRoundTripThreeOpens(t *testing.T) {
	key := ledger.SourceKey{LogSourceID: "index-fold", SourceGeneration: "g1"}
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration})
	for i := 0; i < 65; i++ {
		if err := led.RecordGap(key, uint64(i*100+10), uint64(i*100+20), ledger.GapReasonDeliverError, "离散缺口"); err != nil {
			t.Fatal(err)
		}
	}
	if err := led.AdvanceRead(key, 6500); err != nil {
		t.Fatal(err)
	}
	entry := led.Get(key)
	if len(entry.Gaps[0].FoldSpans) != 2 {
		t.Fatalf("未触发折叠：%+v", entry.Gaps[0])
	}
	entry.Gaps = append(entry.Gaps, ledger.Gap{StartPos: 7000, EndPos: 7010, Reason: "SEED", Resolved: true, Resolution: "人工放弃", ReasonCode: ledger.GapReasonPermanentlyLost, ResolvedBy: "测试操作者", ResolvedAtUTC: "2026-10-22T00:00:00Z"})
	m := &Manager{}
	rows, err := m.stateToIndexState(&persistedState{Sources: map[string]persistedSource{key.String(): {Ledger: []ledger.Entry{*entry}}}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.db")
	for round := 1; round <= 3; round++ {
		store, err := stateindex.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if round == 1 {
			if _, err := store.Apply(rows); err != nil {
				t.Fatal(err)
			}
		}
		loadedRows, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		restored, err := indexStateToState(loadedRows)
		if err != nil {
			t.Fatal(err)
		}
		got := restored.Sources[key.String()].Ledger[0]
		if !reflect.DeepEqual(entry.Gaps, got.Gaps) {
			t.Errorf("第%d次重开缺口元数据不等价：want=%+v got=%+v", round, entry.Gaps, got.Gaps)
		}
		restoredLedger := ledger.New()
		if err := restoredLedger.Restore([]ledger.Entry{got}); err != nil {
			t.Fatal(err)
		}
		if _, err := restoredLedger.AbandonGapsThrough(key, 6500, ledger.GapAbandonment{Operator: "测试操作者", AtUTC: "2026-10-22T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		if _, covered := restoredLedger.AbandonmentsCovering(key, 50); covered {
			t.Errorf("第%d次重开后健康字节50被错误放弃", round)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIndexFingerprintOnlyMutationPersists(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "latest.log")
	if err := os.WriteFile(path, []byte("隔离日志\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := SourceConfig{LogSourceID: "index-fingerprint", SourceGeneration: "g1", Path: path, Mode: pipeline.ModeFilePrimary, StorageNamespace: "index-fingerprint", UTCDay: runtimeTestUTCDay()}
	m, err := New(Options{Root: root, Catalog: catalog.New(catalog.NewMemJournal()), Sources: []SourceConfig{cfg}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = crashClose(m) }()
	pipe := m.pipes[cfg.LogSourceID+"/g1"]
	if err := pipe.Ledger().AdvanceRead(pipe.Key(), 1); err != nil {
		t.Fatal(err)
	}
	if err := pipe.Ledger().RecordDelivery(pipe.Key(), 0, 1, logtypes.DeliveryRequestDone); err != nil {
		t.Fatal(err)
	}
	if err := m.persist(); err != nil {
		t.Fatal(err)
	}
	if !pipe.Ledger().SetPositionsFingerprint(pipe.Key(), 1, "independent-change") {
		t.Fatal("未能设置指纹")
	}
	if err := m.persist(); err != nil {
		t.Fatal(err)
	}
	saved, err := m.loadStateFromIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := saved.Sources[cfg.LogSourceID+"/g1"].Ledger[0].PositionsFingerprint; got != "independent-change" {
		t.Fatalf("真实Manager.persist遗漏独立指纹更新：%q", got)
	}
}

func TestIndexEventStatusNeverUsesDeliveryAsAck(t *testing.T) {
	key := ledger.SourceKey{LogSourceID: "index-unknown", SourceGeneration: "g1"}
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{})
	if err := led.RecordDelivery(key, 0, 100, logtypes.DeliveryUnknown); err != nil {
		t.Fatal(err)
	}
	entry := led.Get(key)
	got := classifyEventStatus(EventStatusFacts{Positions: entry.Positions, Batches: entry.DeliveryBatches}, 1, 99)
	if got.State != EventStateSentUnacked {
		t.Errorf("UNKNOWN被伪装成ACK：%+v", got)
	}
	got = classifyEventStatus(EventStatusFacts{Positions: entry.Positions}, 1, 99)
	if got.State == EventStateAcked {
		t.Fatalf("旧已裁剪记录无独立证据却判ACK：%+v", got)
	}
}
