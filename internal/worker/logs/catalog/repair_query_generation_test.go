package catalog

// 本文件是「投影 query_generation 落后于权威 generation」的修复回归（2026-10-21 生产既成损害）。
//
// 现场背景（`catalog.journal.jsonl` 实测，已核对到具体 seq）：
//   - `resetNamespaceConflictCounts` 的旧实现用无 CAS 的 `AppendJournal` 做派生计数对齐，
//     并把 `Generation` 自增 1 却**不动** `PublishedProjection.QueryGeneration`；
//   - 生产 `inst:320` 因此留下 28 个 `generation=2 ∧ query_generation=1` 的分区，
//     覆盖 2026-09-20～2026-10-19 的**活跃日期**；
//   - 这类分区此后永远无法再发布（`PublishProjection` 一进门就
//     `projection and authority do not match`），采集侧的补投/重建因此恒失败。

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// staleQueryGenCatalog 造一个「权威代次 2、投影仍写着 1」的目录（复刻生产形态）。
//
// 造法刻意走**正规路径**而不是直接改字段：先按代次 1 发布，再用 `AppendJournal`
// 把 Generation 推到 2（这正是旧实现的错误动作），从而得到与现场逐字同构的记录。
func staleQueryGenCatalog(t *testing.T) *Catalog {
	t.Helper()
	c := New(nil)
	require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
	proj := &PublishedProjection{
		ManifestVersion:    "pv-1",
		CoverageComplete:   true,
		ClosedVisibleSeq:   map[string]uint64{"src-a": 100},
		QueryLocationDirID: "hot-g1",
		QueryGeneration:    1,
	}
	err := c.PublishProjection(c.mustGet(t), mustRecordWithProjection(c, testKey(), proj))
	require.NoError(t, err)

	// 旧实现的错误动作：只把 Generation 自增，不动 QueryGeneration。
	cur, ok := c.Get(testKey())
	require.True(t, ok)
	bumped := cur.Clone()
	bumped.Generation = 2
	require.NoError(t, c.AppendJournal(JournalEntry{
		Key: bumped.Key, Record: bumped, State: bumped.MigrationState,
		Detail: "derived conflict_count reconciled after identity-conflict clearance",
	}))
	return c
}

func (c *Catalog) mustGet(t *testing.T) *Record {
	t.Helper()
	rec, ok := c.Get(testKey())
	require.True(t, ok)
	return rec
}

// mustRecordWithProjection 以**当前权威记录**为基底，只替换投影字段。
//
// 为什么不能凭空造 Record：`PublishProjection` 要求 `next` 的
// owner/generation/owner_dir/write_route 与 `expected` 完全一致（"cannot change authority"），
// 凭空构造会立刻违反该前置条件，测到的就不是本意了。
func mustRecordWithProjection(c *Catalog, key PartitionKey, proj *PublishedProjection) *Record {
	cur, ok := c.Get(key)
	if !ok || cur == nil {
		return nil
	}
	next := cur.Clone()
	next.PublishedProjection = proj
	return next
}

