package acquire

import (
	"strings"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// buildWALEvents 构造 n 条位置相邻的事件（from 为起始序号，保证多次调用位置单调递增）。
func buildWALEvents(id, gen string, from, n int, payload string) []logtypes.Event {
	evs := make([]logtypes.Event, 0, n)
	for i := 0; i < n; i++ {
		idx := from + i
		evs = append(evs, logtypes.BuildEvent(
			logtypes.SourceIdentity{LogSourceID: id, SourceGeneration: gen, ParserVersion: "acquire-v1"},
			logtypes.RecordRange{Start: uint64(idx * 100), End: uint64(idx*100 + 99)},
			"t0", "t1", "INFO", "stdout", payload,
		))
	}
	return evs
}

// TestWALBacklogLimitDropsOldestWithoutPausing 钉住 2026-10-29 的新语义：
// **积压越界丢最旧、不暂停采集、不背压**。
//
// 历史（为何改）：旧语义是“越界暂停采集、不丢数据”。它付过的代价是
// 生产 19 个源里 10 个因积压闸停采 20+ 小时——被保住的只是“已经投不出去”
// 的旧条目，而新日志全部停摆。用户 2026-10-29 裁定改为丢弃。
//
// 本用例钉四件事（缺一不可）：
//  1. 未越界时不丢也不暂停；
//  2. 越界时**只丢到刚好回到上限以下**,不多丢一条；
//  3. 丢的是**最旧**的前缀（保留最新数据，它们才是有价值的）；
//  4. 源**保持可采**（不暂停），且后续追加照常成功。
func TestWALBacklogLimitDropsOldestWithoutPausing(t *testing.T) {
	key := testKey("src-backlog", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(4, 0) // 条目上限 4（字节上限走默认）
	pipe := NewPipeline(led, key, wal)
	// 真实采集路径在 Commit 之后一定有一次 durable 持久化，裁剪点就在那之后
	// （见 WAL.TrimBacklog）。测试必须复现这条路径，否则测的是“没人裁剪”。
	pipe.SetCommittedPersist(func(context.Context, []CommitIdentity) error { return nil })

	require.NoError(t, pipe.Ingest(buildWALEvents("src-backlog", "g1", 0, 3, "line")))
	require.False(t, led.Get(key).AcquirePaused, "未越界时不得暂停")
	require.Len(t, wal.Snapshot(), 3, "未越界时一条不得丢")

	// 追加到 5 条：越过上限 4 → 丢最旧 1 条，剩 4 条。
	require.NoError(t, pipe.Ingest(buildWALEvents("src-backlog", "g1", 3, 2, "line")))
	ent := led.Get(key)
	require.False(t, ent.AcquirePaused, "丢弃后不得暂停采集（不背压）")
	require.Empty(t, ent.PauseReason)

	snapshot := wal.Snapshot()
	require.Len(t, snapshot, 4, "只丢到刚好回到上限，不多丢")
	// 保留的必须是最新的 4 条（位置 [100,199] … [400,499]），最旧的 [0,99] 被丢。
	require.EqualValues(t, 100, snapshot[0].Event.Record.Start, "丢的必须是最旧前缀")
	require.EqualValues(t, 499, snapshot[len(snapshot)-1].Event.Record.End, "最新尾必须保留")

	// 丢弃不是静默行为：必须有可查的缺口凭据，且原因码专用。
	gaps := ent.Gaps
	dropped := 0
	for _, g := range gaps {
		if g.Reason == ledger.GapReasonWALBacklogDropped {
			dropped++
			require.EqualValues(t, 0, g.StartPos)
			// RecordRange 是**闭区间**（Start..End 均包含），所以被丢的第一条是 [0,99]。
			require.EqualValues(t, 99, g.EndPos, "区间必须是被丢条目的真实位置")
			require.False(t, g.Resolved)
		}
	}
	require.Equal(t, 1, dropped, "丢一段必须登记一条 WAL_BACKLOG_DROPPED 凭据")

	// 不背压：后续追加照常成功（但结果同样会落在上限内——超了就再丢一条，
	// 这正是“不暂停、只丢最旧”的稳态：采集永远在走，队列永远有界）。
	require.NoError(t, pipe.Ingest(buildWALEvents("src-backlog", "g1", 5, 1, "line")),
		"丢弃语义下不得再出现“暂停期间不得追加”")
	require.Len(t, wal.Snapshot(), 4, "队列恒在界内：新数据进、最旧出")
	require.False(t, led.Get(key).AcquirePaused, "连续越界也只能丢弃，不得沉淀成暂停")
	require.EqualValues(t, 599, wal.Snapshot()[3].Event.Record.End, "最新一条必须能进来")
}

// TestWALBacklogDroppedGapDoesNotBlockResume 钉住「丢弃类缺口不阻塞恢复」。
//
// 为什么必须单独钉（而不是顺手）：`ResumeAcquire` 是 **default-deny** ——
// 任何不在白名单里的未解决缺口一律拒绝恢复。若 `WAL_BACKLOG_DROPPED` 不被显式豁免，
// 则“丢弃 → 登记缺口 → 恢复被拒 → 再次暂停”会把刚拆掉的暂停从后门装回来，
// 与“不背压”直接矛盾。
func TestWALBacklogDroppedGapDoesNotBlockResume(t *testing.T) {
	key := testKey("src-backlog-resume", "g1")
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "acquire-v1"})

	require.NoError(t, led.RecordGap(key, 0, 100, ledger.GapReasonWALBacklogDropped, "capacity"))
	require.NoError(t, led.ResumeAcquire(key),
		"WAL_BACKLOG_DROPPED 必须不阻塞恢复（它救不回来，等也没用，且用户要求不暂停）")

	// 可见性不得因此丢失：缺口仍在账本里，只是不阻断。
	require.Equal(t, 1, led.UnresolvedGapCount(key), "不阻塞 ≠ 看不见")

	// 红线：其它未分类原因仍必须阻塞（default-deny 不得被顺手放宽）。
	require.NoError(t, led.RecordGap(key, 200, 300, "SOME_UNCLASSIFIED_REASON", "test"))
	require.Error(t, led.ResumeAcquire(key), "未分类原因必须仍然阻塞")
}

