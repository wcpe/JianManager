package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 2026-10-29 语义迁移（**关切方向翻转**）：新语义下「账本拒绝推进」只剩一种形态
// ——`ErrReclaimPrecondition`（调用方逐条 ACK 复核未通过）。而这条拒绝的含义是
// 「这段字节没有本次进程的完整正文 ACK」，剪掉它就是丢数据。所以 qualified 路径下
// 遇到 Precondition 拒绝时**必须不剪枝**（见 TryReclaimThrough 的 `else if err !=
// ledger.ErrReclaimPrecondition`）。
//
// 旧用例验的是反方向（“账本拒绝推进时仍须按当前水位剪枝”）。那个形态的两个前提
// 在新语义下都没了：
//   - 非 qualified 路径下 `led.TryReclaim` 只剩「源未注册」一种错误（不是“拒绝推进”）；
//   - 真正会拒绝的 Precondition 路径被设计为**不剪枝**（丢数据方向）。
//
// 旧关切「不剪枝导致积压无界增长」在新语义下由另一条链承担：认证覆盖推不动时，
// 积压由**丢弃语义**（enforceBacklogLimitLocked，见 wal_backlog_limit_test.go）定界，
// 不再依赖「拒绝时也剪一刀」。
//
// 所以本用例保留下来但翻转为**数据安全方向**：ACK 复核不通过时，一条都不许剪。
//
// 转红方式（实测）：把 `else if err != ledger.ErrReclaimPrecondition` 改成 `else`
// （即 Precondition 拒绝也剪枝）⇒ 本用例立即红。
func TestTryReclaim_DoesNotPruneWhenACKReviewRejects(t *testing.T) {
	key := testKey("src-prune", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)

	// 一条 durable 事件，Record.End = 99（落在一段 0..100 的覆盖之内）。
	evs := buildWALEvents("src-prune", "g1", 0, 1, "line")
	require.NoError(t, wal.Append(evs...))
	require.NoError(t, wal.Commit())
	require.Len(t, wal.Snapshot(), 1)

	// 铺好账本：认证覆盖 + 读入量都在 100 ⇒ 账本本身愿意推进到 100。
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "src-prune", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceRead(key, 100))
	require.NoError(t, led.AdvanceDurable(key, 100))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))

	// 开启完整正文 ACK 闸：此后回收要求逐条条目都有本进程的 acknowledged stamp。
	// 本条从未经投递确认 ⇒ condition 拒绝 ⇒ ErrReclaimPrecondition。
	wal.EnableQualifiedReclaim()
	_, err := wal.TryReclaim()
	require.ErrorIs(t, err, ledger.ErrReclaimPrecondition,
		"夹具应处于「调用方逐条 ACK 复核未通过」的状态")
	require.Equal(t, uint64(0), func() uint64 { p, _ := led.Positions(key); return p.Reclaim }(),
		"前置：被拒时回收位置不得推进")
	require.Len(t, wal.Snapshot(), 1,
		"ACK 复核不通过时一条都不许剪：那段字节没有本次进程的完整正文确认，剪了就是丢数据")
}

