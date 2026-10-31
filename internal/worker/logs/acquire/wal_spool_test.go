package acquire

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// fakeSpool 记录被写入的事件；delay 用于模拟磁盘慢。
type fakeSpool struct {
	count   atomic.Int64
	failAll atomic.Bool
	delay   time.Duration
}

func (f *fakeSpool) AppendEvent(key string, ev logtypes.Event) (uint64, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.failAll.Load() {
		return 0, errors.New("fake spool: 写入失败")
	}
	return uint64(f.count.Add(1)), nil
}

func (f *fakeSpool) calls() int64 { return f.count.Load() }

func newSpoolTestWAL(t *testing.T, spool SpoolSink) *WAL {
	t.Helper()
	led := ledger.New()
	key := ledger.SourceKey{LogSourceID: "test-src"}
	w := NewWAL(led, key)
	if spool != nil {
		w.SetSpoolSink(spool)
	}
	w.SetLimits(2, 1<<20)
	return w
}

func mkEvent(i int) logtypes.Event {
	return logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: "test-src", SourceGeneration: "g1", ParserVersion: "v1"},
		logtypes.RecordRange{Start: uint64(i * 10), End: uint64(i*10 + 9)},
		"2026-10-31T00:00:00Z", "2026-10-31T00:00:00Z", "info", "stdout",
		"msg-"+string(rune('a'+i%26)),
	)
}

func fillWAL(t *testing.T, w *WAL, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := w.Append(mkEvent(i)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
}

// TestSpoolReceivesDroppedEntries 越界裁切时，条目必须先交给磁盘缓冲。
//
// 触发链：pipeline 在持久化后调 TrimBacklog → enforceBacklogLimitLocked → 丢弃。
func TestSpoolReceivesDroppedEntries(t *testing.T) {
	sp := &fakeSpool{}
	w := newSpoolTestWAL(t, sp)

	fillWAL(t, w, 10)
	if n := len(w.entries); n != 10 {
		t.Fatalf("Append 阶段不应丢弃，实际 entries=%d", n)
	}
	w.TrimBacklog()

	if n := len(w.entries); n > 2 {
		t.Fatalf("裁切后 entries=%d，应当 ≤ 上限 2", n)
	}
	deadline := time.Now().Add(2 * time.Second)
	for sp.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sp.calls() == 0 {
		t.Fatalf("裁切时应当把条目交给磁盘缓冲，实际 count=0（stats=%+v）", w.SpoolStats())
	}
	t.Logf("磁盘缓冲收到 %d 条，WAL stats=%+v", sp.calls(), w.SpoolStats())
}

func TestSpoolNotMountedKeepsOldBehavior(t *testing.T) {
	w := newSpoolTestWAL(t, nil)
	fillWAL(t, w, 10)
	w.TrimBacklog()

	if got := w.SpoolStats(); got.Saved != 0 || got.Failed != 0 || got.Skipped != 0 {
		t.Fatalf("未挂载时统计应全为 0，得到 %+v", got)
	}
	if n := len(w.entries); n > 2 {
		t.Fatalf("裁切后条目数 = %d，应当 ≤ 上限 2", n)
	}
}

func TestSpoolFailureDoesNotBlock(t *testing.T) {
	sp := &fakeSpool{}
	sp.failAll.Store(true)
	w := newSpoolTestWAL(t, sp)

	done := make(chan struct{})
	go func() {
		defer close(done)
		fillWAL(t, w, 20)
		w.TrimBacklog()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("缓冲写失败时卡住了（不得阻塞采集）")
	}
	if n := len(w.entries); n > 2 {
		t.Fatalf("裁切后条目数 = %d，应当 ≤ 上限 2", n)
	}
	deadline := time.Now().Add(2 * time.Second)
	for w.SpoolStats().Failed == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	t.Logf("失败计数 = %+v", w.SpoolStats())
}

// TestSpoolDoesNotHoldWALMutexDuringDiskIO 锁边界：磁盘 IO 期间其它 goroutine 必须能拿到 w.mu。
//
// 这是本改造最关键的一条：持 w.mu 做磁盘 IO 会让整个源的采集停摆。
func TestSpoolDoesNotHoldWALMutexDuringDiskIO(t *testing.T) {
	sp := &fakeSpool{delay: 200 * time.Millisecond}
	w := newSpoolTestWAL(t, sp)

	fillWAL(t, w, 10)
	w.TrimBacklog()

	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	w.mu.Lock()
	elapsed := time.Since(start)
	w.mu.Unlock()

	if elapsed > 50*time.Millisecond {
		t.Fatalf("抢 w.mu 花了 %v：磁盘 IO 期间持锁了（硬约束：不得持 w.mu 做磁盘 IO）", elapsed)
	}
	t.Logf("磁盘 IO 期间抢 w.mu 只花了 %v，锁边界正确", elapsed)
}

func TestSpoolSkippedCountedWhenQueueFull(t *testing.T) {
	sp := &fakeSpool{delay: 50 * time.Millisecond}
	w := newSpoolTestWAL(t, sp)
	w.SetLimits(1, 1<<20)

	start := time.Now()
	fillWAL(t, w, 1000)
	w.TrimBacklog()
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("裁切 1000 条花了 %v：队列满时阻塞了（必须非阻塞丢弃）", elapsed)
	}
	s := w.SpoolStats()
	if s.Skipped == 0 {
		t.Fatalf("队列应当溢出并计入 Skipped，实际 %+v", s)
	}
	t.Logf("stats=%+v，总耗时 %v", s, elapsed)
}
