package lifecycle

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

func dayFixture(t *testing.T) (*catalog.Catalog, *catalog.MemJournal, []catalog.PartitionKey) {
	t.Helper()
	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)
	keys := []catalog.PartitionKey{{StorageNamespace: "node:1", UTCDay: "2026-09-23"}, {StorageNamespace: "inst:9", UTCDay: "2026-09-23"}}
	for index, key := range keys {
		rec := catalog.NewStableRecord(key, catalog.OwnerHot, 1, "hot-day")
		rec.PublishedProjection = &catalog.PublishedProjection{ManifestVersion: "hot", ProjectionGeneration: "p-hot",
			CoverageComplete: true, QueryLocationDirID: "hot-day", QueryGeneration: 1,
			ClosedVisibleSeq: map[string]uint64{key.String(): uint64(index + 20)}}
		require.NoError(t, cat.AppendJournal(catalog.JournalEntry{Key: key, State: catalog.StateCleaned,
			Owner: rec.Owner, Generation: rec.Generation, DirID: rec.OwnerDirID,
			AuthorityCommit: true, JournalComplete: true, Record: rec}))
	}
	return cat, journal, keys
}

func dayOps() Ops {
	return Ops{Drain: func(catalog.PartitionKey) error { return nil },
		Snapshot:   func(catalog.PartitionKey, string) (string, error) { return "snapshot", nil },
		Copy:       func(catalog.PartitionKey, string, string) (string, error) { return "copy", nil },
		Verify:     func(catalog.PartitionKey, string, string) (string, error) { return "checksum", nil },
		Attach:     func(catalog.PartitionKey, string) error { return nil },
		LeaseDrain: func(catalog.PartitionKey, uint64) error { return nil },
		Detach:     func(catalog.PartitionKey, string) error { return nil },
		Cleanup:    func(catalog.PartitionKey, string) error { return nil }}
}

func stateRank(state catalog.MigrationState) int {
	rank, _ := catalog.HappyIndex(state)
	return rank
}

func TestDayManagerMigratesAllNamespacesTogether(t *testing.T) {
	cat, _, keys := dayFixture(t)
	manager := NewDayManager(cat, dayOps(), RetentionGateFunc(func(catalog.PartitionKey, string, string) (bool, string) { return true, "ok" }))
	require.NoError(t, manager.Start("2026-09-23", catalog.OwnerCold, "20260923"))
	for index, key := range keys {
		rec, ok := cat.Get(key)
		require.True(t, ok)
		require.Equal(t, catalog.OwnerCold, rec.Owner)
		require.Equal(t, catalog.StateCleaned, rec.MigrationState)
		require.True(t, rec.JournalComplete)
		require.EqualValues(t, index+20, rec.PublishedProjection.ClosedVisibleSeq[key.String()])
		require.False(t, cat.IsQueryDirVisible(key, "hot-day"))
	}
}

func TestDayManagerResumesBatchFromEveryState(t *testing.T) {
	for _, stop := range catalog.HappyPath {
		t.Run(string(stop), func(t *testing.T) {
			cat, journal, keys := dayFixture(t)
			_, err := cat.BeginDayMigration("2026-09-23", catalog.OwnerCold, "20260923")
			require.NoError(t, err)
			if stop != catalog.StateRoutingFrozen {
				for _, state := range []catalog.MigrationState{catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify} {
					_, err = cat.AdvanceDay("2026-09-23", state)
					require.NoError(t, err)
					if state == stop {
						break
					}
				}
			}
			if stateRank(stop) >= stateRank(catalog.StateAttachedStaging) {
				require.NoError(t, cat.RecordDayVerification("2026-09-23", "checksum"))
				if current := cat.RecordsForDay("2026-09-23")[0].MigrationState; current != catalog.StateAttachedStaging {
					_, err = cat.AdvanceDay("2026-09-23", catalog.StateAttachedStaging)
					require.NoError(t, err)
				}
			}
			if stateRank(stop) >= stateRank(catalog.StateOwnerSwitched) {
				projections := map[catalog.PartitionKey]*catalog.PublishedProjection{}
				for _, key := range keys {
					rec, _ := cat.Get(key)
					projections[key] = migratedProjection(rec.PublishedProjection, "20260923", 2)
				}
				_, err = cat.SwitchDayOwners("2026-09-23", catalog.OwnerCold, "20260923", projections)
				require.NoError(t, err)
			}
			for _, state := range []catalog.MigrationState{catalog.StateQueryLeaseDraining, catalog.StateDetached} {
				if stateRank(stop) >= stateRank(state) {
					_, err = cat.AdvanceDay("2026-09-23", state)
					require.NoError(t, err)
				}
			}
			if stop == catalog.StateCleaned {
				require.NoError(t, cat.ClearDayResiduals("2026-09-23"))
				require.NoError(t, cat.MarkDayComplete("2026-09-23", "checksum"))
				_, err = cat.AdvanceDay("2026-09-23", catalog.StateCleaned)
				require.NoError(t, err)
			}
			restarted := catalog.New(journal)
			restarted.StartingRecover()
			manager := NewDayManager(restarted, dayOps(), RetentionGateFunc(func(catalog.PartitionKey, string, string) (bool, string) { return true, "ok" }))
			require.NoError(t, manager.Resume("2026-09-23"))
			for _, key := range keys {
				rec, _ := restarted.Get(key)
				require.Equal(t, catalog.StateCleaned, rec.MigrationState)
				require.Equal(t, catalog.OwnerCold, rec.Owner)
			}
		})
	}
}

