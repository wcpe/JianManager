package acquire

// 同一 event_id 的多变体（同区间异正文）在投递资格门上的行为回归。
//
// ## 2026-10-29 架构变更对本文件的影响
//
// WAL 改为**纯内存队列**后：
//   - `source_wal` 表已删除，`stateindex` 不再有 WAL 行；
//   - 重启不再恢复队列内容，因此「重开索引后逐条还原 WAL」这件事本身已不存在。
//
// 但本文件真正要钉的语义**不是**“队列能持久化”，而是「资格门不得折叠变体 / 不得让同身份
// 的未提交条目借到资格」。这些全是内存队列 + 账本的语义，与表无关，因此保留，并把
// 「重启后」改成「用内存快照重新装入一份等价队列」（等价于新进程的起点状态）。
//
// 真实索引仍然参与：账本快照照旧经 `stateindex` 落盘与读回（那是持久化的真源），
// 只是不再有 WAL 行。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest/stateindex"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

var errSixFixQualificationIncomplete = errors.New("受控索引快照未完整提交")

func sixFixQualificationEvent(key ledger.SourceKey, start, end uint64, at string) logtypes.Event {
	event := logtypes.BuildEvent(logtypes.SourceIdentity{
		LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "acquire-v1",
	}, logtypes.RecordRange{Start: start, End: end}, at, at, "INFO", "stdout", "sample")
	event.Fields = map[string]string{"variant": at}
	return event
}

// 夹具使用生产索引组件保存**账本**（持久化的真源）；WAL 是内存队列，不再进索引。
//
// 额外带上几条 gap 行：只为让「单单元只写 1 行」的预算用例有足够的差异行可写
// （否则行数太少，「不完整提交」还没撑完预期轮次就做完了）。
func sixFixQualificationState(key ledger.SourceKey, led *ledger.Ledger) (stateindex.State, error) {
	owner := key.String()
	payload, err := json.Marshal(led.Snapshot())
	if err != nil {
		return stateindex.State{}, err
	}
	positions, _ := led.Positions(key)
	gaps := make([]stateindex.GapRow, 0, 3)
	for i := 0; i < 3; i++ {
		gaps = append(gaps, stateindex.GapRow{
			Key: owner, ID: int64(i), StartPos: uint64(i * 10), EndPos: uint64(i*10 + 9),
			Reason: "PAUSED", Detail: "qualification fixture",
		})
	}
	return stateindex.State{
		Sources:   []stateindex.SourceRow{{Key: owner, LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, StorageNamespace: owner}},
		Positions: []stateindex.PositionRow{{Key: owner, ReadPos: positions.Read, DurablePos: positions.Durable, ReclaimPos: positions.Reclaim}},
		Aux:       []stateindex.AuxRow{{Key: owner, Payload: payload}},
		Gaps:      gaps,
	}, nil
}

func sixFixQualificationPersist(store *stateindex.Store, state stateindex.State) error {
	stats, err := store.Apply(state)
	if err != nil {
		return err
	}
	if stats.Incomplete {
		return errSixFixQualificationIncomplete
	}
	return nil
}

// sixFixQualificationRestore 从索引读回账本，并把给定的内存种子装回队列——
// 等价于「新进程起来后：账本来自磁盘，队列为空或由调用方提供」。
func sixFixQualificationRestore(t *testing.T, key ledger.SourceKey, store *stateindex.Store) *ledger.Ledger {
	t.Helper()
	state, err := store.Load()
	require.NoError(t, err)
	require.Len(t, state.Aux, 1)
	var snapshot []ledger.Entry
	require.NoError(t, json.Unmarshal(state.Aux[0].Payload, &snapshot))
	led := ledger.New()
	require.NoError(t, led.Restore(snapshot))
	return led
}

// cloneEntries 复制一份条目作为「上一个进程留下的内存队列快照」。
func cloneEntries(w *WAL) []WALEntry { return w.Snapshot() }

