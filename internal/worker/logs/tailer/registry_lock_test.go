package tailer

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Flush 延迟观测（**不设硬断言**，只打读数供人工 review）。
//
// 原始诉求是「采集登记不被长周期锁拖死」。
//
// # 我试过的两种判据，都不可靠（写在这里免得后人重走）
//
//	① 数「Flush 期间完成多少次 Get」：正常实现 2184 次，
//	   但往锁内塞一次整表 JSON 序列化后仍有 1313 次——照样绿。
//	   调度空隙总能给读者钻进去，区分不了锁内/锁外。
//	② 测「单次锁等待占总耗时的比例」：同一份代码连跑两次得 80% 与 18%；
//	   注入锁内重活得 55%。三个数互相重叠，无法设阈值。
//
// 根因：一次 Flush 只有几百微秒，而 Go 调度抢占粒度与 GC 噪声就同量级。
// 想在微秒级用墙钟分辨「锁内干重活」，是在噪声里找信号。
//
// 所以这里只做观测。**结构上的保证靠代码审阅**：Flush 内只有一个
// 「拷快照 + 清脏标记」的临界区，序列化与写盘都在锁外。
// 下面两个弱断言（能推进 > 0）保留，它们能拦住「完全独占到底」那种极端回退。
func TestRegistry_FlushLatencyObservation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.jsonl")
	r, err := OpenRegistry(path)
	require.NoError(t, err)

	const n = 4000
	for i := 0; i < n; i++ {
		r.Put(FileState{
			Identity: FileIdentity{Dev: 1, Inode: uint64(i + 1), Fingerprint: "fp"},
			Path:     "/tmp/server/logs/latest.log",
			Offset:   int64(i),
			Size:     int64(i + 1),
		})
	}

	var maxWaitNs atomic.Int64
	var samples atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		id := FileIdentity{Dev: 1, Inode: 1, Fingerprint: "fp"}
		for {
			select {
			case <-stop:
				return
			default:
			}
			t0 := time.Now()
			r.Get(id)
			wait := time.Since(t0).Nanoseconds()
			for {
				cur := maxWaitNs.Load()
				if wait <= cur || maxWaitNs.CompareAndSwap(cur, wait) {
					break
				}
			}
			samples.Add(1)
		}
	}()

	time.Sleep(2 * time.Millisecond)

	flushStart := time.Now()
	require.NoError(t, r.Flush())
	flushCost := time.Since(flushStart)
	close(stop)
	wg.Wait()

	maxWait := time.Duration(maxWaitNs.Load())
	var ratio float64
	if flushCost > 0 {
		ratio = 100 * float64(maxWait) / float64(flushCost)
	}
	t.Logf("观测：Flush %v（%d 条）｜单次锁等待最长 %v｜占比 %.0f%%｜采样 %d 次",
		flushCost, n, maxWait, ratio, samples.Load())

	// 弱断言：探针确实跑起来了（否则上面的读数无意义）。
	require.Greater(t, samples.Load(), int64(0), "观测探针未能采样")
}

// Flush 期间 Put 也不应被困住（同一把锁）。
func TestRegistry_FlushDoesNotStarveWriters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.jsonl")
	r, err := OpenRegistry(path)
	require.NoError(t, err)

	for i := 0; i < 4000; i++ {
		r.Put(FileState{
			Identity: FileIdentity{Dev: 1, Inode: uint64(i + 1), Fingerprint: "fp"},
			Path:     "/tmp/server/logs/latest.log",
			Offset:   int64(i),
			Size:     int64(i + 1),
		})
	}

	var puts atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		var seq int64 = 100000
		for {
			select {
			case <-stop:
				return
			default:
				seq++
				r.Put(FileState{
					Identity: FileIdentity{Dev: 2, Inode: uint64(seq), Fingerprint: "fp2"},
					Offset:   seq,
					Size:     seq + 1,
				})
				puts.Add(1)
			}
		}
	}()

	time.Sleep(2 * time.Millisecond)
	putsAtStart := puts.Load()

	require.NoError(t, r.Flush())

	putsAfterFlush := puts.Load()
	close(stop)
	wg.Wait()

	delta := putsAfterFlush - putsAtStart
	t.Logf("Flush 期间完成 Put %d 次", delta)
	require.Greater(t, delta, int64(0),
		"Flush 期间写操作零推进 —— 新登记被整表落盘阻塞")
}

// 基本的持久化正确性：Flush 后重启能读回。
func TestRegistry_FlushRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.jsonl")
	r, err := OpenRegistry(path)
	require.NoError(t, err)

	want := FileState{
		Identity: FileIdentity{Dev: 7, Inode: 42, Fingerprint: "abcd"},
		Path:     "/srv/x/logs/latest.log",
		Offset:   1234,
		Size:     5678,
	}
	r.Put(want)
	require.NoError(t, r.Flush())

	_, err = os.Stat(path)
	require.NoError(t, err, "Flush 后应有位点文件")

	r2, err := OpenRegistry(path)
	require.NoError(t, err)
	got, ok := r2.Get(want.Identity)
	require.True(t, ok, "重启后应能读回位点")
	require.Equal(t, want.Offset, got.Offset)
	require.Equal(t, want.Path, got.Path)
}

// Flush 期间的 Put 不能丢：快照后的写入可以等下一轮，但不能消失。
func TestRegistry_PutsDuringFlushSurviveInMemory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.jsonl")
	r, err := OpenRegistry(path)
	require.NoError(t, err)

	r.Put(FileState{Identity: FileIdentity{Dev: 1, Inode: 1, Fingerprint: "a"},
		Offset: 10, Size: 10})
	require.NoError(t, r.Flush())

	// 模拟「快照已取、磁盘已写完之后又来的登记」。
	late := FileState{Identity: FileIdentity{Dev: 1, Inode: 2, Fingerprint: "b"},
		Offset: 20, Size: 20}
	r.Put(late)

	// 它必须还在内存里（下一轮 Flush 会带上它），不能因为上一轮清脏就丢。
	got, ok := r.Get(late.Identity)
	require.True(t, ok, "Flush 之后的新 Put 仍应可见")
	require.Equal(t, int64(20), got.Offset)

	// 再 Flush 一次，它应当落盘。
	require.NoError(t, r.Flush())
	r2, err := OpenRegistry(path)
	require.NoError(t, err)
	_, ok2 := r2.Get(late.Identity)
	require.True(t, ok2, "下一轮 Flush 应把晚到的 Put 写进文件")
}
