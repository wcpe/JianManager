package catalog

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件固定「已归档（ARCHIVED）」权威态的迁移、回滚与回收闸门。
//
// 覆盖的验收点（对应 FR-501 回收热层闭环）：
//  1. 证据不完整一律拒绝（空归档/无 checksum/coverage 未完整 ⇒ 不得据此删热层）；
//  2. 只有稳定 HOT 权威可迁移；在途迁移/失败态/活跃租约一律拒绝；
//  3. 同一归档位置重跑幂等（不产生第二条归档记录）；
//  4. 归档位置不同 ⇒ 拒绝（一天两份归档必须人工裁定）；
//  5. 回收失败可回滚，且回滚把查询能力原样还给热层（投影/目录/代次都恢复）；
//  6. 已回收后拒绝回滚（数据已不在热层，回滚等于伪造）；
//  7. 同日仍有 HOT 成员 / 在途写许可 / 活跃读取时拒绝删热层目录；
//  8. 归档分区拒绝写入、其退役热层目录永久排除出 QueryPlanner。

func archiveTestNow() time.Time {
	return time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
}

func archiveTestKey() PartitionKey {
	return PartitionKey{StorageNamespace: "inst:147", UTCDay: "2026-09-01"}
}

// archiveTestEvidence 构造一份**足够完整**的归档证据。
func archiveTestEvidence() ArchiveEvidence {
	return ArchiveEvidence{
		ProviderKind:     "local",
		ObjectPrefix:     "inst:147/2026-09-01/g1",
		ManifestChecksum: "sha256:manifest",
		Coverage: ArchiveCoverage{
			ObjectCount: 3,
			SourceBytes: 4096,
			StoredBytes: 1024,
			Complete:    true,
		},
	}
}

// archiveReadyRecord 构造一条带已发布投影的稳定 HOT 记录（可查询状态）。
func archiveReadyRecord(t *testing.T) *Record {
	t.Helper()
	rec := NewStableRecord(archiveTestKey(), OwnerHot, 1, "hot-g1")
	rec.PublishedProjection = &PublishedProjection{
		ManifestVersion:       "manifest-hot-1",
		CoverageComplete:      true,
		QueryLocationDirID:    "hot-g1",
		QueryGeneration:       1,
		ProjectionGeneration:  "p-hot-1",
		ProjectionGenerations: []string{"p-hot-1"},
		ClosedVisibleSeq:      map[string]uint64{"default": 42},
	}
	rec.LastVerifyCompleted = true
	rec.LastVerifyChecksum = "checksum-hot"
	return rec
}

// TestArchiveTransition_RejectsIncompleteEvidence 证据不完整一律拒绝。
//
// 这是「空归档不得成为删除依据」的落点：`archive_copy` 的导出侧已保证空快照
// 不产出成功，但 Catalog 侧必须独立再校验一次——否则任何一天只要上游漏填，
// 一次误删就不可逆。
func TestArchiveTransition_RejectsIncompleteEvidence(t *testing.T) {
	now := archiveTestNow()
	cases := []struct {
		name string
		mut  func(*ArchiveEvidence)
	}{
		{"缺 provider kind", func(e *ArchiveEvidence) { e.ProviderKind = "" }},
		{"缺 object prefix", func(e *ArchiveEvidence) { e.ObjectPrefix = "" }},
		{"缺 manifest 校验和", func(e *ArchiveEvidence) { e.ManifestChecksum = "" }},
		{"coverage 未完整", func(e *ArchiveEvidence) { e.Coverage.Complete = false }},
		{"对象数为 0", func(e *ArchiveEvidence) { e.Coverage.ObjectCount = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := archiveReadyRecord(t)
			ev := archiveTestEvidence()
			tc.mut(&ev)
			_, err := ApplyArchiveTransition(rec, ev, now)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrArchiveEvidenceMissing), "want ErrArchiveEvidenceMissing, got %v", err)
			// 关键：拒绝时**不得**改动任何权威字段。
			require.Equal(t, OwnerHot, rec.Owner)
			require.Equal(t, "hot-g1", rec.OwnerDirID)
			require.Nil(t, rec.ArchiveEvidence)
		})
	}
}