// TestRepairProjectionQueryGenerationFixesStalePartition 固定「落后的分区能被修好，
// 且修好后可以继续正常发布」。
//
// 为什么它能转红：把 `RepairProjectionQueryGeneration` 改成直接 `return false, "", nil`
// （不做任何修复）⇒ 记录仍是 `gen=2, qgen=1`、后续 `PublishProjection` 仍报
// `projection and authority do not match` ⇒ 两条断言同时失败。
func TestRepairProjectionQueryGenerationFixesStalePartition(t *testing.T) {
	c := staleQueryGenCatalog(t)

	before, ok := c.Get(testKey())
	require.True(t, ok)
	require.EqualValues(t, 2, before.Generation)
	require.EqualValues(t, 1, before.PublishedProjection.QueryGeneration,
		"夹具前提：必须造出 query_generation 落后于 generation 的形态")

	// 修复前：连正规发布都过不了前置校验（这正是现场的「永远无法再发布」）。
	err := c.PublishProjection(before, func() *Record {
		n := before.Clone()
		n.PublishedProjection.ManifestVersion = "pv-2"
		return n
	}())
	require.ErrorIs(t, err, ErrInvalidState,
		"夹具前提：修复前该分区必须无法发布（projection and authority do not match）")

	fixed, manual, err := c.RepairProjectionQueryGeneration(testKey())
	require.NoError(t, err)
	require.True(t, fixed, "落后的分区必须被修好")
	require.Empty(t, manual, "能自动修好的分区不属于「需人工」")

	after, ok := c.Get(testKey())
	require.True(t, ok)
	require.EqualValues(t, 2, after.PublishedProjection.QueryGeneration,
		"query_generation 必须对齐到权威 generation")
	require.EqualValues(t, 2, after.Generation, "权威代次本身不得被改变")
	require.Equal(t, before.OwnerDirID, after.OwnerDirID, "权威目录不得被改变")
	require.Equal(t, before.PublishedProjection.ManifestVersion, after.PublishedProjection.ManifestVersion,
		"投影清单版本不得被本次代次对齐顺手改掉")
	require.EqualValues(t, before.PublishedProjection.ClosedVisibleSeq["src-a"],
		after.PublishedProjection.ClosedVisibleSeq["src-a"], "封闭水位不得被改动")

	// 修复后：正规发布必须能通过前置校验（这才是「修好了」的实证）。
	err = c.PublishProjection(after, func() *Record {
		n := after.Clone()
		n.PublishedProjection.ManifestVersion = "pv-2"
		return n
	}())
	require.NoError(t, err, "修好之后该分区必须能重新发布")

	// 幂等：再修一次不应再改动任何东西。
	again, manual, err := c.RepairProjectionQueryGeneration(testKey())
	require.NoError(t, err)
	require.False(t, again, "已一致的分区不得被重复修复")
	require.Empty(t, manual, "已一致 ⇒ 既不需要修复，也不属于「需人工」")
}

