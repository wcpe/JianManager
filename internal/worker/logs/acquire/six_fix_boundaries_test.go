package acquire

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func TestSixFixPersistCaptureDoesNotAuthorizeLaterCommit(t *testing.T) {
	for _, drain := range []bool{false, true} {
		t.Run(map[bool]string{false: "正常摄取", true: "存量排空"}[drain], func(t *testing.T) {
			key, led := testKey("six-captured", "g1"), ledger.New()
			wal := NewWAL(led, key)
			pipe := NewPipeline(led, key, wal)
			first, later, uncommitted := sixFixEvent(key, 0, 7), sixFixEvent(key, 7, 14), sixFixEvent(key, 14, 21)
			var delivered []logtypes.Event
			pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
				delivered = append(delivered, events...)
				return 200, false, nil
			})
			pipe.SetDurablePersist(func() error {
				// 模拟提交门在飞时另一个已提交批次加入，以及尚未 Commit 的尾部。
				require.NoError(t, wal.Append(later))
				require.NoError(t, wal.Commit())
				require.NoError(t, wal.Append(uncommitted))
				return nil
			})
			if drain {
				require.NoError(t, wal.Append(first))
				require.NoError(t, wal.Commit())
				_, err := pipe.DeliverPending()
				require.NoError(t, err)
			} else {
				require.NoError(t, pipe.Ingest([]logtypes.Event{first}))
			}
			require.Len(t, delivered, 1)
			require.Equal(t, first.EventID, delivered[0].EventID, "本次persist只授权在回调前捕获的批次")
			// 2026-10-29：不能按固定下标取尾部条目——`first` 投递成功后会当场被回收剪掉，
			// 队列位置整体前移。改为按 event_id 找那个**未 Commit** 的尾部条目。
			var uncommittedEntry *WALEntry
			for i, entry := range wal.Snapshot() {
				if entry.Event.EventID == uncommitted.EventID {
					uncommittedEntry = &wal.Snapshot()[i]
					break
				}
			}
			require.NotNil(t, uncommittedEntry, "未 Commit 的尾部条目必须仍在队列里")
			require.False(t, uncommittedEntry.Durable, "未 Commit 的尾部不得被本次 persist 授权为已耐久")
		})
	}
}

func TestSixFixLineBudgetPrecedesAllocationAndCanRecover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "latest.log")
	original := []byte(strings.Repeat("x", 2<<20))
	require.NoError(t, os.WriteFile(path, original, 0600))
	key, led := testKey("six-allocation", "g1"), ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, path, wal, nil)
	require.NoError(t, tailer.SetReadLimits(ReadLimits{MaxLineBytes: 4096, MaxBytesPerPoll: 16384}))
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	_, err := tailer.Poll()
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	t.Logf("sourceBytes=%d maxLineBytes=4096 maxPollBytes=16384 totalAllocDelta=%d heapAllocBefore=%d heapAllocAfter=%d pollDuration=%s", len(original), after.TotalAlloc-before.TotalAlloc, before.HeapAlloc, after.HeapAlloc, elapsed)
	var limitErr *ReadLimitError
	require.ErrorAs(t, err, &limitErr)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(512<<10), "2MiB无换行行必须在小块读取阶段停止，而非先分配完整正文")
	for n := 0; n < 3; n++ {
		_, err := tailer.Poll()
		require.ErrorAs(t, err, &limitErr)
	}
	require.Equal(t, 1, led.UnresolvedGapCount(key))
	require.Zero(t, tailer.cursor)
	require.Zero(t, tailer.LogicalPos())
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, actual)

	// 运营者显式放宽限额后，仍形成一条原始区间事件，不裸截断或改变事件身份推导。
	require.NoError(t, tailer.SetReadLimits(ReadLimits{MaxLineBytes: len(original) + 1, MaxBytesPerPoll: int64(len(original) + 2)}))
	appendBytes(t, path, []byte{'\n'})
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, uint64(len(original)+1), events[0].Record.End)
	require.Equal(t, string(original), events[0].Message)
	pipe := NewPipeline(led, key, wal)
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) { return 200, false, nil })
	require.NoError(t, pipe.Ingest(events))
	require.Zero(t, led.UnresolvedGapCount(key), "完整原行真正提交和请求成功后才可按精确范围解除背压")
}

