package acquire

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 账本拒绝**推进**回收位置时，WAL 仍必须按**当前**水位剪枝（2026-09-30 生产实证）。
//
// 缺了它：wal.TryReclaim 一遇错便 return → pruneReclaimed 永不执行 → 连水位**之下的**
// 条目也永远保留 → 积压只增不减 → 滞回（≤上限一半）永不满足 → 源永久停在 paused
// （实测某源 7998 条卡死数小时，积压数字一个字节不动）。
//
// 形态刻意与生产一致：回收位置恰好等于分段边界（CoversTo == Reclaim），
// 于是 `CoversTo > Reclaim` 不成立 → 账本报错，而水位之下的一切其实早已判定安全。
//
// 变异验证：把 TryReclaim 的错误分支改回「直接 return」，本测试立即转红。
func TestTryReclaim_StillPrunesWhenLedgerDeclinesAdvance(t *testing.T) {
	key := testKey("src-prune", "g1")
	led := ledger.New()
	wal := NewWAL(led, key)

	// 一条 durable 事件，Record.End = 99（落在一段 0..100 的覆盖之内）。
	evs := buildWALEvents("src-prune", "g1", 0, 1, "line")
	require.NoError(t, wal.Append(evs...))
	require.NoError(t, wal.Commit())
	require.Len(t, wal.Snapshot(), 1)

	// 铺好账本：段 0..100 走完责任转移并推进回收 → Reclaim=100。
	led.Ensure(key, logtypes.SourceIdentity{LogSourceID: "src-prune", SourceGeneration: "g1"})
	require.NoError(t, led.AdvanceDurable(key, 100))
	require.NoError(t, led.RecordDelivery(key, 0, 100, logtypes.DeliveryRequestDone))
	require.NoError(t, led.RegisterRecovery(key, ledger.RecoveryRef{
		SegmentID: "seg1", State: logtypes.RecoveryStaged, CoversFrom: 0, CoversTo: 100,
	}))
	require.NoError(t, led.TransitionRecovery(key, "seg1", logtypes.RecoveryDurableVerified, "", ""))
	require.NoError(t, led.TransitionRecovery(key, "seg1", logtypes.RecoveryWALResponsibilityXfer, "", "receiver-A"))
	pos, err := led.TryReclaim(key)
	require.NoError(t, err)
	require.Equal(t, uint64(100), pos, "首次回收应推进到分段末端")

	// 第二次：**构造真实的「账本拒绝推进」形态**。
	//
	// 2026-10-20 修正：原先用「回收位置恰好等于分段边界（CoversTo == Reclaim）」来制造拒绝，
	// 但账本后来修了「Reclaim 已追平读水位时返回幂等成功」——该形态现在**不再报错**（那正是
	// 补段空转的修复）。本用例要守的意图不变（账本拒绝推进时 WAL 仍须剪枝），故改用仍真实
	// 存在的拒绝形态：读水位前进到 Reclaim 之前、且没有任何恢复分段覆盖它 ⇒ 存在真洞 ⇒ 哨兵。
	require.NoError(t, led.AdvanceRead(key, 200))
	_, err = led.TryReclaim(key)
	require.Error(t, err, "夹具应处于「账本拒绝继续推进」的状态（Reclaim < Read 且无段覆盖）")

	_, err = wal.TryReclaim()
	require.Error(t, err, "WAL 侧同样应观察到账本拒绝推进")
	require.Empty(t, wal.Snapshot(),
		"账本拒绝推进时仍须剪掉水位之下的条目：否则积压永不回落、源永久停在 paused")
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
	if err := wal.Restore(entries); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	// 恢复后必须当场裁到上限内（上限 3，恢复 5 条 ⇒ 丢最旧 2 条）。
	if n := len(wal.Snapshot()); n != 3 {
		t.Fatalf("恢复越界积压后必须立即裁剪（条目闸 %d 条，实测积压 %d）", 3, n)
	}
	// 必须丢最旧、保留最新。
	if got := wal.Snapshot()[0].Event.Record.Start; got != 31 {
		t.Fatalf("必须丢最旧前缀：剩下的首条应从 31 开始，实测 %d", got)
	}
	// 不得因此暂停采集。
	if entry := led.Get(key); entry != nil && entry.AcquirePaused {
		t.Fatalf("丢弃语义下恢复后不得暂停采集: %q", entry.PauseReason)
	}
	// 丢弃必须留凭据。
	sawDrop := false
	if entry := led.Get(key); entry != nil {
		for _, g := range entry.Gaps {
			if g.Reason == ledger.GapReasonWALBacklogDropped {
				sawDrop = true
			}
		}
	}
	if !sawDrop {
		t.Fatal("恢复期裁剪必须登记 WAL_BACKLOG_DROPPED 凭据")
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