// TestArchiveTransition_OnlyStableHotAuthority 只有稳定 HOT 权威可迁移。
func TestArchiveTransition_OnlyStableHotAuthority(t *testing.T) {
	now := archiveTestNow()

	t.Run("在途迁移拒绝", func(t *testing.T) {
		rec := archiveReadyRecord(t)
		rec.MigrationState = StateRoutingFrozen
		_, err := ApplyArchiveTransition(rec, archiveTestEvidence(), now)
		require.ErrorIs(t, err, ErrIllegalTransition)
		require.Equal(t, OwnerHot, rec.Owner)
	})

	t.Run("待恢复分区拒绝", func(t *testing.T) {
		rec := archiveReadyRecord(t)
		rec.RecoveryRequired = true
		_, err := ApplyArchiveTransition(rec, archiveTestEvidence(), now)
		require.Error(t, err)
		require.Equal(t, OwnerHot, rec.Owner)
	})

	t.Run("活跃查询租约拒绝", func(t *testing.T) {
		rec := archiveReadyRecord(t)
		rec.AddLease(QueryLeaseRef{
			LeaseID: "lease-1", ViewID: "view-1", Generation: rec.Generation,
			Owner: OwnerHot, DirID: rec.OwnerDirID,
			CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
		})
		_, err := ApplyArchiveTransition(rec, archiveTestEvidence(), now)
		require.Error(t, err)
		require.Equal(t, OwnerHot, rec.Owner, "有活跃读取租约时不得改权威")
	})

	t.Run("COLD 权威不走本路径", func(t *testing.T) {
		rec := NewStableRecord(archiveTestKey(), OwnerCold, 1, "cold-g1")
		_, err := ApplyArchiveTransition(rec, archiveTestEvidence(), now)
		require.ErrorIs(t, err, ErrIllegalTransition)
		require.Equal(t, OwnerCold, rec.Owner)
	})
}

// TestArchiveTransition_ProducesAuthoritativeArchive 迁移成功后各字段自洽。
func TestArchiveTransition_ProducesAuthoritativeArchive(t *testing.T) {
	now := archiveTestNow()
	rec := archiveReadyRecord(t)
	originalProjection := rec.PublishedProjection.ManifestVersion

	already, err := ApplyArchiveTransition(rec, archiveTestEvidence(), now)
	require.NoError(t, err)
	require.False(t, already)

	require.Equal(t, OwnerArchive, rec.Owner)
	// OwnerDirID 必须**不再**指向热层目录：否则查询规划器会指向一个即将被删的路径。
	require.Equal(t, "archive:local:inst:147/2026-09-01/g1", rec.OwnerDirID)
	require.NotEqual(t, "hot-g1", rec.OwnerDirID)

	ev := rec.ArchiveEvidence
	require.NotNil(t, ev)
	require.Equal(t, "local", ev.ProviderKind)
	require.Equal(t, "inst:147/2026-09-01/g1", ev.ObjectPrefix)
	require.Equal(t, "sha256:manifest", ev.ManifestChecksum)
	require.Equal(t, 3, ev.Coverage.ObjectCount)
	require.True(t, ev.Coverage.Complete)
	require.Equal(t, now, ev.ArchivedAt)
	require.False(t, ev.HotReclaimed, "移动权威本身不等于热层已回收")
	// 回滚依据必须完整（owner/dir/generation/投影）。
	require.Equal(t, OwnerHot, ev.PrevOwner)
	require.Equal(t, "hot-g1", ev.PrevDirID)
	require.Equal(t, uint64(1), ev.PrevGeneration)
	require.NotNil(t, ev.PrevProjection)
	require.Equal(t, originalProjection, ev.PrevProjection.ManifestVersion)

	// 旧投影必须清空：它指向即将被删除的热层目录，留着就会产出读它的租约。
	require.Nil(t, rec.PublishedProjection)
	// 写入路由冻结且无 late 目标。
	require.True(t, rec.WriteRoute.Frozen)
	require.Empty(t, rec.WriteRoute.LateOwner)
	// 热层目录进退役表（被 QueryPlanner 永久排除）。
	require.Len(t, rec.RetiredHotDirs, 1)
	require.Equal(t, "hot-g1", rec.RetiredHotDirs[0].ID)
	for _, d := range rec.Dirs {
		require.NotEqual(t, "hot-g1", d.ID, "权威目录表里不能再保留热层目录")
	}
	// 查询权威：OwnerForQuery 给出归档标识，且热层目录不可见。
	ref := OwnerForQuery(rec)
	require.True(t, ref.OK)
	require.Equal(t, OwnerArchive, ref.Owner)
	require.False(t, IsQueryPlannerVisible(rec, "hot-g1"), "已退役的热层目录不得进入 QueryPlanner")
	require.True(t, IsQueryPlannerVisible(rec, ref.DirID))
}