func TestSixFixPollByteBudgetAndLineLimitsHaveExactRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "latest.log")
	body := strings.Repeat("0123456789\n", 30)
	require.NoError(t, os.WriteFile(path, []byte(body), 0600))
	key, led := testKey("six-round", "g1"), ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, path, wal, nil)
	require.NoError(t, tailer.SetReadLimits(ReadLimits{MaxLineBytes: 16, MaxBytesPerPoll: 49}))
	var all []logtypes.Event
	for n := 0; n < 30 && len(all) < 30; n++ {
		before := tailer.LogicalPos()
		events, err := tailer.Poll()
		require.NoError(t, err)
		require.NotEmpty(t, events)
		require.LessOrEqual(t, tailer.LogicalPos()-before, uint64(49))
		require.NoError(t, wal.Append(events...))
		require.NoError(t, wal.Commit())
		all = append(all, events...)
	}
	require.Len(t, all, 30)
	for i, event := range all {
		require.Equal(t, uint64(i*11), event.Record.Start)
		require.Equal(t, uint64((i+1)*11), event.Record.End)
	}
}

func TestSixFixFiniteArchiveByteBudgetAndDirtyRestart(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "纯文本", true: "压缩"}[compressed], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.log")
			lines := make([]string, 30)
			for i := range lines {
				lines[i] = "0123456789"
			}
			if compressed {
				path += ".gz"
				writeGzip(t, path, lines)
			} else {
				require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600))
			}
			key, led := testKey("six-finite-budget", "g1"), ledger.New()
			imp := NewArchiveImporter(led, key)
			require.NoError(t, imp.SetReadLimits(ReadLimits{MaxLineBytes: 16, MaxBytesPerPoll: 49}))
			var all []logtypes.Event
			restarts := 0
			for n := 0; n < 120; n++ {
				var result *ImportResult
				var err error
				if compressed {
					result, err = imp.ImportGzipBounded(path, 3)
				} else {
					result, err = imp.ImportTextBounded(path, 3)
				}
				require.NoError(t, err)
				var bytes uint64
				for _, event := range result.Events {
					bytes += event.Record.End - event.Record.Start
				}
				require.LessOrEqual(t, bytes, uint64(49))
				all = append(all, result.Events...)
				// 上轮未回收分段和物理原物均保留，二启/三启重新流式跳过前缀。
				if len(all) >= (restarts+1)*9 && restarts < 2 {
					require.NoError(t, imp.Close())
					led2 := ledger.New()
					require.NoError(t, led2.Restore(led.Snapshot()))
					led = led2
					imp = NewArchiveImporter(led, key)
					require.NoError(t, imp.SetReadLimits(ReadLimits{MaxLineBytes: 16, MaxBytesPerPoll: 49}))
					restarts++
				}
				if !result.Partial {
					require.NoError(t, imp.MarkImported(path, result.ArchiveObjectID, 0, 330))
					break
				}
			}
			require.NoError(t, imp.Close())
			require.Equal(t, 2, restarts)
			require.Len(t, all, 30)
			for i, event := range all {
				require.Equal(t, uint64(i*11), event.Record.Start)
				require.Equal(t, uint64((i+1)*11), event.Record.End)
			}
		})
	}
}

func TestSixFixFiniteArchiveLongLineKeepsResponsibility(t *testing.T) {
	for _, newline := range []string{"", "\n"} {
		path := filepath.Join(t.TempDir(), "archive.log")
		require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 100)+newline), 0600))
		key, led := testKey("six-archive-long", "g1"), ledger.New()
		imp := NewArchiveImporter(led, key)
		require.NoError(t, imp.SetReadLimits(ReadLimits{MaxLineBytes: 32, MaxBytesPerPoll: 96}))
		var limitErr *ReadLimitError
		for n := 0; n < 5; n++ {
			result, err := imp.ImportText(path)
			if err != nil {
				require.ErrorAs(t, err, &limitErr)
				require.Empty(t, result.Events)
				break
			}
		}
		require.NotNil(t, limitErr)
		require.Equal(t, 1, led.UnresolvedGapCount(key))
		require.False(t, archiveSegmentOf(led, key, path).Imported)
		require.Zero(t, archiveSegmentOf(led, key, path).EndPos)
		require.NoError(t, imp.SetReadLimits(ReadLimits{MaxLineBytes: 128, MaxBytesPerPoll: 256}))
		result, err := imp.ImportText(path)
		require.NoError(t, err)
		require.Len(t, result.Events, 1)
		require.Equal(t, strings.Repeat("x", 100), result.Events[0].Message)
		require.Equal(t, uint64(100+len(newline)), result.Events[0].Record.End)
		require.False(t, result.Partial)
		require.NoError(t, imp.Close())
	}
}

