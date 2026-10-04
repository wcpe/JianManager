package ingest

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 本文件是 D 修复（2026-10-04，resolve-gaps 挂死专项）的确定性红/绿：
//
// 现场（pprof 铁证）：pollOnce 的 cycleMu 覆盖整轮 ⇒ worker 堵在 m.mu 的 33.8 万条 WAL
// 序列化时，"work <- i / wg.Wait()"把 cycleMu 长期占住 ⇒ 解算/登记/停止全挂（35×Resolve TIMEOUT）。
// 修法：cycleMu 只覆盖"读快照"段，进入分发段前必须已释放。
//
// 绿（现状）：进入分发段时 TryLock 成功（cycleMu 已放）；红（变异：把 defer 覆盖还原）⇒ TryLock 失败 ⇒ 转红。
func TestPollOnceReleasesCycleMuBeforeDispatch(t *testing.T) {
	m := &Manager{pipes: map[string]*pipeline.Pipeline{}, sources: map[string]SourceConfig{}}
	var heldByDispatcher atomic.Bool
	m.pollDispatchHook.Store(func() {
		// 分发段开始时 cycleMu 必须可被他人获取
		if !m.cycleMu.TryLock() {
			heldByDispatcher.Store(true)
			return
		}
		m.cycleMu.Unlock()
	})
	m.pollOnce()
	require.False(t, heldByDispatcher.Load(), "D：进入分发段时 cycleMu 必须已释放（否则解算/登记全挂）")
}