// TestArchiveTransition_Idempotent 同一归档位置重跑幂等；不同位置拒绝。
func TestArchiveTransition_Idempotent(t *testing.T) {
	now := archiveTestNow()
	cat := New(nil)
	key := archiveTestKey()
	require.NoError(t, cat.Put(archiveReadyRecord(t)))

	first, already, err := cat.MarkArchived(key, archiveTestEvidence(), now)
	require.NoError(t, err)
	require.False(t, already)
	seqAfterFirst := first.JournalLastSeq
	require.NotZero(t, seqAfterFirst)

	// 重跑（同 provider + 同 prefix）：幂等，不追加 journal。
	again, already, err := cat.MarkArchived(key, archiveTestEvidence(), now.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, already, "同一归档位置重跑必须幂等")
	require.Equal(t, seqAfterFirst, again.JournalLastSeq, "幂等重跑不得追加 journal")
	require.Equal(t, now, again.ArchiveEvidence.ArchivedAt, "幂等重跑不得改写归档时刻")
	require.Len(t, cat.Journal().Entries(key), 1, "重跑不产生第二条归档记录")

	// 不同归档位置：一天两份归档必须人工裁定。
	other := archiveTestEvidence()
	other.ObjectPrefix = "inst:147/2026-09-01/g2"
	_, _, err = cat.MarkArchived(key, other, now)
	require.Error(t, err)
	require.Contains(t, err.Error(), "拒绝改挂")
}

// TestArchiveRollback_RestoresQueryAuthority 回收失败回滚必须把查询能力原样还回去。
func TestArchiveRollback_RestoresQueryAuthority(t *testing.T) {
	now := archiveTestNow()
	cat := New(nil)
	key := archiveTestKey()
	require.NoError(t, cat.Put(archiveReadyRecord(t)))

	_, _, err := cat.MarkArchived(key, archiveTestEvidence(), now)
	require.NoError(t, err)

	stillArchived, err := cat.RollbackArchived(key, "热层目录被占用（模拟）", now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, stillArchived, "该日已无归档成员 ⇒ 调用方应解除日冻结")

	rec, ok := cat.Get(key)
	require.True(t, ok)
	require.Equal(t, OwnerHot, rec.Owner)
	require.Equal(t, "hot-g1", rec.OwnerDirID)
	require.Equal(t, uint64(1), rec.Generation)
	// 查询能力必须恢复：投影与目录都回到热层。
	require.NotNil(t, rec.PublishedProjection)
	require.Equal(t, "hot-g1", rec.PublishedProjection.QueryLocationDirID)
	require.Equal(t, rec.Generation, rec.PublishedProjection.QueryGeneration)
	require.True(t, IsQueryPlannerVisible(rec, "hot-g1"))
	require.False(t, rec.WriteRoute.Frozen, "回滚后必须重新接受写入")
	require.Empty(t, rec.RetiredHotDirs, "热层目录重新成为权威目录")
	// 证据保留为历史审计。
	require.NotNil(t, rec.ArchiveEvidence)
	require.Equal(t, 1, rec.ArchiveEvidence.RollbackCount)
	require.Contains(t, rec.ArchiveEvidence.LastRollbackReason, "热层目录被占用")
}

// TestArchiveRollback_RefusedAfterReclaim 已回收后拒绝回滚。
func TestArchiveRollback_RefusedAfterReclaim(t *testing.T) {
	now := archiveTestNow()
	cat := New(nil)
	key := archiveTestKey()
	require.NoError(t, cat.Put(archiveReadyRecord(t)))
	_, _, err := cat.MarkArchived(key, archiveTestEvidence(), now)
	require.NoError(t, err)
	_, err = cat.MarkArchiveReclaimed(key, now.Add(time.Minute))
	require.NoError(t, err)

	_, err = cat.RollbackArchived(key, "不该发生", now.Add(2*time.Minute))
	require.ErrorIs(t, err, ErrIllegalTransition)

	rec, _ := cat.Get(key)
	require.Equal(t, OwnerArchive, rec.Owner, "已回收后权威必须保持归档")
	require.True(t, rec.ArchiveEvidence.HotReclaimed)
}

