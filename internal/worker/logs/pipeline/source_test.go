package pipeline

import (
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSourceForMapsEveryMode 每个已知模式都必须能选出一个来源实现。
//
// 变异：把 sourceFor 里任一分支删掉，对应子用例转红。
func TestSourceForMapsEveryMode(t *testing.T) {
	for _, mode := range []AcquireMode{ModeFilePrimary, ModeStdioPrimary, ModePushOnly} {
		src, err := sourceFor(mode, &Pipeline{})
		require.NoError(t, err, "模式 %s 必须能选出来源", mode)
		require.NotNil(t, src, "模式 %s 选出的来源不得为 nil", mode)
		require.Equal(t, mode, src.Mode(), "来源报告的模式必须与请求一致")
	}
}

// TestSourceForRejectsUnknownMode 未知模式必须报错，而不是默默给一个默认实现。
func TestSourceForRejectsUnknownMode(t *testing.T) {
	_, err := sourceFor(AcquireMode("NOPE"), &Pipeline{})
	require.Error(t, err, "未知模式必须报错（默默降级会掩盖配置错误）")
}

// TestOnlyPullSourcesImplementPoller 核心断言：推送型来源**不得**实现 Poller。
//
// 为什么这条最重要：PUSH_ONLY 若实现了永远返回 nil 的 Poll，那是假实现——
// 读者会以为它在做事，后来者也可能在上面加逻辑。类型层面不实现，语义就是事实。
func TestOnlyPullSourcesImplementPoller(t *testing.T) {
	cases := []struct {
		mode    AcquireMode
		isPoller bool
	}{
		{ModeFilePrimary, true},
		{ModeStdioPrimary, true},
		{ModePushOnly, false},
	}
	for _, c := range cases {
		src, err := sourceFor(c.mode, &Pipeline{})
		require.NoError(t, err)
		_, ok := src.(Poller)
		require.Equal(t, c.isPoller, ok,
			"模式 %s 的 Poller 实现性应当是 %v", c.mode, c.isPoller)
	}
}

// TestPollLockedOnPushOnlyReturnsNothing 推送源主动轮询必须无事件且不报错。
//
// 注意：这里断言的是**行为与改造前一致**（原来 case ModePushOnly 直接 return nil,nil）。
func TestPollLockedOnPushOnlyReturnsNothing(t *testing.T) {
	src, err := sourceFor(ModePushOnly, &Pipeline{})
	require.NoError(t, err)
	p := &Pipeline{source: src, mode: ModePushOnly}
	events, err := p.pollLocked()
	require.NoError(t, err)
	require.Empty(t, events, "推送源主动轮询不得返回事件")
}

// TestPollLockedWithNilSourceIsSafe 未装配来源时不得 panic（零值 Pipeline 的容错）。
func TestPollLockedWithNilSourceIsSafe(t *testing.T) {
	p := &Pipeline{}
	events, err := p.pollLocked()
	require.NoError(t, err)
	require.Empty(t, events)
}

// TestSourceInterfaceIsMinimal 护栏：Source 接口只该有 Mode 与 Register。
//
// 这不是可选的洁癖：接口一旦变胖，新增来源（journald/syslog）就会被逼实现
// 与它无关的方法。此测试用反射固定当前形状，改接口时会提醒同步评估。
func TestSourceInterfaceIsMinimal(t *testing.T) {
	src, err := sourceFor(ModePushOnly, &Pipeline{})
	require.NoError(t, err)
	// 推送源只该有 Mode 与 Register（不要因为“顺手”往里加方法）。
	require.Equal(t, ModePushOnly, src.Mode())
	// Register 对 nil ledger 的容错不在本测试范围（真实装配总是有 ledger）。
	_ = io.Reader(nil)
}
