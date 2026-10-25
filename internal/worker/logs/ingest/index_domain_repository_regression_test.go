package ingest

import (
	"context"
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

func TestIndexRecoveryArchiveLedgerThousandBatchesThreeOpens(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	m := &Manager{root: root}
	s, err := stateindex.Open(m.indexPath())
	if err != nil {
		t.Fatal(err)
	}
	m.index = s
	key := ledger.SourceKey{LogSourceID: "disk-recovery", SourceGeneration: "g1"}
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{})
	led.SetRecoveryArchiveStore(m)
	for i := 0; i < 1000; i++ {
		from, to := uint64(i*10), uint64((i+1)*10)
		id := fmt.Sprintf("recovery-%04d", i)
		if err := led.AdvanceRead(key, to); err != nil {
			t.Fatal(err)
		}
		if err := led.RecordDelivery(key, from, to, logtypes.DeliveryRequestDone); err != nil {
			t.Fatal(err)
		}
		if err := led.RegisterRecovery(key, ledger.RecoveryRef{SegmentID: id, Path: "projection://disk", State: logtypes.RecoveryStaged, CoversFrom: from, CoversTo: to}); err != nil {
			t.Fatal(err)
		}
		for _, state := range []logtypes.RecoverySegmentState{logtypes.RecoveryDurableVerified, logtypes.RecoveryWALResponsibilityXfer, logtypes.RecoveryReleased, logtypes.RecoveryCleaned} {
			reason := logtypes.ReleaseReason("")
			if state == logtypes.RecoveryReleased {
				reason = logtypes.ReleaseProjectionBacked
			}
			if err := led.TransitionRecovery(key, id, state, reason, "projection:disk"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := led.TryReclaim(key); err != nil {
			t.Fatal(err)
		}
		if (i+1)%16 == 0 {
			if _, err := led.ArchiveCleanedRecovery(ctx, key, 16); err != nil {
				t.Fatal(err)
			}
		}
		if active := len(led.Get(key).RecoveryRefs); active > 15 {
			t.Fatalf("活动恢复历史随累计批次增长：%d", active)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	before := len(led.Get(key).RecoveryRefs)
	if _, err := led.ArchiveCleanedRecovery(canceled, key, 16); err == nil {
		t.Fatal("取消仓储不能返回成功回执")
	}
	if got := len(led.Get(key).RecoveryRefs); got != before {
		t.Fatalf("取消后未归档责任被裁：%d -> %d", before, got)
	}
	if _, err := led.ArchiveCleanedRecovery(ctx, key, 16); err != nil {
		t.Fatal(err)
	}
	rows, err := m.stateToIndexState(&persistedState{Sources: map[string]persistedSource{key.String(): {Ledger: []ledger.Entry{*led.Get(key)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Aux[0].Payload) > 8192 {
		t.Fatalf("活动aux仍携带全部历史：%d", len(rows.Aux[0].Payload))
	}
	if _, err := s.Apply(rows); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 3; round++ {
		m.index, err = stateindex.Open(m.indexPath())
		if err != nil {
			t.Fatal(err)
		}
		saved, err := m.loadStateFromIndex()
		if err != nil {
			t.Fatal(err)
		}
		entry := saved.Sources[key.String()].Ledger[0]
		if len(entry.RecoveryRefs) != 0 {
			t.Fatalf("第%d次重开历史复活：%d", round, len(entry.RecoveryRefs))
		}
		count, cursor := 0, ""
		for {
			records, receipts, err := m.ListRecoveryArchive(ctx, key, cursor, 37)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) == 0 {
				break
			}
			if len(records) != len(receipts) {
				t.Fatal("审计与回执不对应")
			}
			count += len(records)
			cursor = records[len(records)-1].Ref.SegmentID
		}
		if count != 1000 {
			t.Fatalf("第%d次重开完整历史仅%d", round, count)
		}
		restored := ledger.New()
		if err := restored.Restore([]ledger.Entry{entry}); err != nil {
			t.Fatal(err)
		}
		restored.SetRecoveryArchiveStore(m)
		if err := restored.RegisterRecovery(key, ledger.RecoveryRef{SegmentID: "recovery-0042", Path: "projection://disk", State: logtypes.RecoveryStaged, CoversFrom: 420, CoversTo: 430}); err != nil {
			t.Fatal(err)
		}
		if len(restored.Get(key).RecoveryRefs) != 0 {
			t.Fatal("幂等重绑把已归档CLEANED重新激活")
		}
		if err := m.index.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

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