// TestRepairProjectionQueryGenerationLeavesHealthyAndAmbiguousRecords 固定「不误修」边界，
// 并固定「不修也必须报」：三种只能交人工的形态都要有非空的 needsManual 原因。
//
// 为什么「不修也要报」必须被钉住（2026-10-21 复审缺陷 A4）：早前这三种形态一律
// `return false, nil`，调用方只在「修复数或失败数 > 0」时打日志 ⇒ 它们既不计数也不留痕，
// 运维看到的是「自愈完成」，实际还有 N 个分区永久不可发布。
func TestRepairProjectionQueryGenerationLeavesHealthyAndAmbiguousRecords(t *testing.T) {
	t.Run("健康记录不动", func(t *testing.T) {
		c := New(nil)
		require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
		proj := &PublishedProjection{
			ManifestVersion: "pv-1", CoverageComplete: true,
			QueryLocationDirID: "hot-g1", QueryGeneration: 1,
		}
		err := c.PublishProjection(c.mustGet(t), mustRecordWithProjection(c, testKey(), proj))
		require.NoError(t, err)

		fixed, manual, err := c.RepairProjectionQueryGeneration(testKey())
		require.NoError(t, err)
		require.False(t, fixed, "已一致的记录不得被修复")
		require.Empty(t, manual, "健康记录不属于「需人工」：needsManual 必须为空，否则 WARN 会误报")
	})

	t.Run("反向矛盾不动_交人工", func(t *testing.T) {
		c := New(nil)
		require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
		// query_generation 反而**大于** generation：成因未知，一律不碰。
		//
		// 造法必须走 AppendJournal：`PublishProjection` 的前置校验恰好会拒绝这种形态
		// （qgen != gen 一律拒），用它造夹具根本造不出来——早前这个子用例因此实际只测到了
		// 「没有已发布投影」那一条分支，名不副实（2026-10-21 复审一并修正）。
		cur, ok := c.Get(testKey())
		require.True(t, ok)
		reversed := cur.Clone()
		reversed.PublishedProjection = &PublishedProjection{
			ManifestVersion: "pv-1", CoverageComplete: true,
			QueryLocationDirID: "hot-g1", QueryGeneration: 5,
		}
		require.NoError(t, c.AppendJournal(JournalEntry{
			Key: reversed.Key, Record: reversed, State: reversed.MigrationState,
		}))

		fixed, manual, err := c.RepairProjectionQueryGeneration(testKey())
		require.NoError(t, err)
		require.False(t, fixed, "反向矛盾（qgen > gen）不得被自动修复，应交人工判断")
		require.NotEmpty(t, manual, "反向矛盾必须报成「需人工」，不得静默返回无需修复")
		require.Contains(t, manual, "反向矛盾", "原因文案必须说清是哪种形态")

		still, ok := c.Get(testKey())
		require.True(t, ok)
		require.EqualValues(t, 5, still.PublishedProjection.QueryGeneration, "反向矛盾分区不得被改写")
		require.EqualValues(t, 1, still.Generation, "权威代次不得被抬到 qgen（那等于凭空造一次权威前进）")
	})

	t.Run("目录也不一致时不动", func(t *testing.T) {
		c := staleQueryGenCatalog(t)
		// 把投影指向的目录改成非权威目录：属另一类损坏，不应被「顺手修好」。
		cur, ok := c.Get(testKey())
		require.True(t, ok)
		broken := cur.Clone()
		broken.PublishedProjection.QueryLocationDirID = "some-old-dir"
		require.NoError(t, c.AppendJournal(JournalEntry{Key: broken.Key, Record: broken, State: broken.MigrationState}))

		fixed, manual, err := c.RepairProjectionQueryGeneration(testKey())
		require.NoError(t, err)
		require.False(t, fixed, "目录已不一致的记录不得只修代次（会掩盖另一类损坏）")
		require.NotEmpty(t, manual, "目录不一致必须报成「需人工」")
	})

	t.Run("不存在或无投影", func(t *testing.T) {
		c := New(nil)
		_, _, err := c.RepairProjectionQueryGeneration(testKey())
		require.ErrorIs(t, err, ErrNotFound)

		require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
		fixed, manual, err := c.RepairProjectionQueryGeneration(testKey())
		require.NoError(t, err)
		require.False(t, fixed, "没有已发布投影时无事可做")
		require.NotEmpty(t, manual, "没有已发布投影必须报成「需人工」，不得静默返回无需修复")
	})
}

// TestRepairProjectionQueryGenerationIsJournalReplayable 确认修复本身经 journal 重放后仍成立。
//
// 为什么要独立一条：catalog 的持久化真源是 journal，若修复只改内存不落 journal，
// 重启后会被打回原形（本项目的历史教训：跑在内存里的"对齐"等于没做）。
func TestRepairProjectionQueryGenerationIsJournalReplayable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.journal.jsonl")

	store, err := NewJSONLFileStore(path)
	require.NoError(t, err)
	journal, err := NewJournalWithStore(store)
	require.NoError(t, err)
	c := New(journal)
	require.NoError(t, c.Put(NewStableRecord(testKey(), OwnerHot, 1, "hot-g1")))
	proj := &PublishedProjection{
		ManifestVersion: "pv-1", CoverageComplete: true,
		QueryLocationDirID: "hot-g1", QueryGeneration: 1,
	}
	err = c.PublishProjection(c.mustGet(t), mustRecordWithProjection(c, testKey(), proj))
	require.NoError(t, err)
	cur, _ := c.Get(testKey())
	bumped := cur.Clone()
	bumped.Generation = 2
	require.NoError(t, c.AppendJournal(JournalEntry{
		Key: bumped.Key, Record: bumped, State: bumped.MigrationState,
		Detail: "derived conflict_count reconciled after identity-conflict clearance",
	}))

	fixed, manual, err := c.RepairProjectionQueryGeneration(testKey())
	require.NoError(t, err)
	require.True(t, fixed)
	require.Empty(t, manual)
	// 重放：修复必须仍然成立，不能被 journal 回滚。
	reopenedStore, err := NewJSONLFileStore(path)
	require.NoError(t, err)
	reopened, err := NewJournalWithStore(reopenedStore)
	require.NoError(t, err)
	replayed := New(reopened)
	rec, ok := replayed.Get(testKey())
	require.True(t, ok)
	require.EqualValues(t, 2, rec.Generation)
	require.EqualValues(t, 2, rec.PublishedProjection.QueryGeneration,
		"修复必须落在 journal 里，重启重放后仍然一致")
}

