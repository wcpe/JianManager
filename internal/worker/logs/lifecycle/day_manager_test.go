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
//
//	· `MigrationFromDirID` / `TargetDirID` / `TargetOwner` —— 目录与目标全是别人的；
//	· `LastVerifyCompleted` —— 已 CLEANED 记录在 MarkDayComplete 时被置 true
//	  ⇒ `if !records[0].LastVerifyCompleted` 直接为假 ⇒ **整段 snapshot/copy/verify
//	  被跳过**，直接推进到 ATTACHED_STAGING；
//	· `LastVerifyChecksum` —— 用别人的校验和结账。
//
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
	settled.LastVerifyCompleted = true                        // 已结清：校验"已完成"
	settled.LastVerifyChecksum = "checksum-of-settled-record" // 别人的校验和
	settled.MigrationFromDirID = "dir-settled-old"
	settled.TargetDirID = "dir-settled-new"
	settled.TargetOwner = catalog.OwnerCold
	require.NoError(t, cat.Put(settled))

	// **活跃记录的 OwnerDirID 必须非空且与 settled 不同**（2026-10-22 复审 P1-3）。
	//
	// 若留空（初版写法），三种情形会退化出同一个值 ""：
	//   ① 取错记录（rep=records[0]）⇒ LastVerifyCompleted=true ⇒ 整段 snapshot/copy
	//      **从不执行** ⇒ snapshotFromDir 保持零值 "";
	//   ② 正确取活跃记录 ⇒ 其 OwnerDirID 也是 ""（BeginMigration 从这里赋值）;
	// 于是断言分不清「没执行」与「执行对了」，`rep = records[0]` 变异**照样全绿** ——
	// 正是本项目反复中招的假绿形态。给了非空值后三态互异：
	//   从不执行 "" / 正确 "dir-active-old" / 取错 "dir-settled-old"。
	active := catalog.NewStableRecord(activeKey, catalog.OwnerHot, 1, "dir-active-old")
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
			Verify:  func(catalog.PartitionKey, string, string) (string, error) { return "sum-active", nil },
			Attach:  func(catalog.PartitionKey, string) error { return nil },
			Detach:  func(catalog.PartitionKey, string) error { return nil },
			Cleanup: func(catalog.PartitionKey, string) error { return nil },
		},
	}

	require.NoError(t, m.Resume(day))

	// **唯一有判别力的断言**（2026-10-22 复审 P1）。
	//
	// 活跃记录自带 `dir-active-old`（夹具里显式给的非空目录），而已结清记录是
	// `dir-settled-old`。三态因此互异：
	//   · `""`                 ⇒ snapshot 整段没执行（取错记录 ⇒ LastVerifyCompleted=true ⇒ 跳过校验）
	//   · `dir-settled-old`    ⇒ snapshot 执行了，但拿的是已结清记录的目录
	//   · `dir-active-old`     ⇒ 正确：代表记录来自活跃子集
	//
	// 为什么必须断言 `dir-active-old` 而不是「不是 settled 的值」：把活跃记录的
	// OwnerDirID 留空时，`""` 既是「没执行」也是「执行对了」的取值 ⇒ 断言分不清两者，
	// 声明要防的变异（rep = records[0]）照样全绿。这正是本项目反复中招的假绿形态，
	// 而当时的注释还写着「其 OwnerDirID 是空的 ⇒ 必须是空串」，会把下一位复审者
	// 直接引回假绿 —— 该注释已按实际夹具改写。
	require.Equal(t, "dir-active-old", snapshotFromDir,
		"快照源目录必须是活跃记录自己的 OwnerDirID；\"\" 表示 snapshot 根本没执行（取错记录导致跳过校验）")
	// 下面两条是**辅助**断言（对 `rep = records[0]` 变异不具判别力，因为 "" 同时
	// 不等于这两个值）；保留它们只为在「取到已结清目录」这种更具体的形式下给出提示。
	require.NotEqual(t, "dir-settled-old", snapshotFromDir,
		"快照源目录取自已结清记录 ⇒ 代表记录选错（records[0] 而不是活跃子集）")
	require.NotEqual(t, "dir-settled-new", copiedToDir,
		"拷贝的目标目录取自已结清记录 ⇒ 代表记录选错（records[0] 而不是活跃子集）")
}

