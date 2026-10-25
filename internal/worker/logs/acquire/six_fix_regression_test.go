package acquire

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

func sixFixEvent(key ledger.SourceKey, start, end uint64) logtypes.Event {
	return logtypes.BuildEvent(logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "acquire-v1"}, logtypes.RecordRange{Start: start, End: end}, "2026-10-22T00:00:00Z", "2026-10-22T00:00:00Z", "INFO", "stdout", "sample")
}

func TestSixFixMissingLiveYieldsAndRegistersRotationOnce(t *testing.T) {
	if os.Getenv("JM_ACQUIRE_SIX_MISSING_LIVE_CHILD") == "1" {
		dir := t.TempDir()
		live, rotated := filepath.Join(dir, "latest.log"), filepath.Join(dir, "latest.log.1")
		require.NoError(t, os.WriteFile(live, []byte("sample\n"), 0600))
		key, led := testKey("six-missing-live", "g1"), ledger.New()
		tailer := NewFileTailer(led, key, live, NewWAL(led, key), nil)
		tailer.SetRotateTarget(rotated)
		_, err := tailer.Poll()
		require.NoError(t, err)
		require.NoError(t, os.Rename(live, rotated))
		debug.SetMaxStack(128 << 10)
		for n := 0; n < 50; n++ {
			events, err := tailer.Poll()
			require.NoError(t, err)
			require.Empty(t, events)
		}
		require.Equal(t, 1, tailer.RotationCount())
		require.NoError(t, os.WriteFile(live, []byte("replacement\n"), 0600))
		events, err := tailer.Poll()
		require.NoError(t, err)
		require.Len(t, events, 1)
		require.Equal(t, uint64(7), events[0].Record.Start)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSixFixMissingLiveYieldsAndRegistersRotationOnce$", "-test.timeout=4s")
	cmd.Env = append(os.Environ(), "JM_ACQUIRE_SIX_MISSING_LIVE_CHILD=1")
	out, err := cmd.CombinedOutput()
	t.Logf("受控缺失窗口子进程：exit=%v，stackOverflow=%t", err, strings.Contains(string(out), "stack overflow"))
	require.NoError(t, err, "轮转中间态不得递归耗尽栈：%s", out)
}

func TestSixFixPersistFailuresBlockEveryDrainUntilSuccess(t *testing.T) {
	for _, persistErr := range []error{errors.New("受控提交失败"), context.DeadlineExceeded, errors.New("受控Incomplete或交办")} {
		t.Run(persistErr.Error(), func(t *testing.T) {
			key, led := testKey("six-persist", "g1"), ledger.New()
			wal := NewWAL(led, key)
			p := NewPipeline(led, key, wal)
			fails, persists, deliveries := true, 0, 0
			p.SetDurablePersist(func() error {
				persists++
				if fails {
					return persistErr
				}
				return nil
			})
			p.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) { deliveries++; return 200, false, nil })
			require.Error(t, p.Ingest([]logtypes.Event{sixFixEvent(key, 0, 7)}))
			for n := 0; n < 3; n++ {
				events, err := p.DeliverPending()
				require.Error(t, err)
				require.Empty(t, events)
				require.Zero(t, deliveries, "内存Durable不得单独授权外发")
			}
			require.NoError(t, led.PauseAcquire(key, "受控暂停"))
			require.Error(t, p.Ingest([]logtypes.Event{sixFixEvent(key, 7, 14)}))
			require.Zero(t, deliveries, "拒绝追加的兜底排空也必须过持久化门")
			require.Equal(t, uint64(7), led.Get(key).Positions.Durable)
			// 以失败后的候选快照重启，资格必须再次由成功提交取得，而非信任内存标记。
			led2 := ledger.New()
			require.NoError(t, led2.Restore(led.Snapshot()))
			wal2 := NewWAL(led2, key)
			require.NoError(t, wal2.Restore(wal.Snapshot()))
			p2 := NewPipeline(led2, key, wal2)
			p2.SetDurablePersist(func() error {
				persists++
				if fails {
					return persistErr
				}
				return nil
			})
			p2.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
				deliveries++
				require.Len(t, events, 1)
				require.Equal(t, uint64(7), events[0].Record.End)
				return 200, false, nil
			})
			_, err := p2.DeliverPending()
			require.Error(t, err)
			require.Zero(t, deliveries)
			fails = false
			events, err := p2.DeliverPending()
			require.NoError(t, err)
			require.Len(t, events, 1)
			require.Equal(t, 1, deliveries)
			require.GreaterOrEqual(t, persists, 6)
		})
	}
}

