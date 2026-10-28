package acquire

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 缺陷 A 回归（缺口风暴把采集焊死）的自愈链：
//
//	投递失败 → 记缺口（有界）→ 源暂停 → **投递恢复** → 存量外发 →
//	缺口被消解（按「恢复后回读重放」分类，见 maybeResumeBacklogLocked）→ 采集自动恢复。
//
// 为什么必须覆盖「暂停期间的存量外发」：FileTailer 在 AcquirePaused 时直接拒绝读取，
// 「读 → 投递 → 回收 → 恢复评估」整条链没有任何触发点；缺了这一环，源会在暂停上停住，
// 直到人工解算（生产实证持续 13+ 小时零新数据）。
//
// 暂停成因（2026-10-29 起）：积压闸已改为「丢弃最旧」，本包**不再产生**积压暂停；
// 而本用例要守的是「已暂停的源靠投递恢复自愈」这条链，暂停从哪来无关紧要——
// 这里用旧二进制留下的**存量积压暂停**形态（legacyWALBacklogPauseReason），
// 它正是 maybeResumeBacklogLocked 唯一认的那一类，因而是本自愈链当前的真实入口。
//
// 转红：改动前不存在 DeliverPending/EvaluateResume，且缺口只能靠人工解算；
// 本用例断言的「存量外发 → 缺口不再阻塞 → 自动恢复」三步中任一步不成立即红。
func TestPausedSourceSelfHealsAfterDeliveryRecovers(t *testing.T) {
	key := testKey("src-selfheal", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(4, 0) // 条目上限 4（与下面存量暂停串里的 entries=5/4 对齐）
	pipe := NewPipeline(led, key, wal)

	deliveryErr := errors.New("victoria logs unavailable")
	deliveries := 0
	pipe.SetDeliver(func(events []logtypes.Event, replay bool) (int, bool, error) {
		deliveries++
		if deliveryErr != nil {
			return 0, false, deliveryErr
		}
		return 200, false, nil
	})

	// 阶段一：投递持续失败。每次失败的批次区间相邻 → 缺口被合并成一条（有界）。
	for i := 0; i < 5; i++ {
		require.Error(t, pipe.Ingest(buildWALEvents("src-selfheal", "g1", i, 1, "line")))
	}
	// 用**积压类存量形态**做前置：本包现在不再产生它，但现场库里仍有（旧二进制写的）。
	// 而 maybeResumeBacklogLocked 只认这一类暂停 —— 换成容量门禁的暂停，
	// 它就不会去解除（那是对的，越权反例见 TestEvaluateResumeDoesNotClearForeignPause）。
	require.NoError(t, led.PauseAcquire(key, legacyWALBacklogPauseReason+" (entries=5/4)"))
	entry := led.Get(key)
	require.True(t, entry.AcquirePaused, "前置：应处于积压类暂停")
	require.Len(t, entry.Gaps, 1, "连续同因失败必须合并为一条覆盖区间")
	require.Equal(t, uint64(0), entry.Gaps[0].StartPos)
	require.Equal(t, uint64(499), entry.Gaps[0].EndPos)
	require.Equal(t, 1, led.UnresolvedGapCount(key))
	require.Len(t, wal.Snapshot(), 5, "失败不得丢条目")

	// 阶段二：投递恢复。暂停中的源自己不会再有新批次（tailer 不读），
	// 存量必须由 DeliverPending 推出去——否则积压永不回落、缺口永无消解来源。
	deliveryErr = nil
	before := deliveries
	delivered, err := pipe.DeliverPending()
	require.NoError(t, err)
	require.NotEmpty(t, delivered, "恢复投递后必须把已 durable 存量外发")
	require.Greater(t, deliveries, before, "存量外发必须真正经过投递路径")
	// 存量投递同样登记请求结果；此时还没有回收证据，条目仍应保留（不得静默丢）。
	require.Len(t, wal.Snapshot(), 5)

	// 阶段三：缺口被消解——**由自愈链自己完成**，不需要外部证据。
	//
	// 流程（2026-10-29 复核）：DeliverPending 成功 → TryReclaim →（无可回收条目）
	// maybeResumeBacklogLocked → ResumeAcquire 被未消解缺口拒绝 →
	// resolveReReadHealableGapsLocked 按「恢复后回读重放」**分类裁定**消解可愈合类。
	// 这不是错误地“把没落库当成已落库”：消解词是待重读类，回读会重新产生并投递这段。
	//
	// 原阶段三用手动 ResolveGapsThroughExcept 表达“已确认落库”证据——路径变了，
	// 现在到达这里时已没有任何未消解缺口可消解（实测 resolved=0），所以那一步删掉，
	// 改为钉住自愈链自己的分类裁定（更强：顺带验了消解词的归属）。
	entry = led.Get(key)
	require.Zero(t, led.UnresolvedGapCount(key), "自愈链必须把暂停期缺口消解到不阻塞")
	require.Len(t, entry.Gaps, 1)
	require.True(t, entry.Gaps[0].Resolved)
	require.True(t, IsReReadHealableResolution(entry.Gaps[0].Resolution),
		"分类裁定必须记为待重读类（回读会重投），不得写成“已确认落库”: %q", entry.Gaps[0].Resolution)
	// 存量已外发 + 缺口不再阻塞 ⇒ 本次投递就已经把源拉回来了。
	require.False(t, entry.AcquirePaused, "存量外发 + 缺口分类消解后必须自动恢复采集")
	require.Empty(t, entry.PauseReason)

	// 阶段四：回收推进 + 自动恢复。责任转移后 CanReclaim 放行，回收前缀被剪掉。
	require.NoError(t, wal.BindRecoverySegment("seg-selfheal", "projection://g1", 0, 500))
	require.NoError(t, led.TransitionRecovery(key, "seg-selfheal", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "seg-selfheal", logtypes.RecoveryWALResponsibilityXfer, "", "projection:g1"))
	_, err = wal.TryReclaim()
	require.NoError(t, err)
	require.Empty(t, wal.Snapshot(), "回收前缀内的条目应被剪掉")
	entry = led.Get(key)
	require.False(t, entry.AcquirePaused, "缺口不再阻塞 + 回收完成后仍必须保持可采（不得重新暂停）")
	require.Empty(t, entry.PauseReason)
}

// EvaluateResume 只做「推进回收 + 读暂停态」，不得越权清除其它路径设置的暂停。
//
// 转红：若实现改成无条件清除 AcquirePaused，本用例的越权反例即红。
// 第二个反例（自有的存量积压暂停 + 积压未回落）已随丢弃语义失效，原因见用例内注释。
func TestEvaluateResumeDoesNotClearForeignPause(t *testing.T) {
	key := testKey("src-foreign-pause", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(4, 0)
	pipe := NewPipeline(led, key, wal)

	// 2026-10-29：积压闸已改为丢弃，不再产生暂停。本用例验的是「评估不得越权清除
	// 其它路径的暂停」——这个不变量与暂停来源无关，前置改用积压类的**存量形态**即可
	//（存量形态也是 maybeResumeBacklogLocked 唯一会尝试解除的那一类，越权风险最大）。
	require.NoError(t, pipe.Ingest(buildWALEvents("src-foreign-pause", "g1", 0, 5, "line")))
	require.NoError(t, led.PauseAcquire(key, legacyWALBacklogPauseReason+" (entries=5/4)"))
	require.True(t, led.Get(key).AcquirePaused, "前置：处于积压类暂停")

	// 换成容量门禁造成的暂停：评估不得清除它（归属判定不满足）。
	require.NoError(t, led.PauseAcquire(key, "disk usage 95.0% >= 90.0%; pause irreversible writes"))
	require.False(t, wal.EvaluateResume(), "不得清除其它路径设置的暂停")
	require.True(t, led.Get(key).AcquirePaused)
	require.Contains(t, led.Get(key).PauseReason, "disk usage", "不得改写其它路径的暂停原因")

	// 反例二（已删）：原断言「自有的积压暂停 + 积压高于低水位 ⇒ 不得恢复（滞回）」。
	//
	// 该断言的前提「积压高于一半上限」在今天已不可能由 `Append` 造出来了：
	// 越界不再暂停而是丢弃（enforceBacklogLimitLocked），所以前置里 `Ingest` 之后源
	// 根本不在暂停态（原先那句 `require.True(AcquirePaused)` 必红）。而“滞回”本身也随
	// 低水位判据一起被移除（见 maybeResumeBacklogLocked），断言失去了被测物。
	// 该不变量尚存活的部分（不得越权清除**别家**的暂停）已由上面的容量门禁反例覆盖。
}
