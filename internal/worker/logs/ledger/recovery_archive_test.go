package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

type recoveryArchiveFixture struct {
	mu      sync.Mutex
	records map[recoveryUseKey]RecoveryArchiveRecord
	fail    error
	before  func()
	lookup  func()
}

func newRecoveryArchiveFixture() *recoveryArchiveFixture {
	return &recoveryArchiveFixture{records: make(map[recoveryUseKey]RecoveryArchiveRecord)}
}

func (s *recoveryArchiveFixture) ArchiveRecovery(ctx context.Context, records []RecoveryArchiveRecord) ([]RecoveryArchiveReceipt, error) {
	if s.before != nil {
		s.before()
	}
	if s.fail != nil {
		return nil, s.fail
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var receipts []RecoveryArchiveReceipt
	for _, record := range records {
		if err := ValidateRecoveryArchiveRecord(record); err != nil {
			return nil, err
		}
		id := recoveryUseKey{Key: record.Key, SegmentID: record.Ref.SegmentID}
		if old, ok := s.records[id]; ok && old != record {
			return nil, errors.New("夹具历史身份冲突")
		}
		s.records[id] = record
		receipts = append(receipts, archiveFixtureReceipt(record))
	}
	return receipts, nil
}

func (s *recoveryArchiveFixture) LookupRecovery(ctx context.Context, key SourceKey, segmentID string) (RecoveryArchiveRecord, RecoveryArchiveReceipt, bool, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryArchiveRecord{}, RecoveryArchiveReceipt{}, false, err
	}
	s.mu.Lock()
	record, ok := s.records[recoveryUseKey{Key: key, SegmentID: segmentID}]
	s.mu.Unlock()
	if s.lookup != nil {
		s.lookup()
	}
	return record, archiveFixtureReceipt(record), ok, nil
}

func archiveFixtureReceipt(record RecoveryArchiveRecord) RecoveryArchiveReceipt {
	return RecoveryArchiveReceipt{Key: record.Key, SegmentID: record.Ref.SegmentID, Fingerprint: record.Fingerprint, ArchiveID: "fixture-archive:" + record.Fingerprint}
}

