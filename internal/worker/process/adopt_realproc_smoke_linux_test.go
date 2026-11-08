//go:build linux

package process

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFR497_RealWrapperAdoptSmoke 是夹具自检：确认真实 wrapper 子进程能被收养、PID 不变。
//
// 为什么先有这条「冒烟」：后面的心跳 / 巡检 / Stop 用例都建立在「这条真实进程对确实被收养了」之上。
// 若夹具本身造不出可收养的现场（例如 argv 形状不对、Java 没起来、socket 拨不通），那些用例会以
// 各种间接形态失败，很难判断是被测逻辑坏了还是夹具坏了。这条把夹具是否成立单独钉住。
func TestFR497_RealWrapperAdoptSmoke(t *testing.T) {
	h := newRealAdoptHarness(t, 0)
	inst := h.adopt(t)

	require.Equal(t, h.wrapper.PID, inst.strategy.GetPID(), "收养只重连，wrapper PID 必须不变")
	require.Equal(t, h.workDir, inst.WorkDir, "工作目录取自 PID 记录")
	require.True(t, inst.strategyStale, "重连策略只承接当前控制通道")
	require.True(t, inst.launchSpecPending, "仅凭 PID 记录收养时启动规格待补齐")
	require.Equal(t, 1, h.countAudit("orphan.auto_adopted"))
}
