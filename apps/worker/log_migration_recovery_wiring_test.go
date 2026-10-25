package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/lifecycle"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

type wiringObservedRecoveryBudget struct {
	budget *vlsup.BudgetController
	seen   chan error
}

func (b wiringObservedRecoveryBudget) Admit(work vlsup.WorkClass) error {
	err := b.budget.Admit(work)
	select {
	case b.seen <- err:
	default:
	}
	return err
}

func knownWiringRecoverySample() vlsup.ResourceSample {
	return vlsup.ResourceSample{WorkerRSSKnown: true, DiskKnown: true, DiskBytesKnown: true,
		Namespaces: map[vlsup.Namespace]vlsup.NamespaceSample{
			vlsup.NamespaceHot: {RSSKnown: true}, vlsup.NamespaceCold: {RSSKnown: true}, vlsup.NamespaceRehydrate: {RSSKnown: true},
		}}
}

func frozenWiringRecoveryDay(t *testing.T, keepQueryView bool) (*catalog.Catalog, catalog.PartitionKey, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.jsonl")
	open := func() *catalog.Catalog {
		store, err := catalog.NewJSONLFileStore(path)
		require.NoError(t, err)
		journal, err := catalog.NewJournalWithStore(store)
		require.NoError(t, err)
		return catalog.New(journal)
	}
	cat := open()
	key := catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-22"}
	record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "20260922")
	record.PublishedProjection = wiringRange(catalog.OwnerHot).Projection
	record.PublishedProjection.ManifestVersion = "persisted-source"
	require.NoError(t, cat.Put(record))
	var viewID string
	if keepQueryView {
		var err error
		viewID, err = cat.NewQueryViewID(time.Now())
		require.NoError(t, err)
		_, err = cat.SnapshotAndLease(catalog.QuerySnapshotRequest{ViewID: viewID, Keys: []catalog.PartitionKey{key}, TTL: time.Minute})
		require.NoError(t, err)
	}
	_, err := cat.BeginDayMigration(key.UTCDay, catalog.OwnerCold, "20260922")
	require.NoError(t, err)
	// 未启动的普通热分区不能因恢复tick而被自动搬运。
	stable := catalog.NewStableRecord(catalog.PartitionKey{StorageNamespace: "inst:2", UTCDay: "2026-09-21"}, catalog.OwnerHot, 1, "20260921")
	require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: stable.Key, Record: stable,
		State: catalog.StateCleaned, Owner: stable.Owner, Generation: stable.Generation, DirID: stable.OwnerDirID,
		AuthorityCommit: true, JournalComplete: true}))
	if !keepQueryView {
		cat = open()
	}
	cat.StartingRecover()
	return cat, key, viewID
}

func wiringRecoveryDayManager(cat *catalog.Catalog) *lifecycle.DayManager {
	ops := lifecycle.Ops{
		Drain:      func(catalog.PartitionKey) error { return nil },
		Snapshot:   func(catalog.PartitionKey, string) (string, error) { return "snapshot", nil },
		Copy:       func(catalog.PartitionKey, string, string) (string, error) { return "copy", nil },
		Verify:     func(catalog.PartitionKey, string, string) (string, error) { return "verified", nil },
		Attach:     func(catalog.PartitionKey, string) error { return nil },
		LeaseDrain: func(catalog.PartitionKey, uint64) error { return nil },
		Detach:     func(catalog.PartitionKey, string) error { return nil },
		Cleanup:    func(catalog.PartitionKey, string) error { return nil },
	}
	return lifecycle.NewDayManager(cat, ops, lifecycle.RetentionGateFunc(func(catalog.PartitionKey, string, string) (bool, string) { return true, "verified-receiver" }))
}

