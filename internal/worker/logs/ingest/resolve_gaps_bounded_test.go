package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是 C 修复（2026-10-04，resolve-gaps 挂死专项）的确定性红/绿：
//
// 现场（pprof 铁证）：pollOnce 持 cycleMu 卡在 `work <- i`（worker 全堵在 m.mu 的 33.8 万条
// WAL 序列化上）⇒ ResolveCoveredGapsForSource 无限等 cycleMu ⇒ 舰队 33/53 调用 45-60s TIMEOUT。
// 修法：**有界获取**（TryLock 轮询 ≤ resolveGapsCycleMuWait）——放弃裁定"忙即明确失败"安全（不写账本）。
//
// 绿（现状）：测试占住 cycleMu ⇒ 调用在预算内返回"采集轮忙"（且未写账本）；
// 红（变异：改回无限 cycleMu.Lock）⇒ 调用阻塞远超预算 ⇒ "超预算仍未返回"分支转红。
func TestResolveCoveredGapsForSourceBoundedOnCycleMu(t *testing.T) {
	m := &Manager{
		pipes:   map[string]*pipeline.Pipeline{},
		sources: map[string]SourceConfig{},
	}
	// 占住 cycleMu（模拟"采集轮长持有"）
	m.cycleMu.Lock()
	defer m.cycleMu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- m.ResolveCoveredGapsForSource(context.Background(), "inst:a/file", "tester")
	}()
	select {
	case err := <-done:
		require.Error(t, err, "cycleMu 被占时必须明确失败而非挂死")
		require.Contains(t, err.Error(), "采集轮忙", "错误必须可判读（忙/请稍后重试）")
	case <-time.After(3 * resolveGapsCycleMuWait):
		t.Fatal("超预算仍未返回 ⇒ 仍在无限等 cycleMu（红：现场 45-60s 挂死形态）")
	}
}

// 变异记录（2026-10-04 实测，逐字还原）：
//   - 变异：把有界获取改回 `m.cycleMu.Lock(); defer m.cycleMu.Unlock()`（无限等）⇒ 本用例
//     走 "超预算仍未返回" 分支 FAIL；还原后转绿。