func registerCleanedFixture(t *testing.T, l *Ledger, key SourceKey, id string, from, to uint64) RecoveryRef {
	t.Helper()
	ref := RecoveryRef{SegmentID: id, Path: "fixture/recovery", State: logtypes.RecoveryStaged, CoversFrom: from, CoversTo: to}
	if err := l.RegisterRecovery(key, ref); err != nil {
		t.Fatal(err)
	}
	for _, state := range []logtypes.RecoverySegmentState{logtypes.RecoveryDurableVerified, logtypes.RecoveryWALResponsibilityXfer, logtypes.RecoveryReleased, logtypes.RecoveryCleaned} {
		if err := l.TransitionRecovery(key, id, state, logtypes.ReleaseProjectionBacked, "fixture-canonical"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.TryReclaim(key); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestRecoveryArchiveFailureAndMismatchedReceiptsDoNotPrune(t *testing.T) {
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	store := newRecoveryArchiveFixture()
	l.SetRecoveryArchiveStore(store)
	registerCleanedFixture(t, l, key, "fixture-ref", 0, 10)
	store.fail = errors.New("夹具归档提交失败")
	before := l.Get(key)
	if removed, err := l.ArchiveCleanedRecovery(context.Background(), key, 8); removed != 0 || err == nil || !reflect.DeepEqual(l.Get(key), before) {
		t.Fatalf("归档失败裁掉活动责任：removed=%d err=%v", removed, err)
	}
	candidates := l.RecoveryArchiveCandidates(key, 8)
	if len(candidates) != 1 {
		t.Fatalf("缺少候选：%+v", candidates)
	}
	receipt := archiveFixtureReceipt(candidates[0])
	receipt.Fingerprint = "fixture-wrong-fingerprint"
	if removed, err := l.CommitRecoveryArchive(candidates, []RecoveryArchiveReceipt{receipt}); removed != 0 || err == nil || !reflect.DeepEqual(l.Get(key), before) {
		t.Fatalf("错误回执裁掉活动责任：removed=%d err=%v", removed, err)
	}
	store.fail = nil
	if removed, err := l.ArchiveCleanedRecovery(context.Background(), key, 8); removed != 1 || err != nil {
		t.Fatalf("成功归档未摘除：removed=%d err=%v", removed, err)
	}
	if len(store.records) != 1 || len(l.Get(key).RecoveryRefs) != 0 {
		t.Fatal("完整审计未分离到仓储")
	}
	if removed, err := l.CommitRecoveryArchive(candidates, []RecoveryArchiveReceipt{archiveFixtureReceipt(candidates[0])}); removed != 0 || err != nil {
		t.Fatalf("重复回执不幂等：removed=%d err=%v", removed, err)
	}
}

func TestRecoveryArchiveConcurrentHoldAndConsumerPreventPruneWithoutLockingIO(t *testing.T) {
	for _, mode := range []string{"hold", "lease", "consumer"} {
		t.Run(mode, func(t *testing.T) {
			l := New()
			key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
			l.Ensure(key, logtypes.SourceIdentity{})
			store := newRecoveryArchiveFixture()
			l.SetRecoveryArchiveStore(store)
			registerCleanedFixture(t, l, key, "fixture-ref", 0, 10)
			entered, resume := make(chan struct{}), make(chan struct{})
			store.before = func() { close(entered); <-resume }
			type result struct {
				removed int
				err     error
			}
			done := make(chan result, 1)
			go func() {
				removed, err := l.ArchiveCleanedRecovery(context.Background(), key, 8)
				done <- result{removed, err}
			}()
			<-entered
			var release func()
			if mode == "hold" {
				if err := l.SetRecoveryHold(key, "fixture-ref", true); err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				release, err = l.AcquireRecoveryUse(key, "fixture-ref", mode+":fixture")
				if err != nil {
					t.Fatal(err)
				}
			}
			close(resume)
			resultValue := <-done
			if resultValue.err != nil || resultValue.removed != 0 || len(l.Get(key).RecoveryRefs) != 1 || len(store.records) != 1 {
				t.Fatalf("归档IO期间的新使用者被忽略：%+v", resultValue)
			}
			if mode == "hold" {
				if err := l.SetRecoveryHold(key, "fixture-ref", false); err != nil {
					t.Fatal(err)
				}
			} else {
				release()
				release()
			}
			store.before = nil
			if removed, err := l.ArchiveCleanedRecovery(context.Background(), key, 8); err != nil || removed != 1 {
				t.Fatalf("解除持有后不能幂等续归档：removed=%d err=%v", removed, err)
			}
			if err := l.SetRecoveryHold(key, "fixture-ref", true); err == nil {
				t.Fatal("已摘除引用接受了无效hold")
			}
			if _, err := l.AcquireRecoveryUse(key, "fixture-ref", "late:fixture"); err == nil {
				t.Fatal("已摘除引用接受了新的消费者")
			}
		})
	}
}

func TestRecoveryArchiveCandidateFingerprintMustStillMatch(t *testing.T) {
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	registerCleanedFixture(t, l, key, "fixture-ref", 0, 10)
	candidates := l.RecoveryArchiveCandidates(key, 8)
	l.mu.Lock()
	l.entries[key].RecoveryRefs[0].RetentionDeadlineUnixSec = 12345
	l.mu.Unlock()
	if removed, err := l.CommitRecoveryArchive(candidates, []RecoveryArchiveReceipt{archiveFixtureReceipt(candidates[0])}); removed != 0 || err != nil || len(l.Get(key).RecoveryRefs) != 1 {
		t.Fatalf("旧候选裁掉已变更引用：removed=%d err=%v", removed, err)
	}
}

func TestRecoveryArchiveLongRun1000BatchesAndThreeRestartRebinds(t *testing.T) {
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	store := newRecoveryArchiveFixture()
	l.SetRecoveryArchiveStore(store)
	var refs []RecoveryRef
	for i := uint64(0); i < 1000; i++ {
		if err := l.AdvanceRead(key, (i+1)*10); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, registerCleanedFixture(t, l, key, fmt.Sprintf("fixture-%04d", i), i*10, (i+1)*10))
		if (i+1)%16 == 0 || i == 999 {
			if _, err := l.ArchiveCleanedRecovery(context.Background(), key, 16); err != nil {
				t.Fatal(err)
			}
		}
		if active := len(l.Get(key).RecoveryRefs); active > 15 {
			t.Fatalf("活动元数据随已清理历史增长：batch=%d active=%d", i, active)
		}
	}
	if len(store.records) != 1000 || len(l.Get(key).RecoveryRefs) != 0 {
		t.Fatalf("完整审计丢失或活动引用未裁：history=%d active=%d", len(store.records), len(l.Get(key).RecoveryRefs))
	}
	for round := 1; round <= 3; round++ {
		data, err := json.Marshal(l.Snapshot())
		if err != nil {
			t.Fatal(err)
		}
		var entries []Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		restarted := New()
		if err := restarted.Restore(entries); err != nil {
			t.Fatal(err)
		}
		restarted.SetRecoveryArchiveStore(store)
		beforeRevision := restarted.Revision(key)
		for _, ref := range refs {
			if err := restarted.RegisterRecovery(key, ref); err != nil {
				t.Fatalf("第%d轮重绑失败：%v", round, err)
			}
		}
		if len(restarted.Get(key).RecoveryRefs) != 0 || restarted.Revision(key) != beforeRevision || len(store.records) != 1000 {
			t.Fatalf("第%d轮已清理引用被复活或审计丢失", round)
		}
		conflict := refs[0]
		conflict.Path = "fixture/conflicting-recovery"
		if err := restarted.RegisterRecovery(key, conflict); err == nil {
			t.Fatal("已归档SegmentID被重绑到不同身份")
		}
		l = restarted
	}
}

func TestRecoveryArchiveRebindRacingPruneDoesNotResurrect(t *testing.T) {
	l := New()
	key := SourceKey{LogSourceID: "fixture-source", SourceGeneration: "fixture-generation"}
	l.Ensure(key, logtypes.SourceIdentity{})
	store := newRecoveryArchiveFixture()
	l.SetRecoveryArchiveStore(store)
	ref := registerCleanedFixture(t, l, key, "fixture-ref", 0, 10)
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	store.lookup = func() { once.Do(func() { close(entered); <-resume }) }
	done := make(chan error, 1)
	go func() { done <- l.RegisterRecovery(key, ref) }()
	<-entered
	if removed, err := l.ArchiveCleanedRecovery(context.Background(), key, 8); removed != 1 || err != nil {
		t.Fatalf("并发归档失败：removed=%d err=%v", removed, err)
	}
	close(resume)
	if err := <-done; err != nil || len(l.Get(key).RecoveryRefs) != 0 {
		t.Fatalf("锁外旧lookup将已归档引用复活：err=%v", err)
	}
}
