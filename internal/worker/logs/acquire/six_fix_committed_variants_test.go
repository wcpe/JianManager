package acquire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
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

// 夹具使用生产索引组件保存逐条 WAL 及账本；不把内存标志、JSON 往返或持久化桩当作落盘证据。
// acquire.WAL 本身没有路径化打开和关闭接口，真实重开的是承载其快照的索引库。
func sixFixQualificationState(key ledger.SourceKey, led *ledger.Ledger, wal *WAL) (stateindex.State, error) {
	owner := key.String()
	payload, err := json.Marshal(led.Snapshot())
	if err != nil {
		return stateindex.State{}, err
	}
	positions, _ := led.Positions(key)
	state := stateindex.State{
		Sources:   []stateindex.SourceRow{{Key: owner, LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, StorageNamespace: owner}},
		Positions: []stateindex.PositionRow{{Key: owner, ReadPos: positions.Read, DurablePos: positions.Durable, ReclaimPos: positions.Reclaim}},
		Aux:       []stateindex.AuxRow{{Key: owner, Payload: payload}},
	}
	for _, entry := range wal.Snapshot() {
		body, marshalErr := json.Marshal(entry.Event)
		if marshalErr != nil {
			return stateindex.State{}, marshalErr
		}
		row := stateindex.WALRow{
			Key: owner, Seq: entry.Seq, EventID: entry.Event.EventID,
			RecordStart: entry.Event.Record.Start, RecordEnd: entry.Event.Record.End,
			Appended: entry.Appended, Durable: entry.Durable, Replay: entry.Replay, Body: body,
		}
		hash := fnv.New64a()
		if _, writeErr := hash.Write(body); writeErr != nil {
			return stateindex.State{}, writeErr
		}
		row.FP = hash.Sum64()
		state.WAL = append(state.WAL, row)
	}
	return state, nil
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

func sixFixQualificationRestore(t *testing.T, key ledger.SourceKey, store *stateindex.Store) (*ledger.Ledger, *WAL) {
	t.Helper()
	state, err := store.Load()
	require.NoError(t, err)
	require.Len(t, state.Aux, 1)
	var snapshot []ledger.Entry
	require.NoError(t, json.Unmarshal(state.Aux[0].Payload, &snapshot))
	led := ledger.New()
	require.NoError(t, led.Restore(snapshot))
	entries := make([]WALEntry, 0, len(state.WAL))
	for _, row := range state.WAL {
		var event logtypes.Event
		require.NoError(t, json.Unmarshal(row.Body, &event))
		require.Equal(t, row.EventID, event.EventID)
		require.Equal(t, row.RecordStart, event.Record.Start)
		require.Equal(t, row.RecordEnd, event.Record.End)
		entries = append(entries, WALEntry{Seq: row.Seq, Event: event, Appended: row.Appended, Durable: row.Durable, Replay: row.Replay})
	}
	wal := NewWAL(led, key)
	require.NoError(t, wal.Restore(entries))
	return led, wal
}

func TestSixFixCommittedVariantsDrainAfterIndexReopen(t *testing.T) {
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
			path := filepath.Join(t.TempDir(), "ingest.index.db")
			store, err := stateindex.Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			state, err := sixFixQualificationState(key, led, wal)
			require.NoError(t, err)
			require.NoError(t, sixFixQualificationPersist(store, state))
			// 原路径原库连续二启、三启；Seq 和 Durable 必须来自真实已提交行，不能补 true。
			for round := 2; round <= 3; round++ {
				require.NoError(t, store.Close())
				store, err = stateindex.Open(path)
				require.NoError(t, err)
				led, wal = sixFixQualificationRestore(t, key, store)
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
				captured, captureErr := sixFixQualificationState(key, led, wal)
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
	path := filepath.Join(t.TempDir(), "ingest.index.db")
	store, err := stateindex.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	state, err := sixFixQualificationState(key, led, wal)
	require.NoError(t, err)
	require.NoError(t, sixFixQualificationPersist(store, state))
	require.NoError(t, store.Close())
	store, err = stateindex.Open(path)
	require.NoError(t, err)
	led, wal = sixFixQualificationRestore(t, key, store)
	require.False(t, wal.Snapshot()[1].Durable, "重开不能由同身份耐久条目补造另一变体的提交证据")
	pipe := NewPipeline(led, key, wal)
	pipe.SetDurablePersist(func() error {
		state, captureErr := sixFixQualificationState(key, led, wal)
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
	loaded, err := store.Load()
	require.NoError(t, err)
	require.Len(t, loaded.WAL, 2)
	require.True(t, loaded.WAL[0].Durable)
	require.False(t, loaded.WAL[1].Durable, "真实索引不能把无提交证据的旧条目标成耐久")
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
				state, captureErr := sixFixQualificationState(key, led, wal)
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
		state, captureErr := sixFixQualificationState(key, led, wal)
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
	require.Len(t, entries, 4)
	for i := range want {
		require.Equal(t, want[i], entries[i].Event)
	}
	require.True(t, entries[2].Durable)
	require.False(t, entries[3].Durable)
	loaded, err := store.Load()
	require.NoError(t, err)
	require.Len(t, loaded.WAL, len(want), "本批真实持久化快照不应混入回调期间追加的变体")
}
