package catalog

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件守「归档对象自身的回收」的**权威态**语义。
//
// 每条用例都咬住一个具体的说谎形态——这个功能只有两种错法，两种都很严重：
//  ① 删了却没说删（记录仍称「归档里有」，查询面把用户引向一条必然失败的恢复路径）；
//  ② 没说删却删了（宣称已清除，而归档里还剩半份数据，用户以为救不回来了）。

func purgeKey() PartitionKey {
	return PartitionKey{StorageNamespace: "inst:147", UTCDay: "2026-06-01"}
}

// purgeArchivedRecord 构造一条「已归档 + 热层已回收」的记录（清除的前置形态）。
func purgeArchivedRecord(t *testing.T) (*Catalog, PartitionKey) {
	t.Helper()
	cat := New(nil)
	key := purgeKey()
	rec := NewStableRecord(key, OwnerHot, 1, "20260601")
	rec.PublishedProjection = &PublishedProjection{
		ManifestVersion:      "manifest-hot",
		CoverageComplete:     true,
		QueryLocationDirID:   "20260601",
		QueryGeneration:      1,
		ProjectionGeneration: "p-hot",
	}
	ev := ArchiveEvidence{
		ProviderKind:     "local",
		ObjectPrefix:     "inst:147/20260601/g1",
		ManifestChecksum: "sha256:manifest",
		Coverage:         ArchiveCoverage{ObjectCount: 3, Complete: true},
	}
	rec.PublishedProjection = nil
	require.NoError(t, cat.Put(rec))
	_, _, err := cat.MarkArchived(key, ev, time.Unix(0, 0).UTC().Add(time.Hour))
	require.NoError(t, err)
	_, err = cat.MarkArchiveReclaimed(key, time.Unix(0, 0).UTC().Add(2*time.Hour))
	require.NoError(t, err)
	return cat, key
}

// 前置条件自检：夹具必须真的处在「可清除」形态（否则后面所有断言都在测空气）。
func TestArchivePurgeFixtureIsEligible(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	rec, ok := cat.Get(key)
	require.True(t, ok)
	require.Equal(t, OwnerArchive, rec.Owner)
	require.True(t, rec.ArchiveEvidence.HotReclaimed)
	require.False(t, rec.ArchiveEvidence.Purged)
	ok, reason := cat.ArchiveObjectPurgeEligible(key)
	require.True(t, ok, "前置条件：夹具必须可清除，否则后续用例测不到目标分支（%s）", reason)
}

// 契约：三步走完（请求 → 完成）后记录进入「已清除」终态，且审计字段齐全。
func TestArchiveObjectPurgeReachesTerminalState(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	reqAt := time.Unix(0, 0).UTC().Add(730 * 24 * time.Hour)

	rec, already, err := cat.MarkArchiveObjectPurgeRequested(key, "已过 730d 保留期", reqAt)
	require.NoError(t, err)
	require.False(t, already, "首次请求不是幂等命中")
	require.False(t, rec.ArchiveEvidence.Purged, "请求步绝不能把记录标成已清除")
	require.Equal(t, reqAt, rec.ArchiveEvidence.PurgeRequestedAt.UTC())
	require.Equal(t, 1, rec.ArchiveEvidence.PurgeAttempts)

	rec, err = cat.MarkArchiveObjectsPurged(key, "已过 730d 保留期", reqAt.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, rec.ArchiveEvidence.Purged)
	require.True(t, rec.ArchiveEvidence.ArchiveObjectPurged())
	require.False(t, rec.ArchiveEvidence.PurgedAt.IsZero())
	require.Equal(t, "已过 730d 保留期", rec.ArchiveEvidence.PurgeReason)
	require.Empty(t, rec.ArchiveEvidence.LastPurgeError)
}

