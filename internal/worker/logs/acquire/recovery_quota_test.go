package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// TestRecoveryQuotaWidensGateThenReturnsToRegular（2026-10-04 现场，用户定调方案②）：
// **恢复期独立配额**（默认 4×）+ **恢复完成后回归常规闸**。
//
// 现场依据：恢复期的回放/补账是设计行为（gz 归档补账写回原代次 backlog ✓），必然把源推过常规闸
// ⇒ 常规闸在恢复期注定自锁「越闸暂停 ⇒ 更排不空 ⇒ 更回放不了」✗；而恢复完成后若仍按拓宽判，
// 就是**削弱常规期闸语义** ✗（红线）。
//
// 转红方式（实测两条，各自独立红 ✓）：
//   - 变异①：去掉恢复期拓宽（按常规闸判）⇒ 恢复期那批越闸源被暂停 ⇒ 在「恢复期不得暂停」处红；
//   - 变异②：回归不生效（一直按拓宽判）⇒ 回归后越闸源不暂停 ⇒ 在「回归后必须按常规闸暂停」处红。
func TestRecoveryQuotaWidensGateThenReturnsToRegular(t *testing.T) {
	key := testKey("inst:recovery-quota", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetFsync(func() error { return nil })
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"})
	wal.SetLimits(4, 0) // 常规闸：4 条

	appendN := func(n int) {
		events := buildWALEvents("inst:recovery-quota", "g1", n*100, n, "line")
		require.NoError(t, wal.Append(events...))
		require.NoError(t, wal.Commit())
	}

	// 恢复期：按独立配额（4× ⇒ 16 条）判 ⇒ 8 条（> 常规 4、< 拓宽 16）不得暂停 ✓。
	wal.SetRecoveryWiden(4)
	appendN(8)
	require.False(t, led.Get(key).AcquirePaused,
		"恢复期回放/补账不得把源推成暂停（独立配额 4× 之内 ✓）")

	// 回归常规闸：同样 8 条（> 常规 4）⇒ **必须按常规语义暂停** ✓（红线：常规期语义不削弱 ✗）。
	wal.SetRecoveryWiden(1)
	appendN(1) // 再追加 1 条触发一次越闸判定（阈值判定发生在追加/提交路径 ✓）
	require.True(t, led.Get(key).AcquirePaused,
		"恢复完成回归常规闸后，超出常规闸者必须照常暂停（红线 ✓）")
	require.True(t, IsBacklogPauseReason(led.Get(key).PauseReason), "原因须是积压类（便于兜底自愈 ✓）")

	// 有界性红线：拓宽只是**更大的界**，仍然有限 ✓（不得变成"无界"✗）。
	wal.SetRecoveryWiden(4)
	require.Equal(t, int64(16), mustLimits(t, wal).entries, "独立配额必须是有界值（4×4=16 ✓）")
}

// mustLimits 取当前生效的上限（测试内省用）。
func mustLimits(t *testing.T, wal *WAL) struct{ entries, bytes int64 } {
	t.Helper()
	wal.mu.Lock()
	defer wal.mu.Unlock()
	e, b := wal.limitsLocked()
	return struct{ entries, bytes int64 }{e, b}
}