// TestResumeAutoReinitiatesResidualHotMigration 钉住「残留 hot 记录有自动出口」。
//
// 缺陷形态（2026-10-22 终审 P2）：`MarkDayComplete` 的结账守卫拦住「含 Owner=hot
// 残留的日」是对的（远好于静默丢数据），但没有出口时该日会无解死循环：
//
//	MarkDayComplete 拒绝 → fail() → FAILED_RETRYABLE
//	→ 下轮 sweep 从 FAILED_RETRYABLE 恢复 → 再次拒绝
//
// 且 retention.Mover 只记 KeptOriginal，既不重试计数也不升级 FAILED_MANUAL
// ⇒ 每轮 sweep 追加 2 条 journal、重跑 Cleanup，永不收敛。
//
// 这类记录的成因是**必然的**：某日整体下沉到冷层后，迟到的写入或新出现的
// storage namespace 会在同一天新建一条 hot 记录，而 `NewStableRecord` 的默认态
// 就是 `MigrationState=CLEANED` ⇒ 天生是 `hot + CLEANED`，既不在迁移路径上
// （按 state 过滤会被跳过），又会被结账守卫拒绝。
//
// 修法：`Resume` 进来先自动为这类记录**重新发起迁移**（`BeginMigration` 允许从
// `CLEANED` 起跳），目标复用该日既定的 TargetOwner/TargetDirID。
//
// 变异验证：删掉 `reinitiateResidualHotMigration` 的调用 ⇒ 本用例转红
// （残留记录仍停在 CLEANED，没被纳入迁移）。
func TestResumeAutoReinitiatesResidualHotMigration(t *testing.T) {
	const day = "2026-09-26"
	settledKey := catalog.PartitionKey{StorageNamespace: "ns-a-settled", UTCDay: day}
	lateKey := catalog.PartitionKey{StorageNamespace: "ns-z-late", UTCDay: day}

	cat := catalog.New(catalog.NewMemJournal())

	// 已下沉完成的那条：带上既定的 target，供残留记录复用。
	settled := catalog.NewStableRecord(settledKey, catalog.OwnerCold, 2, "dir-cold")
	settled.MigrationState = catalog.StateCleaned
	settled.TargetOwner = catalog.OwnerCold
	settled.TargetDirID = "dir-cold"
	require.NoError(t, cat.Put(settled))

	// 迟到新建的那条：hot + CLEANED（默认态），从未参与迁移。
	late := catalog.NewStableRecord(lateKey, catalog.OwnerHot, 1, "dir-late")
	require.Equal(t, catalog.StateCleaned, late.MigrationState, "夹具前提：默认态是 CLEANED")
	require.Equal(t, catalog.OwnerHot, late.Owner)
	require.NoError(t, cat.Put(late))

	m := &DayManager{
		cat: cat,
		ops: Ops{
			Drain:    func(catalog.PartitionKey) error { return nil },
			Snapshot: func(catalog.PartitionKey, string) (string, error) { return "snap", nil },
			Copy:     func(catalog.PartitionKey, string, string) (string, error) { return "copy", nil },
			Verify:   func(catalog.PartitionKey, string, string) (string, error) { return "sum", nil },
			Attach:   func(catalog.PartitionKey, string) error { return nil },
			Detach:   func(catalog.PartitionKey, string) error { return nil },
			Cleanup:  func(catalog.PartitionKey, string) error { return nil },
		},
	}

	// 触发 Resume：它必须先自动重新发起迁移，把残留记录纳入。
	_ = m.Resume(day)

	lateRec, ok := cat.Get(lateKey)
	require.True(t, ok)
	// **核心断言：这条记录确实被纳入了迁移**。
	//
	// 不断言「最终仍是 CLEANED」——本用例的 ops 全部返回成功，`Resume` 会把
	// 整条 happy path 一次跑完，最终状态回到 CLEANED 是**正确**的收尾。
	// 要区分「自愈完成」与「从未参与」，看的是迁移痕迹：
	//   · TargetDirID 被写入（= 走过 BeginMigration 的目标登记）
	//   · MigrationFromDirID 是本记录自己的 OwnerDirID（= 用的是它自己的数据）
	//   · owner 已切到 cold（= 权威真的换了）
	require.Equal(t, "dir-cold", lateRec.TargetDirID,
		"目标必须复用该日既定目录，不得另起一个（否则冷层出现两份互不相认的副本）")
	require.Equal(t, "dir-late", lateRec.MigrationFromDirID,
		"源目录必须是这条记录自己的 OwnerDirID，不能取别的记录的")
	require.Equal(t, catalog.OwnerCold, lateRec.Owner,
		"完成后 owner 必须是 cold；若仍是 hot 说明它只是被标记而没真正迁移")
}