// TestCanReclaimArchivedDay_Gates 删热层目录的四个前置缺一不可。
func TestCanReclaimArchivedDay_Gates(t *testing.T) {
	now := archiveTestNow()

	t.Run("未归档拒绝", func(t *testing.T) {
		cat := New(nil)
		key := archiveTestKey()
		require.NoError(t, cat.Put(archiveReadyRecord(t)))
		ok, reason := cat.CanReclaimArchivedDay(key)
		require.False(t, ok)
		require.Contains(t, reason, "ARCHIVED")
	})

	t.Run("同日仍有 HOT 成员拒绝", func(t *testing.T) {
		cat := New(nil)
		key := archiveTestKey()
		require.NoError(t, cat.Put(archiveReadyRecord(t)))
		other := NewStableRecord(PartitionKey{StorageNamespace: "inst:148", UTCDay: key.UTCDay}, OwnerHot, 1, "hot-g1-b")
		require.NoError(t, cat.Put(other))
		_, _, err := cat.MarkArchived(key, archiveTestEvidence(), now)
		require.NoError(t, err)

		ok, reason := cat.CanReclaimArchivedDay(key)
		require.False(t, ok, "同一物理日还有未归档命名空间时删目录会连带删掉它的数据")
		require.Contains(t, reason, "HOT 权威分区")
	})

	t.Run("归档证据不完整拒绝", func(t *testing.T) {
		cat := New(nil)
		key := archiveTestKey()
		require.NoError(t, cat.Put(archiveReadyRecord(t)))
		_, _, err := cat.MarkArchived(key, archiveTestEvidence(), now)
		require.NoError(t, err)
		// 模拟 journal 重放后证据缺字段（例如旧版本写入的记录）。
		broken := archiveReadyRecord(t)
		broken.Owner = OwnerArchive
		broken.OwnerDirID = "archive:local:x"
		broken.ArchiveEvidence = &ArchiveEvidence{ProviderKind: "local"}
		require.NoError(t, cat.Put(broken))

		ok, reason := cat.CanReclaimArchivedDay(key)
		require.False(t, ok)
		require.Contains(t, reason, "证据不完整")
	})

	t.Run("在途写许可拒绝", func(t *testing.T) {
		cat := New(nil)
		key := archiveTestKey()
		require.NoError(t, cat.Put(archiveReadyRecord(t)))
		_, _, err := cat.MarkArchived(key, archiveTestEvidence(), now)
		require.NoError(t, err)
		// 直接挂一个写许可（模拟归档前已取得、尚未释放的在途写）。
		cat.nextPermit++
		cat.permits[cat.nextPermit] = &WritePermit{Key: key, Owner: OwnerHot, DirID: "hot-g1", Generation: 1, id: cat.nextPermit, catalog: cat}

		ok, reason := cat.CanReclaimArchivedDay(key)
		require.False(t, ok, "有在途物理写者时不得删目录")
		require.Contains(t, reason, "在途写入许可")
	})

	t.Run("活跃查询租约拒绝", func(t *testing.T) {
		cat := New(nil)
		key := archiveTestKey()
		rec := archiveReadyRecord(t)
		require.NoError(t, cat.Put(rec))
		_, _, err := cat.MarkArchived(key, archiveTestEvidence(), now)
		require.NoError(t, err)
		stored := cat.records[key].Clone()
		stored.AddLease(QueryLeaseRef{
			LeaseID: "lease-late", ViewID: "view-late", Generation: stored.Generation,
			CreatedAt: now, ExpiresAt: time.Now().UTC().Add(time.Minute),
		})
		require.NoError(t, cat.Put(stored))

		ok, reason := cat.CanReclaimArchivedDay(key)
		require.False(t, ok, "正在被读的位置不得删")
		require.Contains(t, reason, "活跃查询租约")
	})

	t.Run("全部条件成立时放行", func(t *testing.T) {
		cat := New(nil)
		key := archiveTestKey()
		require.NoError(t, cat.Put(archiveReadyRecord(t)))
		_, _, err := cat.MarkArchived(key, archiveTestEvidence(), now)
		require.NoError(t, err)

		ok, reason := cat.CanReclaimArchivedDay(key)
		require.True(t, ok, "reason=%s", reason)
	})
}

