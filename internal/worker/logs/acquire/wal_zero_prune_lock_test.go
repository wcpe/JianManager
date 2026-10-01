package acquire

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// 复审 P2-1 回归：pruneReclaimed 的 **pos==0** 分支也必须持 w.mu。
//
// 现场（修复前）：该分支为了让「缺口消解后暂停源还能走出暂停」而补了一次恢复评估，
// 但直接调用了 maybeResumeBacklogLocked——而它的文档契约是「调用方需持 w.mu」。
// 于是这条路径会与 Append/Commit/Restore 并发读写 w.entries（积压统计要读切片头与各条目），
// 属于典型的数据竞争：轻则读到撕裂的积压数（滞回判定抖动），重则与追加并发时读到不一致的
// 切片状态。
//
// 验证方式：读侧持续走 0 水位路径，写侧持续改 w.entries（Restore 不做暂停检查、必然写入），
// 在 -race 下未持锁即报数据竞争；持锁后必须无竞争。
//
// 注意：本用例的判别力来自 Go 竞态检测器（门禁要求 -race 全绿），因此它只在 -race 下有意义。
func TestPruneReclaimedZeroPathUsesWALLock(t *testing.T) {
	key := testKey("src-zero-prune", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	// 积压上限 1 → 首批越界即暂停；暂停原因前缀为本包所写，0 水位分支才会真正进入
	// maybeResumeBacklogLocked → backlogLocked（读 w.entries）。
	wal.SetLimits(1, 0)
	pipe := NewPipeline(led, key, wal)
	require.NoError(t, pipe.Ingest(buildWALEvents("src-zero-prune", "g1", 0, 2, "line")))
	require.True(t, led.Get(key).AcquirePaused, "前置：应处于本包造成的积压暂停")

	restored := make([]WALEntry, 0, 3)
	for i, ev := range buildWALEvents("src-zero-prune", "g1", 0, 3, "line") {
		restored = append(restored, WALEntry{Seq: uint64(i + 1), Event: ev, Appended: true, Durable: true})
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for reader := 0; reader < 3; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				wal.pruneReclaimed(0) // 0 水位路径：必须与写侧互斥
			}
		}()
	}
	for i := 0; i < 2000; i++ {
		require.NoError(t, wal.Restore(restored)) // 写 w.entries（切片头）
	}
	close(stop)
	wg.Wait()

	// 语义不变：0 水位不剪任何条目，并且仍按滞回判定维持暂停（积压未回落）。
	require.True(t, led.Get(key).AcquirePaused, "0 水位路径不得凭剪枝恢复采集")
	require.NotEmpty(t, wal.Snapshot())
}
