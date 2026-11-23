package database

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureEvictSlog 把默认 slog 重定向到内存缓冲，仅保留 Error 级及以上，
// 返回缓冲与恢复函数（频控行为只能靠「Error 级打印次数」观测）。
func captureEvictSlog() (*bytes.Buffer, func()) {
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError})))
	return buf, func() { slog.SetDefault(prev) }
}

// TestEvictLogRateLimited 守护 CP-9：驱逐计数逐次累加，但 Error 级告警按窗口频控。
//
// 修复前毒化连接被 database/sql 反复 putConn/重试时，每次回池都打一条 unconditional
// slog.Error ⇒ 压力下刷屏。计数语义不变（累计真源），告警首条必打、窗口内合并、超窗再打。
func TestEvictLogRateLimited(t *testing.T) {
	buf, restore := captureEvictSlog()
	defer restore()

	g := &sqliteTxGuard{}
	// 首条必打。
	require.True(t, g.logEvictionRateLimited("first", "stage", stagePoolPut), "首次应打印")
	// 窗口内连续多次：只允许极少条打印（边界 CAS 竞态最多多打一条，不能逐次打印）。
	cut := 0
	for i := 0; i < 50; i++ {
		if g.logEvictionRateLimited("burst", "stage", stagePoolPut) {
			cut++
		}
	}
	require.LessOrEqual(t, cut, 1, "频控窗口内 50 次只允许至多 1 条 Error（窗口边界的 CAS 竞态）")

	n := strings.Count(buf.String(), "\n")
	require.LessOrEqual(t, n, 2, "Error 级日志不应逐次刷屏，实际 %d 条", n)
}

// TestGuardConnEvictedCountingUnchanged 守护 CP-9 的另一半：频控只影响打印，
// evictedConns 计数仍逐次累加（它是累计真源，必须精确）。
func TestGuardConnEvictedCountingUnchanged(t *testing.T) {
	buf, restore := captureEvictSlog()
	defer restore()

	g := &sqliteTxGuard{}
	c := &guardConn{guard: g}
	c.txOpen.Store(true)

	// 连续 5 次毒化回池（模拟反复 putConn/重试）。
	for i := 0; i < 5; i++ {
		require.False(t, c.IsValid())
	}
	require.Equal(t, int64(5), g.evictedConns.Load(), "计数必须逐次累加，不得因频控丢失")
	require.Zero(t, g.dirtyRollbacks.Load(), "回池路径不应推进 dirty 回滚计数（那是 ResetSession 的职责）")

	// ResetSession 入口同样计数。
	require.Error(t, c.ResetSession(context.Background()))
	require.Equal(t, int64(6), g.evictedConns.Load())

	// 计数全量保留的前提下，Error 级日志仍被频控住。
	require.LessOrEqual(t, strings.Count(buf.String(), "\n"), 2, "计数保留与频控必须同时成立")
}
