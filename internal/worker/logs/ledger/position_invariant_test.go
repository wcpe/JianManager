package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestRestoreNormalizesReclaimBeyondRead（用户 2026-10-05 批准的红证 · restore 方向）：
// **落库态若 `reclaim > read`，恢复必须归一 + 告警**（不静默 ✗、不崩溃 ✗）。
//
// 现场依据：`inst:196` 的 `reclaim_pos` 166,480,682 **>** `read_pos` 99,776,039 ✗ —— 回收侧
// 此前**没有对称保护**（[ledger.go:1084](internal/worker/logs/ledger/ledger.go#L1084) `tryReclaimLocked`
// 的 `e.Positions.Reclaim = target` ✓），而读侧有（`AdvanceRead` 把 Read 夹到 Reclaim ✓）
// ⇒ "不变量只装了一半" ✓✓。
//
// 转红方式（实测）：去掉 `Restore` 里的归一 ⇒ 越界被原样带出 ⇒ 本用例在
// 「恢复后必须满足 reclaim ≤ read」与「检查器必须为空」两处红 ✓。
func TestRestoreNormalizesReclaimBeyondRead(t *testing.T) {
	key := SourceKey{LogSourceID: "inst:196/file", SourceGeneration: "g1"}
	l := New()
	l.Ensure(key, logtypes.SourceIdentity{
		LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1",
	})
	// 用既有条目快照改出"越界落库态"（**不依赖 Positions 的具体类型名** ✓：直接改快照字段 ✓）。
	snap := l.Get(key)
	snap.Positions.Read = 99_776_039
	snap.Positions.Durable = 99_776_039
	snap.Positions.Reclaim = 166_480_682 // 越界：回收越过读 ✗（现场原样）
	require.Greater(t, snap.Positions.Reclaim, snap.Positions.Read, "前置：构造出越界态 ✓")
	require.NoError(t, l.Restore([]Entry{*snap}))

	got := l.Get(key)
	require.LessOrEqual(t, got.Positions.Reclaim, got.Positions.Read,
		"恢复后必须满足常驻不变量 reclaim ≤ read（现场越界态不得被原样带出 ✗）")
	require.Equal(t, got.Positions.Read, got.Positions.Reclaim,
		"归一语义：夹到读（而不是保留越界值 ✓）")
	require.Empty(t, l.PositionInvariantViolations(),
		"常驻检查器必须为空（它是现场离线核这条不变量的读数面 ✓）")
}
