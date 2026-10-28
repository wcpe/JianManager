package ingest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件钉住「WAL 改纯内存后，重启靠重读而不靠恢复队列」这条可靠性模型。
//
// 它对应的业界做法是 Filebeat 的 registry 语义：内存队列**重启即丢**，不丢数据靠
// “持久化的 offset + 至少一次重读”。Elastic 官方文档（configuring-internal-queue）写得很直：
//
//	"The disk queue stores pending events on the disk rather than main memory...
//	 and to save events when a Beat or device is restarted."
//
// ⇒ 内存队列本身就不保证跨重启保存。我们选了内存队列（用户裁定不落盘），
// 就必须把「不丢」的责任交给重读。

func replayTestKey() ledger.SourceKey {
	return ledger.SourceKey{LogSourceID: "inst:replay/file", SourceGeneration: "g1"}
}

// seedPositions 用 `Ledger.Restore` 直接造出一个带给定水位的账本条目。
//
// 为什么用 Restore 而不是 Append/Commit：这里只需要「重启后从库里读出来的形态」，
// 不需要真的跑一遍采集。Restore 正是生产上的那条路径（从 position 表读出）。
func seedPositions(t *testing.T, led *ledger.Ledger, read, durable, reclaim uint64) {
	t.Helper()
	key := replayTestKey()
	entry := ledger.Entry{
		Key:      key,
		Identity: logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"},
		Positions: logtypes.Positions{
			Read: read, Durable: durable, Reclaim: reclaim,
		},
	}
	require.NoError(t, led.Restore([]ledger.Entry{entry}))
}

// TestRollbackReadToReclaimResumesFromReclaim 钉住核心语义：
// 重启后 read/durable **都**回退到 reclaim，tailer 才能从那里重读。
//
// 转红方式（实测）：把 `led.RollbackDurableToReclaim(key)` 那行删掉 ⇒
// durable 仍停在原值，tailer 取 min(durable, read) 时会被 durable 拉高，
// 本用例在 durable 断言处变红（重读范围会被白白跳过一段）。
func TestRollbackReadToReclaimResumesFromReclaim(t *testing.T) {
	led := ledger.New()
	seedPositions(t, led, 900, 900, 400)

	rollbackReadToReclaim(led, replayTestKey())

	pos, ok := led.Positions(replayTestKey())
	require.True(t, ok)
	require.EqualValues(t, 400, pos.Reclaim, "回收水位本身不得被改")
	require.EqualValues(t, 400, pos.Read, "读游标必须退到回收水位（重读起点）")
	require.EqualValues(t, 400, pos.Durable, "durable 也必须一起退，否则 tailer 会跳过重读区间")
}

// TestRollbackReadToReclaimIsIdempotent 钉住幂等：重启恢复可能被重复触发，
// 已回退过的源不应反复改变水位（也不应把 reclaim 当成新损失重复计数）。
func TestRollbackReadToReclaimIsIdempotent(t *testing.T) {
	led := ledger.New()
	seedPositions(t, led, 900, 900, 400)

	rollbackReadToReclaim(led, replayTestKey())
	first, _ := led.Positions(replayTestKey())
	rollbackReadToReclaim(led, replayTestKey())
	second, _ := led.Positions(replayTestKey())

	require.Equal(t, first, second, "重复调用不得再改变水位")
}

// TestRollbackReadToReclaimKeepsReclaimAsFloor 钉住下界：
// 已回收的字节**不得**被重读。
//
// 为什么这条很重要：重读已回收的字节不只是浪费——它会让 releaseRecovery 的
// from >= to 恒成立、静默不再建恢复段，最终把回收永久卡死（2026-09-30 生产实证）。
func TestRollbackReadToReclaimKeepsReclaimAsFloor(t *testing.T) {
	led := ledger.New()
	// read/durable 已经等于 reclaim（全部已回收）⇒ 不应回退到 0。
	seedPositions(t, led, 400, 400, 400)

	rollbackReadToReclaim(led, replayTestKey())

	pos, _ := led.Positions(replayTestKey())
	require.EqualValues(t, 400, pos.Read, "已回收字节不得重读（否则回收会被自己卡死）")
	require.EqualValues(t, 400, pos.Durable)
}

// TestRollbackReadToReclaimMissingSourceIsNoop 钉住边界：未登记的源不得 panic。
func TestRollbackReadToReclaimMissingSourceIsNoop(t *testing.T) {
	led := ledger.New()
	require.NotPanics(t, func() { rollbackReadToReclaim(led, replayTestKey()) })
}
