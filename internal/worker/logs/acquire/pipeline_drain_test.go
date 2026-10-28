package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 暂停期间仍必须**排空已 durable 的待投递条目**（2026-09-30 生产实证）。
//
// 缺了它：投递挂在摄取上 → 暂停即断流 → 已读未投的积压永不外发 → 投递位置不动 →
// 恢复分段无从覆盖 → 回收停滞 → 源永久停在 paused，新日志全部停摆。
// 实测：某源残留 7998 条卡死数小时，积压数字一个字节不动。
//
// 2026-10-29：积压闸已改为丢弃，不再产生暂停；本用例验的是「暂停期仍要排空存量」，
// 暂停从哪来无关紧要 —— 用容量门禁造成的暂停（本包不会再自己解除它）。
//
// 变异验证：删掉 APPEND_REJECTED 分支里的 `p.handleAppendRejected()`（未接调度者会落到
// `deliverPendingBestEffort`），本测试立即转红。
func TestPausedIngestStillDrainsDurablePending(t *testing.T) {
	key := testKey("src-drain", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(1, 0) // 条目上限 1：先验一次“越界只丢不暂停”
	pipe := NewPipeline(led, key, wal)

	deliveries := 0
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
		deliveries++
		return 204, false, nil
	})
	// 必须接一条持久化钩子：TrimBacklog 只在“已落库”之后才调（见 persistCommittedBatchContext），
	// 不接的话本包根本不会裁剪积压——那样测的就不是生产路径，下面“越界只丢不暂停”也无从成立。
	pipe.SetDurablePersist(func() error { return nil })

	require.NoError(t, pipe.Ingest(buildWALEvents("src-drain", "g1", 0, 2, "line")))
	require.Equal(t, 1, deliveries, "正常批次的投递仍应发生")
	// 丢弃语义下：2 条进、上限 1 ⇒ 落库后当场丢最旧 1 条，但**不**暂停采集。
	require.False(t, led.Get(key).AcquirePaused, "积压越界只丢不暂停（2026-10-29 裁定）")
	require.Len(t, wal.Snapshot(), 1, "越界后必须裁到上限内（2 条 → 1 条）")

	// 用容量门禁造成的暂停制造暂停态（tailer 在此期间不读，源自己不会再产生新批次）。
	//
	// 为什么不能用存量积压暂停串（2026-10-29 实测）：Ingest → AppendEvents 里的
	// wal.TryReclaim 会走到 pruneReclaimed(0) → maybeResumeBacklogLocked；低水位/滞回
	// 判据已随丢弃语义移除，只要缺口不阻塞它就会**当场**解除暂停，于是“暂停期间”
	// 这个前提蒸发了（Ingest 直接成功）。容量门禁的暂停不归本包管（maybeResumeBacklogLocked
	// 只认 legacy 积压前缀）⇒ 它是本包唯一能稳定造出持续暂停态的成因，也正是生产里另一种真实形态。
	require.NoError(t, led.PauseAcquire(key, "disk usage 95.0% >= 90.0%; pause irreversible writes"))
	require.True(t, led.Get(key).AcquirePaused, "夹具应处于暂停态")
	before := deliveries

	// 暂停期间的追加被拒 —— 但**存量必须被排空**（本用例守的就是这条）。
	//
	// 拒绝来自 WAL.appendLocked 的「已暂停则拒 append」（文案 `acquire: paused (...)`），
	// 并由 AppendEvents 作为 APPEND_REJECTED 缺口登记——绝不静默丢。
	err := pipe.Ingest(buildWALEvents("src-drain", "g1", 2, 1, "line"))
	require.Error(t, err, "暂停期间不得再追加")
	require.Contains(t, err.Error(), "paused")
	require.Greater(t, deliveries, before,
		"暂停期间仍须投递已 durable 的存量：否则积压永不外发、回收永不推进、源永久停在 paused")
}