// 契约（关键）：**journal 重放后终态仍然成立**。
//
// 这是本项目踩过的坑（「只改内存不落盘」）：只断言内存记录会漏掉
// 「提交没进 journal」这一类缺陷，而它在重启后表现为「数据其实没删干净但记录说删了」。
//
// 本用例**分三段重放**（归档后 / 请求后 / 完成后各重建一次 Catalog），而不是只在最后
// 重放一次：只重放最终态会让「请求步没落 journal」这类缺陷漏网——最终态仍带着
// Purged=true（它可能是由完成步一次写下的），而真实的三段语义已经丢了。
// 中间那段重放的断言咬的正是这个：请求步必须**自己**在 journal 里留下痕迹。
func TestArchiveObjectPurgeSurvivesJournalReplay(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/journal.jsonl"
	at := time.Unix(0, 0).UTC().Add(time.Hour)

	store, err := NewJSONLFileStore(path)
	require.NoError(t, err)
	journal, err := NewJournalWithStore(store)
	require.NoError(t, err)
	cat := New(journal)
	key := purgeKey()
	require.NoError(t, cat.Put(NewStableRecord(key, OwnerHot, 1, "20260601")))
	_, _, err = cat.MarkArchived(key, ArchiveEvidence{
		ProviderKind:     "local",
		ObjectPrefix:     "inst:147/20260601/g1",
		ManifestChecksum: "sha256:manifest",
		Coverage:         ArchiveCoverage{ObjectCount: 3, Complete: true},
	}, at)
	require.NoError(t, err)
	_, err = cat.MarkArchiveReclaimed(key, at.Add(time.Minute))
	require.NoError(t, err)

	// —— 第一段：请求步之后重放 ——
	_, _, err = cat.MarkArchiveObjectPurgeRequested(key, "保留期到期", at.Add(2*time.Minute))
	require.NoError(t, err)

	replay := func() *Catalog {
		t.Helper()
		s, e := NewJSONLFileStore(path)
		require.NoError(t, e)
		j, e := NewJournalWithStore(s)
		require.NoError(t, e)
		return New(j)
	}
	mid := replay()
	midRec, ok := mid.Get(key)
	require.True(t, ok, "重放后记录必须仍在")
	require.Equal(t, OwnerArchive, midRec.Owner)
	require.False(t, midRec.ArchiveEvidence.Purged, "请求步之后不得是已清除")
	require.Equal(t, 1, midRec.ArchiveEvidence.PurgeAttempts,
		"请求步必须自己落 journal（否则进程在删除中途退出时，重启后无从知道「这一轮开始删过」）")
	require.False(t, midRec.ArchiveEvidence.PurgeRequestedAt.IsZero(),
		"「本轮开始清除」的时刻必须落盘")

	// —— 第二段：完成步之后重放 ——
	_, err = cat.MarkArchiveObjectsPurged(key, "保留期到期", at.Add(3*time.Minute))
	require.NoError(t, err)

	final := replay()
	rec, ok := final.Get(key)
	require.True(t, ok, "重放后记录必须仍在")
	require.Equal(t, OwnerArchive, rec.Owner, "清除归档对象**不改变** Owner：权威仍在归档这一层")
	require.NotNil(t, rec.ArchiveEvidence)
	require.True(t, rec.ArchiveEvidence.Purged, "重放后必须仍是已清除终态（只改内存不落盘的缺陷会在此转红）")
	require.False(t, rec.ArchiveEvidence.PurgedAt.IsZero(), "清除时刻也必须落盘")
	require.Equal(t, 1, rec.ArchiveEvidence.PurgeAttempts)
	require.Empty(t, rec.ArchiveEvidence.PurgeRequestedAt, "完成后不该残留「进行中」标记")
}