// TestSixFixCommittedVariantsDrainKeepAllVariants
//
// 语义：同一 event_id、同区间但正文不同的多个变体，投递时必须**一个不折叠、一个不重排**；
// 资格门只按完整 CommitIdentity（含正文摘要）判定。重新装入等价队列后行为必须一致。
func TestSixFixCommittedVariantsDrainKeepAllVariants(t *testing.T) {
	for _, order := range []string{"旧新", "新旧", "同内容重复"} {
		t.Run(order, func(t *testing.T) {
			key := testKey("six-committed-variants", "g1")
			a := sixFixQualificationEvent(key, 7, 14, "2026-10-21T00:00:00Z")
			b := sixFixQualificationEvent(key, 7, 14, "2026-10-22T00:00:00Z")
			require.Equal(t, a.EventID, b.EventID)
			require.Equal(t, a.Record, b.Record)
			require.NotEqual(t, a.CanonicalHash, b.CanonicalHash)
			middle := []logtypes.Event{a, b}
			switch order {
			case "新旧":
				middle = []logtypes.Event{b, a}
			case "同内容重复":
				middle = []logtypes.Event{a, a}
			}
			want := append([]logtypes.Event{sixFixQualificationEvent(key, 0, 7, a.EventTimeUTC)}, middle...)
			want = append(want, sixFixQualificationEvent(key, 14, 21, b.EventTimeUTC))
			led := ledger.New()
			wal := NewWAL(led, key)
			// 普通追加忠实保留历史同身份变体；不经过会修订事件的回放去重入口。
			require.NoError(t, wal.Append(want...))
			require.NoError(t, wal.Commit())
			seeded := cloneEntries(wal)

			path := filepath.Join(t.TempDir(), "ingest.index.db")
			store, err := stateindex.Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			state, err := sixFixQualificationState(key, led)
			require.NoError(t, err)
			require.NoError(t, sixFixQualificationPersist(store, state))

			// 模拟二启/三启：账本从索引读回（真持久化），队列由内存快照装回（新架构语义）。
			for round := 2; round <= 3; round++ {
				led = sixFixQualificationRestore(t, key, store)
				wal = NewWAL(led, key)
				require.NoError(t, wal.seedEntries(seeded))
				require.Len(t, wal.Snapshot(), len(want))
				for i, entry := range wal.Snapshot() {
					require.Equal(t, uint64(i+1), entry.Seq)
					require.True(t, entry.Durable)
					require.Equal(t, want[i], entry.Event)
				}
				t.Logf("第%d次启动：WAL=%d，Durable=%d", round, len(wal.Snapshot()), led.Get(key).Positions.Durable)
			}

			pipe := NewPipeline(led, key, wal)
			persisted, deliveries := false, 0
			pipe.SetDurablePersist(func() error {
				captured, captureErr := sixFixQualificationState(key, led)
				if captureErr != nil {
					return captureErr
				}
				if persistErr := sixFixQualificationPersist(store, captured); persistErr != nil {
					return persistErr
				}
				persisted = true
				return nil
			})
			pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
				require.True(t, persisted, "真实提交完成之前不得外发")
				require.False(t, replay)
				require.Equal(t, want, events, "资格门不得折叠变体、吞副本或重排责任区间")
				deliveries++
				return 200, false, nil
			})
			got, err := pipe.DeliverPending()
			require.NoError(t, err)
			require.Equal(t, want, got)
			require.Equal(t, 1, deliveries)
			require.Equal(t, []ledger.PositionRange{{From: want[0].Record.Start, To: want[len(want)-1].Record.End}}, ledger.RequestDoneRangesOf(led.Get(key)))
		})
	}
}

func TestSixFixCommittedVariantsRejectUncommittedSiblings(t *testing.T) {
	for _, kind := range []string{"未追加", "已追加未提交", "区间伪造"} {
		t.Run(kind, func(t *testing.T) {
			key, led := testKey("six-uncommitted-variant", "g1"), ledger.New()
			wal := NewWAL(led, key)
			a := sixFixQualificationEvent(key, 0, 7, "2026-10-21T00:00:00Z")
			b := sixFixQualificationEvent(key, 0, 7, "2026-10-22T00:00:00Z")
			require.NoError(t, wal.Append(a))
			require.NoError(t, wal.Commit())
			switch kind {
			case "已追加未提交":
				require.NoError(t, wal.Append(b))
			case "区间伪造":
				b = a
				b.Record.End++
			}
			pipe := NewPipeline(led, key, wal)
			persists := 0
			pipe.SetDurablePersist(func() error { persists++; return nil })
			got, err := pipe.persistCommittedBatch([]logtypes.Event{a, b})
			require.ErrorContains(t, err, "without a matching WAL commit")
			require.Empty(t, got, "同批合法条目不能替未提交同身份变体借资格")
			require.Zero(t, persists, "整批复验必须先于持久化及外发")
		})
	}
}