// TestArchivedPartition_RejectsWrites 归档分区不再接受写入。
func TestArchivedPartition_RejectsWrites(t *testing.T) {
	now := archiveTestNow()
	cat := New(nil)
	key := archiveTestKey()
	require.NoError(t, cat.Put(archiveReadyRecord(t)))
	_, _, err := cat.MarkArchived(key, archiveTestEvidence(), now)
	require.NoError(t, err)

	_, err = cat.WriteAdmission(key)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPartitionArchived,
		"归档后必须显式拒绝写入，不得按 owner 猜一个新物理位置")
}

// TestArchiveFreeze_BlocksWritesAndDrainsSnapshot snapshot 的前置闸门由归档冻结提供。
func TestArchiveFreeze_BlocksWritesAndDrainsSnapshot(t *testing.T) {
	cat := New(nil)
	key := archiveTestKey()
	require.NoError(t, cat.Put(archiveReadyRecord(t)))

	require.NoError(t, cat.FreezeDayForArchive(key.UTCDay, "archive:"+key.UTCDay))
	// 冻结后：写入被拒（与 COLD 迁移同一道闸），快照前置（CheckPhysicalDayDrained）通过。
	_, err := cat.WriteAdmission(key)
	require.ErrorIs(t, err, ErrPhysicalDayFrozen)
	require.NoError(t, cat.CheckPhysicalDayDrained(key.UTCDay))

	// 重复冻结同一归档日幂等。
	require.NoError(t, cat.FreezeDayForArchive(key.UTCDay, "archive:"+key.UTCDay))

	// 解除冻结后写入恢复。
	require.NoError(t, cat.AbortArchiveFreeze(key.UTCDay, "测试"))
	target, err := cat.WriteAdmission(key)
	require.NoError(t, err)
	require.Equal(t, OwnerHot, target.Owner)
}

// TestArchiveFreeze_RefusesToFightMigration 搬运与归档不得抢同一天的写入路由。
func TestArchiveFreeze_RefusesToFightMigration(t *testing.T) {
	cat := New(nil)
	key := archiveTestKey()
	require.NoError(t, cat.Put(archiveReadyRecord(t)))
	// 模拟 COLD 迁移已冻结这一天。
	cat.days[key.UTCDay] = &PhysicalDayMigration{UTCDay: key.UTCDay, TargetOwner: OwnerCold, TargetDirID: "20260901", Frozen: true}

	require.ErrorIs(t, cat.FreezeDayForArchive(key.UTCDay, "archive:"+key.UTCDay), ErrPhysicalDayFrozen)
}

// TestPendingArchiveReclaims 观测面：已归档但热层未回收的分区必须可查。
func TestPendingArchiveReclaims(t *testing.T) {
	now := archiveTestNow()
	cat := New(nil)
	key := archiveTestKey()
	require.NoError(t, cat.Put(archiveReadyRecord(t)))
	_, _, err := cat.MarkArchived(key, archiveTestEvidence(), now)
	require.NoError(t, err)

	require.Equal(t, []PartitionKey{key}, cat.PendingArchiveReclaims(),
		"标记归档但尚未删除热层时必须能被看见（否则会留下无人知晓的副本）")

	_, err = cat.MarkArchiveReclaimed(key, now.Add(time.Minute))
	require.NoError(t, err)
	require.Empty(t, cat.PendingArchiveReclaims())
}

// TestPendingPhysicalDays_IgnoresArchiveFreeze 归档冻结不算 COLD 迁移恢复责任。
func TestPendingPhysicalDays_IgnoresArchiveFreeze(t *testing.T) {
	cat := New(nil)
	key := archiveTestKey()
	require.NoError(t, cat.Put(archiveReadyRecord(t)))
	require.NoError(t, cat.FreezeDayForArchive(key.UTCDay, "archive:"+key.UTCDay))

	// 归档冻结由保留策略的归档链路推进；若混进 COLD 恢复列表，
	// 恢复循环会每轮对没有固定批次的日子调用 Resume 并刷告警。
	require.Empty(t, cat.PendingPhysicalDays())
}