// 契约：热层未回收的分区**不许**清除归档对象。
//
// 这一条的方向极易被写反：直觉是「归档已经在了、热层那份是多余的」，
// 但真相相反——此刻归档是**第二份**副本，删掉它再让热层回收跑一轮，
// 那天就真的消失了，而它本来应该是「至少留一份」。
func TestArchiveObjectPurgeRefusesWhenHotNotReclaimed(t *testing.T) {
	cat := New(nil)
	key := purgeKey()
	require.NoError(t, cat.Put(NewStableRecord(key, OwnerHot, 1, "20260601")))
	// 归档了但**没有** MarkArchiveReclaimed。
	_, _, err := cat.MarkArchived(key, ArchiveEvidence{
		ProviderKind:     "local",
		ObjectPrefix:     "inst:147/20260601/g1",
		ManifestChecksum: "sha256:manifest",
		Coverage:         ArchiveCoverage{ObjectCount: 3, Complete: true},
	}, time.Unix(0, 0).UTC().Add(time.Hour))
	require.NoError(t, err)

	_, _, err = cat.MarkArchiveObjectPurgeRequested(key, "到期", time.Unix(0, 0).UTC().Add(time.Hour*24*800))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrArchivePurgeBlocked)

	rec, _ := cat.Get(key)
	require.False(t, rec.ArchiveEvidence.Purged)
	require.True(t, rec.ArchiveEvidence.PurgeRequestedAt.IsZero(), "被拒绝时不得留下「进行中」标记")
	require.Zero(t, rec.ArchiveEvidence.PurgeAttempts, "被拒绝不算一次尝试")
}

// 契约：存在待恢复标记时**不许**清除。
//
// 待恢复标记的真相是「一次恢复失败还挂着」——此刻归档对象是唯一可用副本。
// 清除它等于把「还能救」直接判成「救不回来」，而界面上看不出任何差别。
func TestArchiveObjectPurgeRefusesWhenRecoveryRequired(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	rec, _ := cat.Get(key)
	broken := rec.Clone()
	broken.RecoveryRequired = true
	broken.PartialReasons = []string{"REHYDRATE_FAILED"}
	require.NoError(t, cat.Put(broken))

	_, _, err := cat.MarkArchiveObjectPurgeRequested(key, "到期", time.Unix(0, 0).UTC().Add(time.Hour*24*800))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrArchivePurgeBlocked)
	require.Contains(t, err.Error(), "待恢复")
}

// 契约：本地存在已恢复投影时**不许**清除（rehydrate 副本是临时的）。
func TestArchiveObjectPurgeRefusesWhenRestoredProjectionExists(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	rec, _ := cat.Get(key)
	restored := rec.Clone()
	restored.PublishedProjection = &PublishedProjection{
		ManifestVersion: "manifest-rehydrate", CoverageComplete: true,
		QueryLocationDirID: restored.OwnerDirID, QueryGeneration: restored.Generation,
		ProjectionGeneration: "rehydrate-rh-1", ProjectionGenerations: []string{"rehydrate-rh-1"},
	}
	require.NoError(t, cat.Put(restored))

	ok, reason := cat.ArchiveObjectPurgeEligible(key)
	require.False(t, ok)
	require.Contains(t, reason, "已恢复投影")

	_, _, err := cat.MarkArchiveObjectPurgeRequested(key, "到期", time.Unix(0, 0).UTC().Add(time.Hour*24*800))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrArchivePurgeBlocked)
}

// 契约：**回滚**让「已清除」保持为假，且不清零尝试计数。
//
// 删了一半就宣称清除，会让查询面给出「已按保留期清除、不可恢复」的死结论，
// 而归档里其实还有半份数据。同时一次失败尝试是真实发生过的事故，
// 回滚不能把它从审计里抹掉——否则「试过几次、上次为什么失败」就再也说不清。
func TestArchiveObjectPurgeRollbackKeepsNotPurgedAndKeepsAttempts(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	at := time.Unix(0, 0).UTC().Add(time.Hour * 24 * 800)
	_, _, err := cat.MarkArchiveObjectPurgeRequested(key, "到期", at)
	require.NoError(t, err)

	rec, err := cat.RollbackArchiveObjectPurge(key, "第 2 个对象删除失败：permission denied", at.Add(time.Second))
	require.NoError(t, err)
	require.False(t, rec.ArchiveEvidence.Purged, "回滚后绝不能是已清除")
	require.True(t, rec.ArchiveEvidence.PurgeRequestedAt.IsZero(), "进行中标记必须清掉")
	require.Equal(t, 1, rec.ArchiveEvidence.PurgeAttempts, "尝试次数是审计事实，回滚不得清零")
	require.Contains(t, rec.ArchiveEvidence.LastPurgeError, "permission denied")

	// 下一轮重试：已经是新的一次尝试。
	rec, _, err = cat.MarkArchiveObjectPurgeRequested(key, "到期（重试）", at.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 2, rec.ArchiveEvidence.PurgeAttempts, "重试必须累加尝试次数")
}

