package ingest

// 关停有界宽限的红/绿并列证明（确定性 ✓，注释中文 ✓）：
//   绿：`Stop()` 所用的模式（priority + maxWait=stopGrace）在门被长占时**有界返回** ✓（＝降级退出 ✓）；
//   红（对照＝旧语义）：`persistPriority`（**无 maxWait** ✗）同场景**必等满门占用** ✗✓。
// 手法：把 stopGrace 临时改短（300ms ✓），避免测试等 45s；两种调用在同一测试内并列 ⇒ 无需 git ✓。

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStopPersistBoundedVsUnbounded(t *testing.T) {
	m, _ := newGateFixture(t, 64, 16)
	const H = 700 * time.Millisecond // 门被占时长：足够跨越 300ms 宽限 ✓

	orig := stopGrace
	stopGrace = 300 * time.Millisecond
	defer func() { stopGrace = orig }()

	// ── 绿：Stop() 所用模式（有界 ✓）
	holdGate(m, H)
	t0 := time.Now()
	err := m.persistCtx(context.Background(), persistMode{priority: true, maxWait: stopGrace})
	dGreen := time.Since(t0)
	require.NoError(t, err)
	require.Less(t, dGreen, stopGrace+100*time.Millisecond,
		"绿：有界宽限内应降级返回（实测 %v）", dGreen)
	require.GreaterOrEqual(t, dGreen, stopGrace-50*time.Millisecond,
		"绿：应确实等满宽限（否则测的不是这条路径）")
	t.Logf("绿 ✓ 有界返回 %v（grace=%v）", dGreen, stopGrace)

	// ── 红（对照＝旧语义）：无 maxWait ⇒ 必等满门占用 ✗
	holdGate(m, H)
	t1 := time.Now()
	err = m.persistCtx(context.Background(), persistPriority)
	dRed := time.Since(t1)
	require.NoError(t, err)
	require.GreaterOrEqual(t, dRed, H-100*time.Millisecond,
		"红：旧语义本应等满门占用（%v），实测 %v", H, dRed)
	t.Logf("红 ✓ 旧语义等满门占用 %v", dRed)
}