func TestSixFixCommittedVariantsDrainSkipsNonDurableSibling(t *testing.T) {
	key, led := testKey("six-nondurable-variant", "g1"), ledger.New()
	wal := NewWAL(led, key)
	a := sixFixQualificationEvent(key, 0, 7, "2026-10-21T00:00:00Z")
	b := sixFixQualificationEvent(key, 0, 7, "2026-10-22T00:00:00Z")
	require.NoError(t, wal.Append(a))
	require.NoError(t, wal.Commit())
	require.NoError(t, wal.Append(b))
	seeded := cloneEntries(wal)

	path := filepath.Join(t.TempDir(), "ingest.index.db")
	store, err := stateindex.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	state, err := sixFixQualificationState(key, led)
	require.NoError(t, err)
	require.NoError(t, sixFixQualificationPersist(store, state))

	// 重新装入等价队列：非耐久标记必须由种子原样带过来，不能被同身份的耐久兄弟补造。
	led = sixFixQualificationRestore(t, key, store)
	wal = NewWAL(led, key)
	require.NoError(t, wal.seedEntries(seeded))
	require.True(t, wal.Snapshot()[0].Durable)
	require.False(t, wal.Snapshot()[1].Durable, "同身份耐久条目不能替另一变体补造提交证据")

	pipe := NewPipeline(led, key, wal)
	pipe.SetDurablePersist(func() error {
		state, captureErr := sixFixQualificationState(key, led)
		if captureErr != nil {
			return captureErr
		}
		return sixFixQualificationPersist(store, state)
	})
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
		require.Equal(t, []logtypes.Event{a}, events)
		return 200, false, nil
	})
	got, err := pipe.DeliverPending()
	require.NoError(t, err)
	require.Equal(t, []logtypes.Event{a}, got)

	// 队列里那条**非耐久**变体仍在，且耐久态未被改动（内存队列以自身条目为准）。
	//
	// 2026-10-29：不能按固定下标/固定条数断言——已投递的 `a` 会当场被回收剪掉。
	// 本用例的关切是「非耐久标记必须由种子原样带过来，不能被同身份的耐久兄弟补造」，
	// 所以按 canonical hash 定位那条变体并断言它仍未耐久。
	after := wal.Snapshot()
	var nonDurable *WALEntry
	for i, entry := range after {
		if entry.Event.CanonicalHash == b.CanonicalHash {
			nonDurable = &after[i]
			break
		}
	}
	require.NotNil(t, nonDurable, "非耐久变体必须仍在队列里（未被投递、不得被回收）")
	require.False(t, nonDurable.Durable, "同身份耐久条目不能替另一变体补造提交证据")
}

func TestSixFixCommittedVariantsPersistFailuresBlockDrain(t *testing.T) {
	for _, kind := range []string{"提交失败", "后台交办", "超时", "真实不完整提交"} {
		t.Run(kind, func(t *testing.T) {
			key, led := testKey("six-variant-persist", "g1"), ledger.New()
			wal := NewWAL(led, key)
			want := []logtypes.Event{
				sixFixQualificationEvent(key, 0, 7, "2026-10-21T00:00:00Z"),
				sixFixQualificationEvent(key, 0, 7, "2026-10-22T00:00:00Z"),
			}
			require.NoError(t, wal.Append(want...))
			require.NoError(t, wal.Commit())
			budget := stateindex.DefaultCommitBudget()
			if kind == "真实不完整提交" {
				budget.MaxRows, budget.MinRows, budget.CycleMaxRows = 1, 1, 1
			}
			store, err := stateindex.OpenWithBudget(filepath.Join(t.TempDir(), "ingest.index.db"), budget)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			pipe := NewPipeline(led, key, wal)
			persists, deliveries := 0, 0
			fail := true
			pipe.SetDurablePersist(func() error {
				persists++
				state, captureErr := sixFixQualificationState(key, led)
				if captureErr != nil {
					return captureErr
				}
				if kind == "真实不完整提交" || !fail {
					return sixFixQualificationPersist(store, state)
				}
				if kind == "超时" {
					return context.DeadlineExceeded
				}
				return fmt.Errorf("受控%s", kind)
			})
			pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
				require.Equal(t, want, events)
				deliveries++
				return 200, false, nil
			})
			for n := 0; n < 3; n++ {
				got, drainErr := pipe.DeliverPending()
				require.Error(t, drainErr)
				if kind == "真实不完整提交" {
					require.ErrorIs(t, drainErr, errSixFixQualificationIncomplete)
				}
				require.Empty(t, got)
				require.Zero(t, deliveries)
				require.Equal(t, n+1, persists, "合法变体应到达真实持久化门，而非被身份折叠误拒")
				require.Empty(t, ledger.RequestDoneRangesOf(led.Get(key)))
			}
			fail = false
			for n := 0; n < 10; n++ {
				got, drainErr := pipe.DeliverPending()
				if errors.Is(drainErr, errSixFixQualificationIncomplete) {
					require.Zero(t, deliveries)
					continue
				}
				require.NoError(t, drainErr)
				require.Equal(t, want, got)
				break
			}
			require.Equal(t, 1, deliveries, "真实完整提交后应解除阻塞，不能被旧内存状态永久焊死")
		})
	}
}