// 契约：已清除后**拒绝**回滚（数据已不在归档里，回滚等于凭空声明「还在」）。
func TestArchiveObjectPurgeRejectsRollbackAfterPurged(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	at := time.Unix(0, 0).UTC().Add(time.Hour * 24 * 800)
	_, _, err := cat.MarkArchiveObjectPurgeRequested(key, "到期", at)
	require.NoError(t, err)
	_, err = cat.MarkArchiveObjectsPurged(key, "到期", at.Add(time.Second))
	require.NoError(t, err)

	_, err = cat.RollbackArchiveObjectPurge(key, "误操作", at.Add(2*time.Second))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrIllegalTransition)

	rec, _ := cat.Get(key)
	require.True(t, rec.ArchiveEvidence.Purged, "拒绝回滚后仍必须是已清除")
}

// 契约：完成步**必须先有请求步**——否则事后无从分辨「没删过」与「删完但没记」。
func TestArchiveObjectPurgeCompleteRequiresRequestStep(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	_, err := cat.MarkArchiveObjectsPurged(key, "到期", time.Unix(0, 0).UTC().Add(time.Hour*24*800))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrIllegalTransition)

	rec, _ := cat.Get(key)
	require.False(t, rec.ArchiveEvidence.Purged)
}

// 契约：请求与完成**各自幂等**——重跑不追加 journal、不改首次清除时刻。
//
// 审计时间线漂移会让「到底哪天清掉的」变得不可回答，而那是合规问询的第一个问题。
func TestArchiveObjectPurgeIsIdempotent(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	at := time.Unix(0, 0).UTC().Add(time.Hour * 24 * 800)
	_, _, err := cat.MarkArchiveObjectPurgeRequested(key, "到期", at)
	require.NoError(t, err)
	seqAfterRequest := mustSeq(t, cat, key)

	// 重跑请求：已清除才幂等——此刻尚未清除，故这是一次新的尝试（次数 +1）。
	rec, already, err := cat.MarkArchiveObjectPurgeRequested(key, "到期", at.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, already)
	require.Equal(t, 2, rec.ArchiveEvidence.PurgeAttempts)

	first, err := cat.MarkArchiveObjectsPurged(key, "到期", at.Add(2*time.Minute))
	require.NoError(t, err)
	purgedAt := first.ArchiveEvidence.PurgedAt
	seqAfterComplete := mustSeq(t, cat, key)
	require.Greater(t, seqAfterComplete, seqAfterRequest)

	// 重跑完成：**不**追加 journal、**不**改清除时刻。
	second, err := cat.MarkArchiveObjectsPurged(key, "到期（重跑）", at.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, purgedAt, second.ArchiveEvidence.PurgedAt, "重跑不得改写清除时刻（审计时间线会漂移）")
	require.Equal(t, seqAfterComplete, mustSeq(t, cat, key), "已清除后重跑不得追加第二条 journal")

	// 已清除后重跑请求：返回 already=true，且不改任何字段。
	third, already, err := cat.MarkArchiveObjectPurgeRequested(key, "到期（重跑）", at.Add(2*time.Hour))
	require.NoError(t, err)
	require.True(t, already, "已清除后请求步必须报幂等命中")
	require.Equal(t, purgedAt, third.ArchiveEvidence.PurgedAt)
	require.Equal(t, 2, third.ArchiveEvidence.PurgeAttempts, "幂等命中不得推进尝试次数")
}