func TestSixFixFsyncFailureDoesNotQualifyAtHigherDurable(t *testing.T) {
	key, led := testKey("six-fsync", "g1"), ledger.New()
	wal := NewWAL(led, key)
	require.NoError(t, led.AdvanceDurable(key, 1000))
	require.NoError(t, wal.Append(sixFixEvent(key, 1, 10)))
	wal.SetFsync(func() error { return errors.New("受控fsync失败") })
	require.Error(t, wal.Commit())
	require.False(t, wal.Snapshot()[0].Durable)
	require.Equal(t, uint64(1000), led.Get(key).Positions.Durable)
}

func TestSixFixUnknownReusedSlotIsStableAndVisible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log.1")
	old := "old-content-before-shutdown\n"
	require.NoError(t, os.WriteFile(path, []byte(old), 0600))
	key, led := testKey("six-unknown-slot", "g1"), ledger.New()
	imp := NewArchiveImporter(led, key)
	first, err := imp.ImportText(path)
	require.NoError(t, err)
	require.NoError(t, imp.MarkImported(path, first.ArchiveObjectID, 0, uint64(len(old))))
	unknown := []byte("unseen-content-written-while-worker-was-stopped\n")
	require.NoError(t, os.WriteFile(path, unknown, 0600))
	for n := 0; n < 3; n++ {
		res, err := imp.ImportText(path)
		require.NoError(t, err)
		require.Empty(t, res.Events, "无锚不得盲目整包分配新身份")
		require.Equal(t, 1, led.UnresolvedGapCount(key), "未知内容必须稳定登记可见缺口")
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, unknown, got)
		led2 := ledger.New()
		require.NoError(t, led2.Restore(led.Snapshot()))
		led, imp = led2, NewArchiveImporter(led2, key)
	}
}

func TestSixFixTruncationMappingSurvivesThreeStarts(t *testing.T) {
	key := testKey("six-trunc-restart", "g1")
	tailer, lines := truncateFixture(t, key, true)
	prefix := strings.Join(lines[:5], "\n") + "\n"
	require.NoError(t, os.Truncate(tailer.path, int64(len(prefix))))
	rotated, err := tailer.PrepareRotation()
	require.NoError(t, err)
	require.False(t, rotated)
	_, err = tailer.Poll()
	require.NoError(t, err)
	originalEnd := tailer.LogicalPos()
	for round := 2; round <= 3; round++ {
		led := ledger.New()
		require.NoError(t, led.Restore(tailer.led.Snapshot()))
		wal := NewWAL(led, key)
		require.NoError(t, wal.Restore(tailer.wal.Snapshot()))
		tailer = NewFileTailer(led, key, tailer.path, wal, nil)
		events, err := tailer.Poll()
		require.NoError(t, err)
		require.Empty(t, events, "第%d次启动不得把durable存活前缀赋新身份", round)
		require.Equal(t, originalEnd, tailer.LogicalPos())
	}
	appendBytes(t, tailer.path, []byte("fresh\n"))
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, originalEnd, events[0].Record.Start)
}