// TestResumeMixedDayUsesActiveRecordNotFirst 钉住「混合态日推进时，代表记录必须取自
// **活跃子集**，不能沿用 `records[0]`」。
//
// 缺陷形态（2026-10-22 复审 P1-1）：混合态日里 `records[0]`（按 key 排序的第一条）
// 很可能是一条**已 CLEANED** 的记录。旧代码用它取：
//   · `MigrationFromDirID` / `TargetDirID` / `TargetOwner` —— 目录与目标全是别人的；
//   · `LastVerifyCompleted` —— 已 CLEANED 记录在 MarkDayComplete 时被置 true
//     ⇒ `if !records[0].LastVerifyCompleted` 直接为假 ⇒ **整段 snapshot/copy/verify
//     被跳过**，直接推进到 ATTACHED_STAGING；
//   · `LastVerifyChecksum` —— 用别人的校验和结账。
// 后果：新 namespace 的当日数据**从未拷到冷层**，却被标成「已迁移」并把日 journal
// 置完成，随后 Cleanup 清掉旧目录 ⇒ 与本次修复要消灭的丢数据完全同形。
//
// 而这条路径在修混合态判定之前是**不可达**的（会先在 divergence 处报错退出），
// 也就是说判定统一之后它才开始可达 —— 必须一起修，否则是把「响亮失败」换成「静默丢数据」。
//
// 变异验证：把 `rep` 改回 `records[0]`（例如 `rep = records[0]`）⇒ 本用例转红。
func TestResumeMixedDayUsesActiveRecordNotFirst(t *testing.T) {
	const day = "2026-09-20"
	// 排序上 a- 开头的 key 会排在 m- 之前 ⇒ records[0] 必定是**已结清**的那条。
	settledKey := catalog.PartitionKey{StorageNamespace: "a-settled", UTCDay: day}
	activeKey := catalog.PartitionKey{StorageNamespace: "m-active", UTCDay: day}

	journal := catalog.NewMemJournal()
	cat := catalog.New(journal)

	settled := catalog.NewStableRecord(settledKey, catalog.OwnerCold, 1, "cold-settled")
	settled.MigrationState = catalog.StateCleaned
	settled.LastVerifyCompleted = true                      // 已结清：校验"已完成"
	settled.LastVerifyChecksum = "checksum-of-settled-record" // 别人的校验和
	settled.MigrationFromDirID = "dir-settled-old"
	settled.TargetDirID = "dir-settled-new"
	settled.TargetOwner = catalog.OwnerCold
	require.NoError(t, cat.Put(settled))

	active := catalog.NewStableRecord(activeKey, catalog.OwnerHot, 1, "")
	require.NoError(t, cat.Put(active))

	// 让活跃记录进入 ATTACHED_STAGING 之前的 STAGING_VERIFY（本用例要验的就是这一步）。
	_, err := cat.BeginDayMigration(day, catalog.OwnerCold, "cold-active-target")
	require.NoError(t, err)
	for _, st := range []catalog.MigrationState{
		catalog.StateDraining, catalog.StateSnapshotting, catalog.StateStagingVerify,
	} {
		_, err = cat.AdvanceDay(day, st)
		require.NoError(t, err)
	}

	// 记录操作拿到了哪些目录/目标：用它们判断代表记录取自哪一条。
	var copiedToDir, snapshotFromDir string
	m := &DayManager{
		cat: cat,
		ops: Ops{
			Drain: func(catalog.PartitionKey) error { return nil },
			Snapshot: func(_ catalog.PartitionKey, fromDir string) (string, error) {
				snapshotFromDir = fromDir
				return "snap-1", nil
			},
			Copy: func(_ catalog.PartitionKey, _, toDir string) (string, error) {
				copiedToDir = toDir
				return "copy-1", nil
			},
			Verify:   func(catalog.PartitionKey, string, string) (string, error) { return "sum-active", nil },
			Attach:   func(catalog.PartitionKey, string) error { return nil },
			Detach:   func(catalog.PartitionKey, string) error { return nil },
			Cleanup:  func(catalog.PartitionKey, string) error { return nil },
		},
	}

	require.NoError(t, m.Resume(day))

	// 核心断言：所有目录都必须来自**活跃**记录（m-active），不能是已结清的 a-settled。
	require.NotEqual(t, "dir-settled-old", snapshotFromDir,
		"快照的源目录取自已结清记录 ⇒ 代表记录选错（records[0] 而不是活跃子集）")
	require.NotEqual(t, "dir-settled-new", copiedToDir,
		"拷贝的目标目录取自已结清记录 ⇒ 代表记录选错（records[0] 而不是活跃子集）")
	// 活跃记录刚由 BeginMigration 建立，其 OwnerDirID 是空的 ⇒ 快照源目录必须是空串，
	// 而不是已结清记录的 `dir-settled-old`。这条同时锁住「代表记录来自活跃子集」。
	require.Equal(t, "", snapshotFromDir,
		"快照源目录必须来自活跃记录（其 OwnerDirID 为空），不得取已结清记录")
}