func mustSeq(t *testing.T, cat *Catalog, key PartitionKey) uint64 {
	t.Helper()
	rec, ok := cat.Get(key)
	require.True(t, ok)
	return rec.JournalLastSeq
}

// 契约：清除**不改变** Owner（权威仍在归档这一层），也不动既有回收判据。
//
// 这是「方案二」的核心断言：若哪天有人改成新增 OwnerPurged，TierOf /
// NamespaceForOwner / writeAdmissionLocked 的 default 分支会各自给出不同答案。
// 同时核对 PendingArchiveReclaims 不被干扰——它是「热层没回收」的告警面，
// 与归档对象是否清除是两件事：一个「已归档但热层残留」的分区必须**继续**出现在里面，
// 否则真实的残留会从告警里消失（那正是这个读数存在的理由）。
func TestArchiveObjectPurgeKeepsOwnerAndPendingReclaimSignal(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	// 再造一个「已归档但热层未回收」的分区：清除另一个分区不能把它从告警面里带走。
	otherKey := PartitionKey{StorageNamespace: "inst:148", UTCDay: "2026-06-02"}
	require.NoError(t, cat.Put(NewStableRecord(otherKey, OwnerHot, 1, "20260602")))
	_, _, err := cat.MarkArchived(otherKey, ArchiveEvidence{
		ProviderKind: "local", ObjectPrefix: "inst:148/2026-06-02/g1",
		ManifestChecksum: "sha256:m2", Coverage: ArchiveCoverage{ObjectCount: 2, Complete: true},
	}, time.Unix(0, 0).UTC().Add(time.Hour))
	require.NoError(t, err)

	require.Equal(t, []PartitionKey{otherKey}, cat.PendingArchiveReclaims(),
		"前置条件：另一分区热层未回收，必须出现在告警面")

	at := time.Unix(0, 0).UTC().Add(time.Hour * 24 * 800)
	_, _, err = cat.MarkArchiveObjectPurgeRequested(key, "到期", at)
	require.NoError(t, err)
	_, err = cat.MarkArchiveObjectsPurged(key, "到期", at.Add(time.Second))
	require.NoError(t, err)

	rec, _ := cat.Get(key)
	require.Equal(t, OwnerArchive, rec.Owner)
	require.Equal(t, "archive:local:inst:147/20260601/g1", rec.OwnerDirID,
		"归档权威标识不得因清除对象而改变")
	// 清除本身不制造也不消除「热层残留」信号：另一个分区仍在里面（逐项相等）。
	require.Equal(t, []PartitionKey{otherKey}, cat.PendingArchiveReclaims(),
		"清除归档对象不得干扰「已归档但热层未回收」这一既有判据")

	// 查询侧可见性：已清除的热层目录仍必须继续被排除（不能让 re-attach 把它塞回查询面）。
	require.False(t, cat.IsQueryDirVisible(key, "20260601"))
}

// 契约：不是 ARCHIVED 权威态的分区一律拒绝（不得顺手把 HOT/COLD 的归档证据改掉）。
func TestArchiveObjectPurgeRejectsNonArchivedOwner(t *testing.T) {
	cat := New(nil)
	key := PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-05-01"}
	require.NoError(t, cat.Put(NewStableRecord(key, OwnerHot, 1, "20260501")))

	_, _, err := cat.MarkArchiveObjectPurgeRequested(key, "到期", time.Unix(0, 0).UTC())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrIllegalTransition)
}