func TestSixFixOldSegmentFingerprintDoesNotInspectNewFile(t *testing.T) {
	dir := t.TempDir()
	live, rotated := filepath.Join(dir, "latest.log"), filepath.Join(dir, "latest.log.1")
	old, current := strings.Repeat("old-old-old\n", 70), strings.Repeat("new-new-new\n", 140)
	require.NoError(t, os.WriteFile(live, []byte(old), 0600))
	key, led := testKey("six-fp-rotation", "g1"), ledger.New()
	wal := NewWAL(led, key)
	tailer := NewFileTailer(led, key, live, wal, nil)
	tailer.SetRotateTarget(rotated)
	events, err := tailer.Poll()
	require.NoError(t, err)
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
	require.NoError(t, led.RecordDelivery(key, 0, uint64(len(old)), logtypes.DeliveryRequestDone))
	_, err = tailer.Poll()
	require.NoError(t, err)
	require.NoError(t, os.Rename(live, rotated))
	require.NoError(t, os.WriteFile(live, []byte(current), 0600))
	changed, err := tailer.PrepareRotation()
	require.NoError(t, err)
	require.True(t, changed)
	events, err = tailer.Poll()
	require.NoError(t, err)
	require.Len(t, events, 140)
	require.NoError(t, wal.Append(events...))
	require.NoError(t, wal.Commit())
	for round := 2; round <= 3; round++ {
		led2 := ledger.New()
		require.NoError(t, led2.Restore(led.Snapshot()))
		wal2 := NewWAL(led2, key)
		require.NoError(t, wal2.Restore(wal.Snapshot()))
		tailer = NewFileTailer(led2, key, live, wal2, nil)
		fresh := "fresh-after-restart\n"
		appendBytes(t, live, []byte(fresh))
		events, err = tailer.Poll()
		require.NoError(t, err)
		require.Len(t, events, 1, "第%d次启动不能把旧段指纹拿来校验新文件", round)
		require.Zero(t, led2.UnresolvedGapCount(key))
		require.NoError(t, wal2.Append(events...))
		require.NoError(t, wal2.Commit())
		led, wal = led2, wal2
	}
}

func TestSixFixLiveUTF8AndCRLFWaitForPhysicalLine(t *testing.T) {
	for _, text := range []string{"x中\n", "x😀\r\n", "ASCII\n"} {
		for split := 1; split < len(text); split++ {
			t.Run(text+string(rune('a'+split)), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "latest.log")
				require.NoError(t, os.WriteFile(path, []byte(text[:split]), 0600))
				key, led := testKey("six-partial", "g1"), ledger.New()
				wal := NewWAL(led, key)
				tailer := NewFileTailer(led, key, path, wal, nil)
				first, err := tailer.Poll()
				require.NoError(t, err)
				require.Empty(t, first)
				require.Zero(t, tailer.LogicalPos())
				// 半行字节不成为持久游标；重启从半行首部重新拼接。
				led2 := ledger.New()
				require.NoError(t, led2.Restore(led.Snapshot()))
				wal2 := NewWAL(led2, key)
				tailer = NewFileTailer(led2, key, path, wal2, nil)
				appendBytes(t, path, []byte(text[split:]))
				events, err := tailer.Poll()
				require.NoError(t, err)
				require.Len(t, events, 1)
				require.Equal(t, strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r"), events[0].Message)
				require.Equal(t, uint64(len(text)), events[0].Record.End)
			})
		}
	}
}

func TestSixFixDefaultLineBudgetBackpressuresWithoutBodyAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "latest.log")
	require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 2<<20)+"\n"), 0600))
	key, led := testKey("six-line-bound", "g1"), ledger.New()
	tailer := NewFileTailer(led, key, path, NewWAL(led, key), nil)
	events, err := tailer.Poll()
	require.Error(t, err, "读取前的独立字节界必须阻止超长行")
	require.Empty(t, events, "不能把长行裸截断或拆为普通事件")
	require.Zero(t, tailer.LogicalPos())
	require.Zero(t, tailer.cursor)
	require.Positive(t, led.UnresolvedGapCount(key), "背压必须显式保留源责任")
}
