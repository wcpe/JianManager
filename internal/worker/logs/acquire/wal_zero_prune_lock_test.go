package acquire

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
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
	// 2026-10-29：积压闸已改为丢弃，不再产生新的积压暂停。但本用例考的是**锁安全性**
	// （0 水位分支必须持 w.mu 才能读 w.entries），与暂停从哪来无关 ——
	// 因此直接构造**存量形态**：暂停原因用旧二进制写的积压串。
	wal.SetLimits(1, 0)
	_ = NewPipeline(led, key, wal)
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "acquire-v1"})
	require.NoError(t, led.PauseAcquire(key, legacyWALBacklogPauseReason+" (entries=2/1)"))
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

	// 语义：0 水位不剪任何条目（与停机无关）。
	//
	// 2026-10-29：原来还断言“仍维持暂停（积压未回落）”—— 那依赖旧滞回语义，已不成立。
	// 现在 maybeResumeBacklogLocked 对**存量积压暂停**是直接解除（不再看积压高低），
	// 因为采集已不再因积压而停，留着暂停只会白白停采。
	// 本用例真正要钉的是**并发安全**（-race 下不得报竞态）与“0 水位不剪条目”，两条都保留。
	require.NotEmpty(t, wal.Snapshot(), "0 水位路径不剪任何条目")
}