// TestWiringRecoveryLoopRetriesWithRetentionDisabled 首轮失败后必须继续恢复既有责任，不能依赖开启新搬运策略。
func TestWiringRecoveryLoopRetriesWithRetentionDisabled(t *testing.T) {
	for _, blockedBy := range []string{"receiver", "budget", "read-pin"} {
		t.Run(blockedBy, func(t *testing.T) {
			require.False(t, retention.Policy{}.Enabled)
			transport := &wiringTransport{rows: map[string][]byte{}, health: true}
			supervisor := newWiringSupervisor(t, t.TempDir(), &wiringFakeFactory{}, transport, "constructed-password")
			clients, err := newManagedLogClients(supervisor)
			require.NoError(t, err)
			require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceHot))
			if blockedBy != "receiver" {
				require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceCold))
			}
			cat, key, viewID := frozenWiringRecoveryDay(t, blockedBy == "read-pin")
			budget, err := vlsup.NewBudgetController(vlsup.RuntimeBudget{Namespaces: vlsup.DefaultNamespaceBudgets()})
			require.NoError(t, err)
			if blockedBy != "budget" {
				budget.Update(knownWiringRecoverySample())
			}
			recovery := newLogPhysicalDayRecovery(wiringRecoveryDayManager(cat), cat, clients, budget)
			seenReady, seenBudget, seenResume := make(chan error, 4), make(chan error, 4), make(chan error, 4)
			ready, resume := recovery.ready, recovery.resume
			recovery.ready = func(ctx context.Context) error { err := ready(ctx); seenReady <- err; return err }
			recovery.budget = wiringObservedRecoveryBudget{budget: budget, seen: seenBudget}
			recovery.resume = func(day string) error { err := resume(day); seenResume <- err; return err }
			var releasePin func()
			if blockedBy == "read-pin" {
				releasePin, err = cat.PinLeasedRead(viewID, time.Now())
				require.NoError(t, err)
				defer releasePin()
			}
			ctx, cancel := context.WithCancel(context.Background())
			ticks := make(chan time.Time)
			done := runLogPhysicalDayRecovery(ctx, recovery.step, ticks)
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("取消后恢复循环未退出")
				}
			})
			var first error
			switch blockedBy {
			case "receiver":
				select {
				case first = <-seenReady:
				case <-time.After(2 * time.Second):
					t.Fatal("没有执行首轮ready检查")
				}
				require.Error(t, first)
				require.NoError(t, supervisor.Start(context.Background(), vlsup.NamespaceCold))
			case "budget":
				select {
				case first = <-seenBudget:
				case <-time.After(2 * time.Second):
					t.Fatal("没有执行首轮预算检查")
				}
				require.Error(t, first)
				budget.Update(knownWiringRecoverySample())
			case "read-pin":
				select {
				case first = <-seenResume:
				case <-time.After(2 * time.Second):
					t.Fatal("没有执行首轮持久迁移恢复")
				}
				require.Error(t, first)
				require.NoError(t, cat.ReleaseQueryView(viewID))
				releasePin()
			}
			select {
			case ticks <- time.Now():
			case <-done:
				t.Fatal("首轮未ready/预算拒绝/旧pin未结束后，恢复循环错误退出")
			case <-time.After(2 * time.Second):
				t.Fatal("恢复循环未消费下一次有限tick")
			}
			select {
			case err := <-seenResume:
				require.NoError(t, err)
			case <-time.After(2 * time.Second):
				t.Fatal("条件恢复后没有续跑已持久迁移")
			}
			record, exists := cat.Get(key)
			require.True(t, exists)
			require.Equal(t, catalog.StateCleaned, record.MigrationState)
			require.Equal(t, catalog.OwnerCold, record.Owner)
			permit, err := cat.AcquireWritePermit(catalog.PartitionKey{StorageNamespace: "late", UTCDay: key.UTCDay})
			require.NoError(t, err, "恢复后物理日必须重新允许迟到写入")
			cat.ReleaseWritePermit(permit)
			stable, stableExists := cat.Get(catalog.PartitionKey{StorageNamespace: "inst:2", UTCDay: "2026-09-21"})
			require.True(t, stableExists)
			require.Equal(t, catalog.OwnerHot, stable.Owner, "恢复不能开启普通热分区的新迁移")
			require.Equal(t, catalog.StateCleaned, stable.MigrationState)
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("取消后仍有恢复调度")
			}
		})
	}
}

// TestWiringRecoveryIdleHotOnlyDoesNotProbe 无持久迁移责任时，合法HOT-only不因冷层关闭误告警或探活。
func TestWiringRecoveryIdleHotOnlyDoesNotProbe(t *testing.T) {
	cat := catalog.New(nil)
	budget, err := vlsup.NewBudgetController(vlsup.RuntimeBudget{})
	require.NoError(t, err)
	seen := make(chan error, 1)
	probes := 0
	recovery := &logPhysicalDayRecovery{cat: cat, budget: wiringObservedRecoveryBudget{budget: budget, seen: seen},
		ready:  func(context.Context) error { probes++; return errors.New("cold-disabled") },
		resume: func(string) error { t.Error("没有既有责任却启动了迁移"); return nil }}
	recovery.step(context.Background())
	require.Zero(t, probes)
	select {
	case <-seen:
		t.Fatal("没有pending却进入未知预算告警路径")
	default:
	}
}

// TestWiringRecoveryPassIsBoundedAndFair 失败日也应轮转，每轮最多四日且取消后不再下发。
func TestWiringRecoveryPassIsBoundedAndFair(t *testing.T) {
	cat := catalog.New(nil)
	for day := 1; day <= 9; day++ {
		key := catalog.PartitionKey{StorageNamespace: "legacy", UTCDay: time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC).Format("2006-01-02")}
		record := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot")
		record.MigrationState = catalog.StateRoutingFrozen
		require.NoError(t, cat.Put(record))
	}
	budget, err := vlsup.NewBudgetController(vlsup.RuntimeBudget{})
	require.NoError(t, err)
	budget.Update(knownWiringRecoverySample())
	var calls []string
	var inFlight, maxInFlight atomic.Int32
	recovery := &logPhysicalDayRecovery{cat: cat, budget: budget, ready: func(context.Context) error { return nil },
		resume: func(day string) error {
			now := inFlight.Add(1)
			maxInFlight.Store(now)
			defer inFlight.Add(-1)
			calls = append(calls, day)
			return errors.New("retryable-existing-day")
		}}
	recovery.step(context.Background())
	require.Len(t, calls, 4)
	recovery.step(context.Background())
	require.Len(t, calls, 8)
	require.NotEqual(t, calls[0], calls[4], "失败的第一日不能饿死后续日")
	require.EqualValues(t, 1, maxInFlight.Load())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recovery.step(ctx)
	require.Len(t, calls, 8)
}