// TestMixedDayMigrationSkipsSettledRecords 钉住「混合态 UTC 日可迁移」。
//
// 缺陷形态（2026-10-21/22 架构复审 P0-1）：某日整体下沉到冷层后，迟到的写入或新出现的
// storage namespace 仍会在同一天新建一条 `Owner=hot` 的记录 ⇒ 该日长期处于
// 「部分已 COLD、部分仍 hot」的混合态。旧实现对每个日级操作都取 `records[0]` 当基准
// 并要求**全部**记录同态：
//
//	· `BeginDayMigration` 遇到已 COLD 就整日拒绝；
//	· `AdvanceDay` / `FailDay` / `SwitchDayOwners` 同理；
//	· `day_manager.Resume` 报 `UTC-day state divergence` 且**不写失败 journal**。
//
// 后果是该日**永久不可迁移**，而那条 hot 分区到期被 VL 原生 retention 直接删除
// ⇒ 该 namespace 该日数据永久丢失，UI 上只留一次迁移失败。
//
// 修法：`activeDayRecords` 统一判据 —— 空态与 CLEANED 不参与本次迁移，
// 其余必须同态（那才是真分歧）。本用例覆盖 Begin/Advance/Fail/Switch 四处。
//
// 变异验证：把 `activeDayRecords` 改回「返回全部记录」⇒ 第一处断言即红
// （BeginDayMigration 遇到已 COLD 会报 owner 非法）。
func TestMixedDayMigrationSkipsSettledRecords(t *testing.T) {
	const day = "2026-09-20"
	keyHot := PartitionKey{StorageNamespace: "ns-hot", UTCDay: day}
	keyCold := PartitionKey{StorageNamespace: "ns-cold", UTCDay: day}

	newMixed := func(t *testing.T) *Catalog {
		t.Helper()
		c := New(nil)
		// 已下沉完成的那条（Owner=cold，状态 CLEANED）。
		settled := NewStableRecord(keyCold, OwnerCold, 1, "cold-g1")
		settled.MigrationState = StateCleaned
		require.NoError(t, c.Put(settled))
		// 迟到的 hot 记录（该日新增的 namespace）。
		require.NoError(t, c.Put(NewStableRecord(keyHot, OwnerHot, 1, "hot-g1")))
		return c
	}

	t.Run("BeginDayMigration 跳过已下沉记录", func(t *testing.T) {
		c := newMixed(t)
		recs, err := c.BeginDayMigration(day, OwnerCold, "cold-g2")
		require.NoError(t, err, "混合态日必须可开始迁移（旧实现遇到已 COLD 就整日拒绝）")
		require.NotEmpty(t, recs)

		hot, ok := c.Get(keyHot)
		require.True(t, ok)
		require.Equal(t, StateRoutingFrozen, hot.MigrationState, "hot 记录必须进入迁移")

		cold, ok := c.Get(keyCold)
		require.True(t, ok)
		require.Equal(t, StateCleaned, cold.MigrationState,
			"已下沉记录不得被本次迁移改动（它是已完成，不是冲突）")
	})

	t.Run("AdvanceDay 只推进迁移中的记录", func(t *testing.T) {
		c := newMixed(t)
		_, err := c.BeginDayMigration(day, OwnerCold, "cold-g2")
		require.NoError(t, err)
		_, err = c.AdvanceDay(day, StateDraining)
		require.NoError(t, err, "混合态日必须能推进状态")
		hot, _ := c.Get(keyHot)
		require.Equal(t, StateDraining, hot.MigrationState)
		cold, _ := c.Get(keyCold)
		require.Equal(t, StateCleaned, cold.MigrationState, "已下沉记录仍不变")
	})

	t.Run("FailDay 在混合态下仍能记录失败", func(t *testing.T) {
		c := newMixed(t)
		_, err := c.BeginDayMigration(day, OwnerCold, "cold-g2")
		require.NoError(t, err)
		// 旧实现要求全一致 ⇒ 失败本身都记不下来，现场查不到原因。
		_, err = c.FailDay(day, StateFailedRetryable, "drain 失败夹具")
		require.NoError(t, err, "混合态日必须能记录失败（否则现场无从排障）")
		hot, _ := c.Get(keyHot)
		require.Equal(t, StateFailedRetryable, hot.MigrationState)
	})

	t.Run("推进到 ATTACHED_STAGING 后 SwitchDayOwners 可切换", func(t *testing.T) {
		c := newMixed(t)
		_, err := c.BeginDayMigration(day, OwnerCold, "cold-g2")
		require.NoError(t, err)
		for _, st := range []MigrationState{StateDraining, StateSnapshotting, StateStagingVerify, StateAttachedStaging} {
			_, err = c.AdvanceDay(day, st)
			require.NoError(t, err)
		}
		proj := &PublishedProjection{
			ManifestVersion: "pv-1", CoverageComplete: true,
			QueryLocationDirID: "cold-g2", QueryGeneration: 2,
		}
		_, err = c.SwitchDayOwners(day, OwnerCold, "cold-g2",
			map[PartitionKey]*PublishedProjection{keyHot: proj})
		require.NoError(t, err, "混合态日必须能完成权威切换（旧实现对已 COLD 记录调 ApplyOwnerSwitch 必失败）")
		hot, _ := c.Get(keyHot)
		require.Equal(t, OwnerCold, hot.Owner, "hot 记录必须切到 cold")
		require.Equal(t, StateOwnerSwitched, hot.MigrationState)
	})
}