func TestSixFixRotationClosesPendingPhysicalLineOnce(t *testing.T) {
	live, rotated := filepath.Join(t.TempDir(), "latest.log"), ""
	rotated = live + ".1"
	require.NoError(t, os.WriteFile(live, []byte("x中"), 0600))
	key, led := testKey("six-rotation-half", "g1"), ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, live, wal, nil)
	tailer.SetRotateTarget(rotated)
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.Empty(t, events)
	require.NoError(t, os.Rename(live, rotated))
	require.NoError(t, os.WriteFile(live, []byte("next\n"), 0600))
	events, err = tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, "x中", events[0].Message)
	require.Equal(t, uint64(4), events[0].Record.End)
	require.Equal(t, uint64(4), events[1].Record.Start)
	require.Equal(t, 1, tailer.RotationCount())
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
	events, err = tailer.Poll()
	require.NoError(t, err)
	require.Empty(t, events)
}

func TestSixFixWALReservationsPersistUntilActualPruneAndRestore(t *testing.T) {
	key, led := testKey("six-reservation", "g1"), ledger.New()
	wal := NewWAL(led, key)
	var mu sync.Mutex
	var held int64
	var denied bool
	reserve := func(bytes int64) (func(), error) {
		mu.Lock()
		defer mu.Unlock()
		if denied || held+bytes > 1000 {
			return nil, errors.New("受控WAL总量预算拒绝")
		}
		held += bytes
		return func() { mu.Lock(); defer mu.Unlock(); held -= bytes }, nil
	}
	require.NoError(t, wal.SetByteReservation(reserve))
	pipe := NewPipeline(led, key, wal)
	// 2026-10-29 改写：旧夹具靠“成功投递后条目仍留在 WAL”观察预留，而新语义下成功投递
	// 会当场推进回收并剪掉已确认前缀 ⇒ 预留随之合法释放，那一步就不再是“仍在 WAL 里”。
	// 改用 AckLost（UNKNOWN）：条目**确实**必须留在队列里（不是回收证据），正是本用例
	// 要守的“请求返回不能释放仍在WAL里的字节”的形态。
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) { return 200, true, nil })
	require.NoError(t, pipe.Ingest([]logtypes.Event{sixFixEvent(key, 0, 7)}))
	one := held
	require.Positive(t, one, "请求返回不能释放仍在WAL里的字节")
	_, err := pipe.DeliverPending()
	require.NoError(t, err)
	require.Equal(t, one, held, "UNKNOWN 的条目仍在队列里 ⇒ 预留不得释放")
	require.Len(t, wal.Snapshot(), 1, "UNKNOWN 不得让条目离开队列")
	require.Error(t, wal.Append(sixFixEvent(key, 7, 14), sixFixEvent(key, 14, 21), sixFixEvent(key, 21, 28)))
	require.Len(t, wal.Snapshot(), 1, "整批预留中途失败要撤回本批全部租约且零追加")
	require.Equal(t, one, held)
	snapshot := wal.Snapshot()
	wal.pruneReclaimed(7)
	require.Zero(t, held)
	require.NoError(t, wal.seedEntries(snapshot))
	require.Equal(t, one, held, "恢复已有条目也必须重新计占用")
	denied = true
	require.Error(t, wal.seedEntries(snapshot))
	require.Error(t, wal.Commit())
	_, err = pipe.DeliverPending()
	require.Error(t, err)
	denied = false
	require.NoError(t, wal.SetByteReservation(reserve))
	require.Equal(t, one, held)
	wal.pruneReclaimed(7)
	require.Zero(t, held)
}