// TestZeroBacklogPausedSourceResumesWithoutAppend（2026-10-03 现场决定性证据）：
// **积压清零的源必须在没有任何新 Append 的情况下自动恢复采集**。
//
// 现场：27 个源 acquire_paused=1 且 source_wal entries=0（积压全清）仍不复活 ✗ ⇒ 恢复判定没有
// 绑定到"活着的积压读数"，唯一检查点挂在 Append 上：没人再 Append ⇒ 永远不再评估 ⇒ 成批停死 ✗✗。
//
// 2026-10-29 后本用例仍然有效且更重要：积压闸不再产生新暂停，但**存量数据库里还有旧二进制
// 写下的积压暂停**（生产 10 个源就是这种形态）。它们必须能被自愈路径认出来并解除，
// 否则这次改造对存量源等于没做。
func TestZeroBacklogPausedSourceResumesWithoutAppend(t *testing.T) {
	key := testKey("src-backlog-selfheal", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)
	wal.SetLimits(4, 0)
	pipe := NewPipeline(led, key, wal)

	// 造出**现场形态**（存量库）：积压为 0，但暂停原因还是旧二进制写的积压串。
	require.NoError(t, led.PauseAcquire(key, legacyWALBacklogPauseReason+" (entries=20213/20000, approx_bytes=8242606/536870912)"))
	require.True(t, led.Get(key).AcquirePaused)

	// 关键：**不做任何 Append** ✗，只由外部低频兜底入口重估一次 ⇒ 必须恢复。
	require.True(t, pipe.EvaluateResume(),
		"存量积压暂停必须能被自愈路径解除（否则旧库里的源永久停在暂停上）")
	ent := led.Get(key)
	require.False(t, ent.AcquirePaused, "不得停在暂停")
	require.Empty(t, ent.PauseReason)

	// 红线复核：非积压/容量类暂停绝不越权清除 ✓。
	require.NoError(t, led.PauseAcquire(key, "disk usage 95.0% >= 90.0%"))
	require.False(t, pipe.EvaluateResume(), "非本包原因不得被容量兜底清除")
	require.Equal(t, "disk usage 95.0% >= 90.0%", led.Get(key).PauseReason)
}

// legacyPrefixSanity 防止 legacyWALBacklogPauseReason 与 IsBacklogPauseReason 漂移：
// 两者一旦不同步，存量暂停就再也认不出来（表现为“源永久停采且无任何报错”）。
func TestLegacyBacklogPauseReasonRecognized(t *testing.T) {
	require.True(t, IsBacklogPauseReason(legacyWALBacklogPauseReason))
	require.True(t, IsBacklogPauseReason(legacyWALBacklogPauseReason+" (entries=1/2)"))
	require.False(t, IsBacklogPauseReason("disk usage 95.0% >= 90.0%"))
	require.False(t, IsBacklogPauseReason(""))
	require.True(t, strings.HasPrefix(legacyWALBacklogPauseReason, "wal backlog"),
		"存量库里的字面前缀不得变更，否则旧记录认不出来")
}