func TestSixFixCommittedVariantsCaptureExcludesConcurrentAppend(t *testing.T) {
	key, led := testKey("six-concurrent-variant", "g1"), ledger.New()
	wal := NewWAL(led, key)
	want := []logtypes.Event{
		sixFixQualificationEvent(key, 0, 7, "2026-10-20T00:00:00Z"),
		sixFixQualificationEvent(key, 0, 7, "2026-10-21T00:00:00Z"),
	}
	require.NoError(t, wal.Append(want...))
	require.NoError(t, wal.Commit())
	store, err := stateindex.Open(filepath.Join(t.TempDir(), "ingest.index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	pipe := NewPipeline(led, key, wal)
	started, release := make(chan struct{}), make(chan struct{})
	pipe.SetDurablePersist(func() error {
		state, captureErr := sixFixQualificationState(key, led)
		if captureErr != nil {
			return captureErr
		}
		close(started)
		<-release
		return sixFixQualificationPersist(store, state)
	})
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
		// 外发回调修改附加字段不能污染 WAL；同 ID 的不同变体也不能共用字段容器。
		for i := range events {
			events[i].Fields["variant"] = "仅外发副本"
		}
		return 200, false, nil
	})
	type drainResult struct {
		events []logtypes.Event
		err    error
	}
	done := make(chan drainResult, 1)
	go func() {
		events, drainErr := pipe.DeliverPending()
		done <- drainResult{events: events, err: drainErr}
	}()
	select {
	case <-started:
	case result := <-done:
		t.Fatalf("未进入真实持久化门即结束：%v", result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("真实持久化门未启动")
	}
	later := sixFixQualificationEvent(key, 0, 7, "2026-10-22T00:00:00Z")
	uncommitted := sixFixQualificationEvent(key, 0, 7, "2026-10-23T00:00:00Z")
	// 持久回调在飞时并发追加同 ID 的后续已提交变体及未提交尾部，不纳入本批授权。
	require.NoError(t, wal.Append(later))
	require.NoError(t, wal.Commit())
	require.NoError(t, wal.Append(uncommitted))
	close(release)
	select {
	case result := <-done:
		require.NoError(t, result.err)
		require.Len(t, result.events, len(want))
		for i, event := range result.events {
			require.Equal(t, want[i].CanonicalHash, event.CanonicalHash)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("并发追加后排空未完成")
	}
	entries := wal.Snapshot()
	// 2026-10-29：不能按固定下标/固定条数断言。本用例的关切是
	// 「本批真实持久化快照不应把回调期间追加的**未提交**变体算进去」，按 canonical hash 定位它。
	//
	// 为什么只剩 1 条：`want` 两条已投递且被确认（reclaim 推到 7）；回调期间追加的
	// `later` 区间也是 [0,7) 且已 Commit ⇒ 同样落在回收水位之内被剪掉（正确的行为：
	// 它已在队列里被确认过的那段区间里）。只有**未提交**的 `uncommitted` 留下。
	require.Len(t, entries, 1, "回调期间追加的未提交变体仍在队列里（未投递、不得被回收）")
	uncommittedEntry := entries[0]
	require.Equal(t, uncommitted.CanonicalHash, uncommittedEntry.Event.CanonicalHash)
	require.False(t, uncommittedEntry.Durable, "本批真实持久化快照不应把回调期间追加的未提交变体算进去")
}