// TestWALRestoreFiresBacklogGate（D4，2026-10-03 现场）：**恢复**持久积压之后必须当场按闸判定
// —— 索引里的持久积压可能早已越界，此时若只在 Append 里判，处理会滞后到「下一次追加」才发生
// （现场形态：内存积压早已越界，闸却迟迟不生效，实测到 113.9 万条才暂停）。
//
// 2026-10-29：处理动作从“暂停”改为“丢弃最旧”，但**当场性**这条要求一字未变，
// 且在丢弃语义下更重要：不裁的话进程一启动就背着超出内存上界的积压。
func TestWALRestoreFiresBacklogGate(t *testing.T) {
	key := ledger.SourceKey{LogSourceID: "restore-gate", SourceGeneration: "g1"}
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"})
	wal := NewWAL(led, key)
	wal.SetLimits(3, 0)

	entries := make([]WALEntry, 0, 5)
	for i := 0; i < 5; i++ {
		end := uint64(10 + i*10)
		entries = append(entries, WALEntry{
			Seq: uint64(i + 1),
			Event: logtypes.BuildEvent(
				logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"},
				logtypes.RecordRange{Start: end - 9, End: end},
				"2026-09-22T01:00:00Z", "2026-09-22T01:00:01Z", "INFO", "stdout", "restored"),
			Appended: true, Durable: true,
		})
	}
	if err := wal.seedEntries(entries); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	// 恢复后必须当场裁到上限内（上限 3，恢复 5 条 ⇒ 丢最旧 2 条）。
	if n := len(wal.Snapshot()); n != 3 {
		t.Fatalf("恢复越界积压后必须立即裁剪（条目闸 %d 条，实测积压 %d）", 3, n)
	}
	// 必须丢最旧、保留最新。恢复的区间是 [1,10) [11,20) [21,30) [31,40) [41,50)，
	// 上限 3 ⇒ 只丢最旧的 2 条 ⇒ 剩下应从 21 开始（21/31/41）。
	//
	// 2026-10-29 改正：这里原先写的是 31（按“丢 3 条”算的），但 5−3=2 不是 3；
	// 丢弃只捡到“刚好回限内”为止（见 enforceBacklogLimitLocked 第二轮循环）。
	snap := wal.Snapshot()
	wantStarts := []uint64{21, 31, 41}
	for i, want := range wantStarts {
		if got := snap[i].Event.Record.Start; got != want {
			t.Fatalf("必须丢最旧前缀：剩下第 %d 条应从 %d 开始，实测 %d", i, want, got)
		}
	}
	// 丢的必须是**连续最旧前缀**，且区间写进凭据的是真实位置：首条 start=1、末条 end=20。
	// 同时不得因此暂停采集。
	entry := led.Get(key)
	if entry != nil && entry.AcquirePaused {
		t.Fatalf("丢弃语义下恢复后不得暂停采集: %q", entry.PauseReason)
	}
	// 丢弃必须留凭据，且区间/原因逐字可查（non-blocking 但仍进账本、仍可见）。
	sawDrop := false
	if entry != nil {
		for _, g := range entry.Gaps {
			if g.Reason != ledger.GapReasonWALBacklogDropped {
				continue
			}
			sawDrop = true
			if g.StartPos != 1 || g.EndPos != 20 {
				t.Fatalf("丢弃凭据的区间就是被丢条目的真实位置 [1,20)，实测 [%d,%d)", g.StartPos, g.EndPos)
			}
			if g.Resolved {
				t.Fatal("丢弃是终局事实，不得被自动消解")
			}
		}
	}
	if !sawDrop {
		t.Fatal("恢复期裁剪必须登记 WAL_BACKLOG_DROPPED 凭据")
	}
	if got := led.UnresolvedGapCount(key); got != 1 {
		t.Fatalf("丢弃缺口应仍可见（不阻塞但计入未解决）: 实测 %d", got)
	}
}

// TestWALBytesLimitZeroMeansUnlimited（2026-10-03 现场 16 MiB 之谜）：**负值哨兵 = 字节不限**。
//
// 现场形态：配置 `log_capacity.max_wal_bytes=0` 的语义是"字节维度不限"，而 acquire 在 0 时回退
// 硬编码 16 MiB ⇒ "不设上限"被静默实现成 16 MiB，一批源停在 32.6 MiB（条目/字节双闸同时起作用），
// 另一批停在 512 MiB（显式配了值）✗✗。
//
// 转红方式（实测）：把 limitsLocked 里 `bytes < 0 ⇒ MaxInt64` 的哨兵去掉（退回"0 ⇒ 默认 16 MiB"）
// —— 本用例在「负值 = 不限」处变红。
func TestWALBytesLimitZeroMeansUnlimited(t *testing.T) {
	key := ledger.SourceKey{LogSourceID: "unlimited", SourceGeneration: "g1"}
	led := ledger.New()
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"})
	wal := NewWAL(led, key)
	wal.SetLimits(1_000_000, -1) // -1 = 字节不限（由 config 的 0 语义映射而来）

	// 造一条"超大"事件（32 MiB，超过默认 16 MiB）：不限时必须**不暂停**。
	big := make([]byte, 32<<20)
	wal.SetFsync(func() error { return nil })
	if err := wal.Append(logtypes.BuildEvent(
		logtypes.SourceIdentity{LogSourceID: key.LogSourceID, SourceGeneration: key.SourceGeneration, ParserVersion: "v1"},
		logtypes.RecordRange{Start: 1, End: 2},
		"2026-09-22T01:00:00Z", "2026-09-22T01:00:01Z", "INFO", "stdout", string(big))); err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	entry := led.Get(key)
	if entry == nil || entry.AcquirePaused {
		t.Fatalf("字节维度不限时不得因 32MiB 事件暂停（16 MiB 之谜的形态）: paused=%v reason=%q",
			entry != nil && entry.AcquirePaused, entry.PauseReason)
	}
}
