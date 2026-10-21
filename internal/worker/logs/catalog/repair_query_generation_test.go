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
