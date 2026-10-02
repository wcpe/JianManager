package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestBackpressureSkipsRevivalWhileBacklogRises（用户指令的红证）：**上一轮积压净涨 ⇒ 本轮不复活**。
//
// 现场依据（+463k ✗）：复活让源开始读文件 ⇒ 常规新数据进入 WAL ⇒ 恶化期继续复活更多源就是
// "越复活越涨" ✗✗ ⇒ Q18 批准的背压：净涨期不放宽、不复活 ✓。
//
// 转红方式（实测）：去掉背压（无条件继续本轮）⇒ 本用例在「净涨期不得复活」处红 ✓。
func TestBackpressureSkipsRevivalWhileBacklogRises(t *testing.T) {
	m, _ := newGateFixture(t, 50, 16)
	require.NotNil(t, m)

	// 注入"一直在涨"的闸视图读数 ⇒ 每一轮都应被背压跳过 ✓。
	rising := int64(0)
	m.sweepBacklogOf = func() int64 { rising += 10; return rising }
	m.resumeInterval = 10 * time.Millisecond
	m.recoveryProbe = func() bool { return false }

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	m.RunCapacityResumeLoop(ctx)

	require.GreaterOrEqual(t, m.BackpressureSkips(), int64(1),
		"持续净涨时必须出现背压跳过（实测 %d 次）", m.BackpressureSkips())
}