// TestResumeDoesNotReinitiateWhileMigrationInFlight 钉住「有在飞迁移时不得重新发起」。
//
// 缺陷形态（2026-10-22 代码审查 P0，本仓库自己引入）：
// `reinitiateResidualHotMigration` 为残留记录调 `BeginDayMigration`，而后者会对该日
// **所有** hot 记录调 `BeginMigration`。混合态日必然出现「某条正在迁移 + 另一条迟到新建」：
//
//	BeginMigration 只接受稳定态（空 / CLEANED / 失败态），
//	对 ROUTING_FROZEN…DETACHED 会返回 `begin from <state>`
//
// 于是整日初始化失败、每轮 sweep 重复报错 —— 比原本「只在结账时卡住」更早卡死，
// 且连 MarkDayComplete 的留痕都到不了。原提交声称的「无需人工介入」在这条路径上不成立。
//
// 修法：该日存在迁移中间态记录时**不插手**（交给正常推进）；只有该日完全静止、
// 只剩残留时才重新发起。
//
// 变异验证：删掉 `reinitiateResidualHotMigration` 里那段在飞检查 ⇒ 本用例转红
// （实际报 `reinitiate residual migration …: begin from SNAPSHOTTING`）。
func TestResumeDoesNotReinitiateWhileMigrationInFlight(t *testing.T) {
	const day = "2026-09-27"
	inflightKey := catalog.PartitionKey{StorageNamespace: "ns-a-inflight", UTCDay: day}
	lateKey := catalog.PartitionKey{StorageNamespace: "ns-z-late", UTCDay: day}

	cat := catalog.New(catalog.NewMemJournal())

	// 一条正在迁移的记录：hot + SNAPSHOTTING，已登记既定 target。
	inflight := catalog.NewStableRecord(inflightKey, catalog.OwnerHot, 1, "dir-inflight")
	inflight.MigrationState = catalog.StateSnapshotting
	inflight.TargetOwner = catalog.OwnerCold
	inflight.TargetDirID = "dir-cold-target"
	require.NoError(t, cat.Put(inflight))

	// 迟到新建的残留：hot + CLEANED（默认态）。
	late := catalog.NewStableRecord(lateKey, catalog.OwnerHot, 1, "dir-late")
	require.Equal(t, catalog.StateCleaned, late.MigrationState, "夹具前提：默认态是 CLEANED")
	require.NoError(t, cat.Put(late))

	m := &DayManager{
		cat: cat,
		ops: Ops{
			Drain:    func(catalog.PartitionKey) error { return nil },
			Snapshot: func(catalog.PartitionKey, string) (string, error) { return "snap", nil },
			Copy:     func(catalog.PartitionKey, string, string) (string, error) { return "copy", nil },
			Verify:   func(catalog.PartitionKey, string, string) (string, error) { return "sum", nil },
			Attach:   func(catalog.PartitionKey, string) error { return nil },
			Detach:   func(catalog.PartitionKey, string) error { return nil },
			Cleanup:  func(catalog.PartitionKey, string) error { return nil },
		},
	}

	// 核心断言：不得因为「另一条记录正在迁移中」而整体失败。
	err := m.Resume(day)
	require.NoError(t, err,
		"混合态日（迁移中 + 迟到残留）必须能正常推进；报 begin from/divergence 说明重新发起错误地插手了在飞迁移")

	// 在飞的那条应被正常推进（SNAPSHOTTING → 更远），而不是被重新发起打断。
	inflightRec, ok := cat.Get(inflightKey)
	require.True(t, ok)
	require.NotEqual(t, catalog.StateSnapshotting, inflightRec.MigrationState,
		"在飞记录必须被正常推进，而不是被重新发起打断")
}
