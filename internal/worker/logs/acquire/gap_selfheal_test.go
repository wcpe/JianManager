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
//	投递失败 → 记缺口（有界）→ 源暂停（积压越界）→ **投递恢复** → 存量外发 →
//	缺口被消解（证据在 ingest 层，本测试用账本 API 等价表达）→ 采集自动恢复。
//
// 为什么必须覆盖「暂停期间的存量外发」：FileTailer 在 AcquirePaused 时直接拒绝读取，
// 「读 → 投递 → 回收 → 恢复评估」整条链没有任何触发点；缺了这一环，源会在暂停上停住，
// 直到人工解算（生产实证持续 13+ 小时零新数据）。
//
// 转红：改动前不存在 DeliverPending/EvaluateResume，且缺口只能靠人工解算；
// 本用例断言的「积压外发 → 缺口清零 → 自动恢复」三步中任一步不成立即红。
func TestPausedSourceSelfHealsAfterDeliveryRecovers(t *testing.T) {
	key := testKey("src-selfheal", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(4, 0) // 条目上限 4：便于用少量事件制造一次真实的积压暂停
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
	//
	// 2026-10-29：积压闸已改为丢弃，不再产生暂停；而本用例要验的是**暂停源的自愈**，
	// 暂停从哪来并不重要。因此改成“投递持续失败 + 由容量门禁设置暂停”——
	// 这正是生产里另一种真实成因（磁盘水位），且与积压无关，更不会因丢弃而消失。
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

	// 阶段三：缺口被消解（生产上由 ingest 层在「已确认落库」证据成立时执行，
	// 这里用同一账本 API 表达证据：覆盖区间 [0,499] 已确认落库）。
	resolved, err := led.ResolveGapsThroughExcept(key, 499,
		"delivery confirmed for this range", ledger.GapReasonStdioRawWriteFailed)
	require.NoError(t, err)
	require.Equal(t, 1, resolved)
	require.Zero(t, led.UnresolvedGapCount(key))

	// 阶段四：回收推进 + 自动恢复。责任转移后 CanReclaim 放行，回收前缀被剪掉，
	// 积压回落到低水位 → 滞回放行 → ResumeAcquire 成功（缺口已清零）。
	require.NoError(t, wal.BindRecoverySegment("seg-selfheal", "projection://g1", 0, 500))
	require.NoError(t, led.TransitionRecovery(key, "seg-selfheal", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "seg-selfheal", logtypes.RecoveryWALResponsibilityXfer, "", "projection:g1"))
	_, err = wal.TryReclaim()
	require.NoError(t, err)
	require.Empty(t, wal.Snapshot(), "回收前缀内的条目应被剪掉")
	entry = led.Get(key)
	require.False(t, entry.AcquirePaused, "缺口清零 + 积压回落后必须自动恢复采集")
	require.Empty(t, entry.PauseReason)
}

// EvaluateResume 只做「按滞回条件评估恢复」，不得越权清除其它路径设置的暂停。
//
// 转红：若实现改成无条件清除 AcquirePaused，本用例的越权反例即红。
func TestEvaluateResumeDoesNotClearForeignPause(t *testing.T) {
	key := testKey("src-foreign-pause", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(4, 0)
	pipe := NewPipeline(led, key, wal)

	// 2026-10-29：积压闸已改为丢弃，不再产生暂停。本用例验的是「评估不得越权清除
	// 其它路径的暂停」——这个不变量与暂停来源无关，前置改用积压类的**存量形态**即可。
	require.NoError(t, pipe.Ingest(buildWALEvents("src-foreign-pause", "g1", 0, 5, "line")))
	require.NoError(t, led.PauseAcquire(key, legacyWALBacklogPauseReason+" (entries=5/4)"))
	require.True(t, led.Get(key).AcquirePaused, "前置：处于积压类暂停")

	// 换成容量门禁造成的暂停：评估不得清除它（滞回 + 归属判定都不满足）。
	require.NoError(t, led.PauseAcquire(key, "disk usage 95.0% >= 90.0%; pause irreversible writes"))
	require.False(t, wal.EvaluateResume(), "不得清除其它路径设置的暂停")
	require.True(t, led.Get(key).AcquirePaused)

	// 恢复为自有暂停，且积压仍高于低水位：仍不得恢复（滞回）。
	led2 := ledger.New()
	wal2 := NewWAL(led2, key)
	wal2.SetLimits(4, 0)
	pipe2 := NewPipeline(led2, key, wal2)
	require.NoError(t, pipe2.Ingest(buildWALEvents("src-foreign-pause", "g1", 0, 5, "line")))
	require.True(t, led2.Get(key).AcquirePaused)
	require.False(t, wal2.EvaluateResume(), "积压高于低水位不得恢复")
}