// TestMarkDayCompleteRefusesHotResidual 钉住「混合态日不得被结账」。
//
// 缺陷形态（2026-10-22 复审 P1-2）：`BeginDayMigration` 按 **Owner** 纳入记录，
// 而后续日级操作（`activeDayRecords`）按 **MigrationState** 排除空态。两套判据不一致，
// 于是「`BeginDayMigration` 之后才出现的 `Owner=hot, state=""` 记录」处在夹缝里：
//
//	· 它不参与后续推进（被当「不参与」跳过）；
//	· 但它也不阻止该日走到 `MarkDayComplete`；
//	· 而 `MarkDayComplete` 无条件对**整日全部**记录置
//	  `JournalComplete/LastVerifyCompleted` ⇒ 这条从未迁移的 hot 记录被标成「已完成」。
//
// 此后任何一致性检查都不再报警，它的热分区在 VL retention 到期时被直接删除
// ⇒ 该 namespace 该日数据永久丢失。
//
// 修法：`MarkDayComplete` 前校验该日不得残留任何 `Owner=hot` 记录。
//
// 变异验证：删掉那段 Owner 校验 ⇒ 本用例第一条断言转红。
func TestMarkDayCompleteRefusesHotResidual(t *testing.T) {
	const day = "2026-09-21"
	settledKey := PartitionKey{StorageNamespace: "ns-settled", UTCDay: day}
	hotKey := PartitionKey{StorageNamespace: "ns-late-hot", UTCDay: day}

	t.Run("有 hot 残留时必须拒绝结账", func(t *testing.T) {
		c := New(NewMemJournal())
		// 先做成「尚未结账」的形态（NewStableRecord 默认 JournalComplete=true）。
		settled := NewStableRecord(settledKey, OwnerCold, 1, "cold-g1")
		settled.JournalComplete = false
		settled.LastVerifyCompleted = false
		require.NoError(t, c.Put(settled))
		// 「BeginDayMigration 之后才出现」的那条：Owner=hot、state 为空。
		late := NewStableRecord(hotKey, OwnerHot, 1, "")
		late.MigrationState = ""
		late.JournalComplete = false
		require.NoError(t, c.Put(late))

		err := c.MarkDayComplete(day, "sum-1")
		require.Error(t, err, "该日仍有 hot 记录时不得结账（否则它会被标成已完成、永不迁移）")
		require.ErrorIs(t, err, ErrIllegalTransition)

		// 失败的结账不得留下部分变更。
		rec, ok := c.Get(settledKey)
		require.True(t, ok)
		require.False(t, rec.JournalComplete, "失败的结账不得留下部分变更")
		hot, ok := c.Get(hotKey)
		require.True(t, ok)
		require.False(t, hot.JournalComplete, "从未迁移的记录更不得被标成已完成")
	})

	t.Run("无 hot 残留时必须能正常结账", func(t *testing.T) {
		c := New(NewMemJournal())
		settled := NewStableRecord(settledKey, OwnerCold, 1, "cold-g1")
		settled.JournalComplete = false
		require.NoError(t, c.Put(settled))

		require.NoError(t, c.MarkDayComplete(day, "sum-1"),
			"全冷态日必须能结账，否则守卫变成永久阻塞")
		rec, ok := c.Get(settledKey)
		require.True(t, ok)
		require.True(t, rec.JournalComplete)
		require.Equal(t, "sum-1", rec.LastVerifyChecksum)
	})
}