// 契约：证据不完整（无 prefix / 无 checksum / coverage 未声明完整）一律拒绝。
//
// 「说不清归档里有什么」的记录不能成为「删掉归档对象」的依据——这与
// 「空归档不得作为回收热层的依据」是同一条论证的另一半。
func TestArchiveObjectPurgeRejectsIncompleteEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ArchiveEvidence)
	}{
		{"缺 object prefix", func(e *ArchiveEvidence) { e.ObjectPrefix = "" }},
		{"缺 manifest 校验和", func(e *ArchiveEvidence) { e.ManifestChecksum = "" }},
		{"coverage 未声明完整", func(e *ArchiveEvidence) { e.Coverage.Complete = false }},
		{"对象数为 0", func(e *ArchiveEvidence) { e.Coverage.ObjectCount = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat, key := purgeArchivedRecord(t)
			rec, _ := cat.Get(key)
			broken := rec.Clone()
			tc.mutate(broken.ArchiveEvidence)
			require.NoError(t, cat.Put(broken))

			_, _, err := cat.MarkArchiveObjectPurgeRequested(key, "到期", time.Unix(0, 0).UTC())
			require.Error(t, err, "证据不完整必须拒绝")
			require.ErrorIs(t, err, ErrArchiveEvidenceMissing)
		})
	}
}

// 契约：有活跃查询租约时不许动（不能掀掉正在进行的读取）。
//
// 时间基准刻意取**真实挂钟**（而不是夹具的 epoch 偏移）：Catalog.ArchiveObjectPurgeEligible
// 与 MarkArchiveObjectPurgeRequested 的零值 now 都落到 time.Now()，用一个 1970 年的
// 假时钟造租约，会在「观测面判可清除、纯函数判有租约」之间造出一个假的矛盾——
// 那种矛盾不是缺陷，测夹具自己制造的时区错觉。
func TestArchiveObjectPurgeRefusesWithActiveLease(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	now := time.Now().UTC()
	rec, _ := cat.Get(key)
	leased := rec.Clone()
	leased.AddLease(QueryLeaseRef{
		LeaseID: "lease-1", ViewID: "view-1", Generation: rec.Generation,
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	require.NoError(t, cat.Put(leased))
	require.Equal(t, 1, leased.ActiveLeaseCount(now), "前置条件：租约必须真的处于活跃期")

	ok, reason := cat.ArchiveObjectPurgeEligible(key)
	require.False(t, ok)
	require.Contains(t, reason, "租约")

	// 纯逻辑函数同一结论（它接受 now，便于验证「租约过期后即可清除」）。
	next := leased.Clone()
	_, err := ApplyArchiveObjectPurgeRequest(next, "到期", now)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrArchivePurgeBlocked)

	// 租约过期后同一分区可以清除：这是「晚一轮」而不是「永远不清」。
	next2 := leased.Clone()
	_, err = ApplyArchiveObjectPurgeRequest(next2, "到期", now.Add(2*time.Minute))
	require.NoError(t, err)
	require.False(t, next2.ArchiveEvidence.PurgeRequestedAt.IsZero())
}

// 契约：ArchivedPartitions 只枚举 ARCHIVED 且有证据的分区（含已清除的）。
//
// 已清除的也要枚举出来：策略层要据此算「这类分区已经处理过了」，
// 而不是每轮把它们当成新候选重新判一遍（那会把次数与日志噪声都放大）。
func TestArchivedPartitionsEnumeratesArchivedOnly(t *testing.T) {
	cat, key := purgeArchivedRecord(t)
	require.NoError(t, cat.Put(NewStableRecord(PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-05-01"}, OwnerHot, 1, "20260501")))

	at := time.Unix(0, 0).UTC().Add(time.Hour * 24 * 800)
	_, _, err := cat.MarkArchiveObjectPurgeRequested(key, "到期", at)
	require.NoError(t, err)
	_, err = cat.MarkArchiveObjectsPurged(key, "到期", at.Add(time.Second))
	require.NoError(t, err)

	got := cat.ArchivedPartitions()
	require.Len(t, got, 1, "HOT 分区不得出现在归档枚举里")
	require.Equal(t, key, got[0].Key)
	require.True(t, got[0].ArchiveEvidence.Purged)
}