// TestBeginMigrationResetsVerifyCredentials 钉住「新一轮迁移必须清掉上一轮的校验凭证」。
//
// 缺陷形态（2026-10-22 复审 P1-4）：`MarkDayComplete`/`RecordDayVerification` 会无条件
// 对**整日全部**记录置 `LastVerifyCompleted=true` + `LastVerifyChecksum`，包括那些
// 从未参与迁移的记录。而 `BeginMigration` 只重置了 `JournalComplete`，把凭证留了下来。
//
// 后果链：该记录日后真被迁移时，`DayManager.Resume` 里 `if !rep.LastVerifyCompleted`
// 直接为假 ⇒ 整段 snapshot/copy/verify 被跳过 ⇒ 直接进入 ATTACHED_STAGING，
// 随后 `cleanupHot` 凭这条**陈旧 checksum** 删除热分区
// ⇒ 数据从未拷到冷层却被删掉。
//
// 变异验证：删掉 `rec.LastVerifyCompleted = false` ⇒ 本用例第一条断言转红。
func TestBeginMigrationResetsVerifyCredentials(t *testing.T) {
	const day = "2026-09-22"
	key := PartitionKey{StorageNamespace: "ns-reuse", UTCDay: day}

	rec := NewStableRecord(key, OwnerHot, 1, "dir-hot")
	// 上一轮遗留的陈旧凭证（模拟被 MarkDayComplete 无差别打上）。
	rec.LastVerifyCompleted = true
	rec.LastVerifyChecksum = "stale-checksum-from-last-round"
	rec.JournalComplete = true

	require.NoError(t, BeginMigration(rec, OwnerCold, "dir-cold-target"))

	require.False(t, rec.LastVerifyCompleted,
		"新一轮迁移必须清掉 LastVerifyCompleted；否则 StagingVerify 会跳过 snapshot/copy/verify")
	require.Empty(t, rec.LastVerifyChecksum,
		"陈旧 checksum 不得留到新一轮；cleanupHot 会凭它删除从未拷贝的热分区")
	require.False(t, rec.JournalComplete, "JournalComplete 同样必须重置")
	require.Equal(t, StateRoutingFrozen, rec.MigrationState)
}
