package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
)

// chmodReadOnly 把目录改成只读，使其中文件的删除真实失败。
//
// 为什么用真实文件权限而不是注入假 error：这一层测的就是「真实 Provider 的 Delete
// 返回真错误时，执行器与 Catalog 会不会一起退回原态」。用假 error 会把
// LocalArchive 对这些错误的实际处理方式整个跳过。
func chmodReadOnly(t *testing.T, dir string) error {
	t.Helper()
	if err := os.Chmod(dir, 0o555); err != nil {
		return err
	}
	// 测试结束后恢复可写，否则 t.TempDir 的清理会失败。
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	return nil
}

// 本文件是「归档对象回收」的**端到端**回归：真实 catalog + 真实本地归档目录 +
// 本包的生产适配层（newCatalogArchivedObjectLister / newArchiveProviderObjectStore /
// newCatalogArchiveObjectAuthority）。
//
// 为什么放在这里而不是 retention 包：retention 刻意不依赖 archive（见其包注释），
// 真实 Provider 与真实目录布局只能在这个装配层连起来测。于是本文件同时钉住两件事：
//   - 执行器与 Catalog 的次序在真件上成立（权威态先落、再删对象、最后标完成）；
//   - 适配层自己那两处逻辑（前缀解析、provider kind 复核）确实把请求导向了正确位置。
//
// 两端缺一都会说谎：只验目录 ⇒ 「删了但记录说没删」（下一轮重删、每轮报警）；
// 只验记录 ⇒ 「记录了但对象还在」（平台宣称已清除、盘一点没省）。

const e2ePurgeRetention = 730 * 24 * time.Hour

func e2ePurgeNow() time.Time { return time.Date(2027, 6, 1, 3, 0, 0, 0, time.UTC) }

// e2ePurgeFixture 建一条「已归档 + 热层已回收」的分区，并在真实目录里铺好对象。
func e2ePurgeFixture(t *testing.T, archivedAt time.Time) (cat *catalog.Catalog, key catalog.PartitionKey, root string) {
	t.Helper()
	cat = catalog.New(nil)
	key = catalog.PartitionKey{StorageNamespace: "inst:147", UTCDay: "2025-03-01"}
	require.NoError(t, cat.Put(catalog.NewStableRecord(key, catalog.OwnerHot, 1, "20250301")))

	prefix := key.StorageNamespace + "/" + key.UTCDay + "/g1"
	_, _, err := cat.MarkArchived(key, catalog.ArchiveEvidence{
		ProviderKind:     archive.KindLocal,
		ObjectPrefix:     prefix,
		ManifestChecksum: "sha256:manifest-e2e",
		Coverage:         catalog.ArchiveCoverage{ObjectCount: 2, Complete: true},
		ArchivedAt:       archivedAt,
	}, archivedAt)
	require.NoError(t, err)
	_, err = cat.MarkArchiveReclaimed(key, archivedAt.Add(time.Minute))
	require.NoError(t, err)

	rec, ok := cat.Get(key)
	require.True(t, ok)
	require.True(t, rec.ArchiveEvidence.HotReclaimed, "前置条件：热层必须已回收")
	require.False(t, rec.ArchiveEvidence.Purged, "前置条件：尚未清除")

	// 真实归档目录，按 archive 包固定布局铺两个对象 + 一个 manifest。
	root = t.TempDir()
	provider := archive.NewLocalArchive(root)
	akey := archive.PartitionKey{
		StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay, Generation: 1,
	}
	require.NoError(t, provider.Put(context.Background(), akey, "manifest.json", []byte("{}")))
	require.NoError(t, provider.Put(context.Background(), akey, "raw/files/a.gz", []byte("A")))
	require.NoError(t, provider.Put(context.Background(), akey, "raw/files/b.gz", []byte("B")))

	objects, err := provider.List(context.Background(), akey)
	require.NoError(t, err)
	require.Len(t, objects, 3, "前置条件：目录里应有 3 个对象，得到 %v", objects)
	return cat, key, root
}

// e2ePurgePolicy 打开全部三闸并启用归档对象回收（删除的唯一合法前提）。
func e2ePurgePolicy() retention.Policy {
	p := retention.DefaultPolicy()
	p.Enabled = true
	p.Discard = true
	p.Sweep.VLSweep = true
	p.Sweep.VLDeleteEnabled = true
	p.ArchiveObjectRetention = retention.ArchiveObjectRetentionPolicy{
		Enabled:     true,
		Retention:   e2ePurgeRetention,
		SweepAtHour: 3,
	}
	return p
}

// 契约（全链路）：三闸全开 + 已过保留期 ⇒ 归档目录被清空，且 Catalog 落已清除终态。
func TestE2E_ArchiveObjectPurgeClearsDirectoryAndCatalog(t *testing.T) {
	archivedAt := e2ePurgeNow().Add(-800 * 24 * time.Hour) // 已过 730d
	cat, key, root := e2ePurgeFixture(t, archivedAt)

	// 候选由生产 lister 给出（不是测试自己拼的）——这样也顺带钉住适配层。
	lister := newCatalogArchivedObjectLister(cat)
	refs, err := lister.ListArchivedObjects()
	require.NoError(t, err)
	require.Len(t, refs, 1)
	require.Empty(t, refs[0].BlockedReason, "前置条件：候选必须可清除（%s）", refs[0].BlockedReason)

	store := newArchiveProviderObjectStore(archive.NewLocalArchive(root), root)
	require.NotNil(t, store, "前置条件：本地 Provider 必须能装配")
	auth := newCatalogArchiveObjectAuthority(cat)
	require.NotNil(t, auth)

	r := retention.NewArchiveObjectReclaimer(e2ePurgePolicy(), lister, store, auth)
	ok, reason := r.Ready()
	require.True(t, ok, "前置条件：三个出口必须齐备（%s）", reason)

	res, err := r.Step(context.Background(), e2ePurgeNow())
	require.NoError(t, err)
	require.Equal(t, 1, res.Purged, "应清除 1 个分区（%v）", res.Outcomes)

	// 数据面：目录里的对象真的没了。
	akey := archive.PartitionKey{
		StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay, Generation: 1,
	}
	left, err := archive.NewLocalArchive(root).List(context.Background(), akey)
	require.NoError(t, err)
	require.Empty(t, left, "归档目录应被清空，实际残留 %v", left)

	// 权威面：Purged 终态且在 journal 里（Get 走的是提交后的记录）。
	rec, ok2 := cat.Get(key)
	require.True(t, ok2)
	require.Equal(t, catalog.OwnerArchive, rec.Owner, "清除归档对象不改变「正本在哪一层」")
	require.True(t, rec.ArchiveEvidence.Purged)
	require.False(t, rec.ArchiveEvidence.PurgedAt.IsZero())
	require.Equal(t, 1, rec.ArchiveEvidence.PurgeAttempts)
	require.True(t, rec.ArchiveEvidence.PurgeRequestedAt.IsZero(), "完成后不该残留「进行中」标记")

	// 幂等：再跑一轮，一个对象都不该删、尝试次数不推进、清除时刻不漂移。
	res2, err := r.Step(context.Background(), e2ePurgeNow().Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 0, res2.Purged, "已清除的分区不该被重复清除")
	after, _ := cat.Get(key)
	require.Equal(t, 1, after.ArchiveEvidence.PurgeAttempts, "幂等重跑不得推进尝试次数")
	require.Equal(t, rec.ArchiveEvidence.PurgedAt, after.ArchiveEvidence.PurgedAt,
		"幂等重跑不得改写清除时刻（审计时间线会漂移）")
}

// 契约（全链路）：删除失败 ⇒ **两侧都保持诚实**——目录仍有残留、Catalog 仍是未清除。
//
// 这是本功能最危险的一半：只要权威面先说了「已清除」，查询面就会给用户一个
// 「恢复不了」的死结论，而归档里其实还躺着数据。
func TestE2E_ArchiveObjectPurgeFailureKeepsBothSidesHonest(t *testing.T) {
	archivedAt := e2ePurgeNow().Add(-800 * 24 * time.Hour)
	cat, key, root := e2ePurgeFixture(t, archivedAt)

	// 把目录的写权限摘掉，让 Delete 失败（真实文件系统错误，不是注入的假 error）。
	akey := archive.PartitionKey{
		StorageNamespace: key.StorageNamespace, UTCDay: key.UTCDay, Generation: 1,
	}
	filesDir := archive.NewLocalArchive(root).PartitionDir(akey)
	require.NoError(t, chmodReadOnly(t, filesDir))

	lister := newCatalogArchivedObjectLister(cat)
	store := newArchiveProviderObjectStore(archive.NewLocalArchive(root), root)
	auth := newCatalogArchiveObjectAuthority(cat)
	r := retention.NewArchiveObjectReclaimer(e2ePurgePolicy(), lister, store, auth)

	res, err := r.Step(context.Background(), e2ePurgeNow())
	require.NoError(t, err)
	require.Equal(t, 0, res.Purged, "删除失败不得记已清除")
	require.Equal(t, 1, res.RolledBack, "删除失败必须回滚权威态")

	// 权威面：仍是「未清除」，并留下失败原因与尝试次数。
	rec, ok := cat.Get(key)
	require.True(t, ok)
	require.False(t, rec.ArchiveEvidence.Purged, "回滚后绝不能是已清除")
	require.True(t, rec.ArchiveEvidence.PurgeRequestedAt.IsZero(), "进行中标记必须清掉")
	require.Equal(t, 1, rec.ArchiveEvidence.PurgeAttempts, "失败尝试必须留在审计里")
	require.NotEmpty(t, rec.ArchiveEvidence.LastPurgeError)

	// 数据面：对象仍在（回滚不假装把删掉的恢复回来）。
	left, err := archive.NewLocalArchive(root).List(context.Background(), akey)
	require.NoError(t, err)
	require.NotEmpty(t, left, "失败时归档目录里应仍有对象")
}

// 契约：Catalog 记录的归档前缀非法 ⇒ 存储出口**拒绝**，绝不猜一个位置去删。
//
// 前缀由导出侧用 PartitionKey.String() 生成，固定三段。若记录被人改坏（或来自别的
// 版本格式），按「尽量解析」去删等于在一个可能完全不相干的位置执行删除。
func TestArchiveProviderObjectStoreRejectsMalformedPrefix(t *testing.T) {
	root := t.TempDir()
	store := newArchiveProviderObjectStore(archive.NewLocalArchive(root), root)
	require.NotNil(t, store)

	for _, prefix := range []string{
		"inst:147/2025-03-01",      // 缺 generation 段
		"inst:147/2025-03-01/g",    // generation 段为空
		"inst:147/2025-03-01/g0",   // generation 必须 > 0
		"inst:147/2025-03-01/g1/x", // 多一段
		"inst:147/2025-3-1/g1",     // 日期不是 YYYY-MM-DD
		"/inst:147/2025-03-01/g1/", // 前后斜杠
		"",                         // 空
	} {
		if _, err := store.ListArchiveObjects(context.Background(), archive.KindLocal, prefix); err == nil {
			t.Errorf("前缀 %q 应被拒绝（不得猜一个位置去删）", prefix)
		}
	}

	// 反向对照：合法前缀必须能通过（否则上面的断言恒真、等于没测）。
	if _, err := store.ListArchiveObjects(context.Background(), archive.KindLocal, "inst:147/2025-03-01/g1"); err != nil {
		t.Fatalf("合法前缀不得被拒：%v", err)
	}
}

// 契约：provider kind 不一致 ⇒ **拒绝删除**。
//
// 证据可能来自上一次部署（先本地目录、后来换了目的地）。按记录里的 kind 去删
// 当前装配的 Provider，等于在一个毫不相干的目录里按同名前缀删对象。
func TestArchiveProviderObjectStoreRejectsProviderKindMismatch(t *testing.T) {
	root := t.TempDir()
	store := newArchiveProviderObjectStore(archive.NewLocalArchive(root), root)
	require.NotNil(t, store)

	err := store.DeleteArchiveObject(context.Background(), archive.KindS3, "inst:147/2025-03-01/g1", "raw/files/a.gz")
	require.Error(t, err, "kind 不一致必须拒绝删除")
	require.Contains(t, err.Error(), "不一致")

	// 反方向：kind 为空时不据此拒绝（历史记录可能没写 kind，交由前缀与证据校验兜住）。
	require.NoError(t, store.DeleteArchiveObject(context.Background(), "", "inst:147/2025-03-01/g1", "raw/files/a.gz"))
}

// 契约：空目录清理只删本分区的 g<gen> 目录，**不碰** root 与其它日期。
//
// 一次 RemoveAll(root) 就等于把整个归档史删了；这条用例把它钉死。
func TestArchiveProviderObjectStoreRemovesOnlyItsPartition(t *testing.T) {
	root := t.TempDir()
	provider := archive.NewLocalArchive(root)
	key := archive.PartitionKey{StorageNamespace: "inst:147", UTCDay: "2025-03-01", Generation: 1}
	other := archive.PartitionKey{StorageNamespace: "inst:147", UTCDay: "2025-03-02", Generation: 1}

	require.NoError(t, provider.Put(context.Background(), other, "raw/files/keep.gz", []byte("K")))
	require.NoError(t, provider.Put(context.Background(), key, "raw/files/gone.gz", []byte("G")))

	store := newArchiveProviderObjectStore(provider, root)
	require.NotNil(t, store)
	// 先删掉本分区的对象，再清空目录（与执行器次序一致）。
	require.NoError(t, store.DeleteArchiveObject(context.Background(), archive.KindLocal,
		"inst:147/2025-03-01/g1", "raw/files/gone.gz"))
	require.NoError(t, store.RemoveArchivePartition(context.Background(), archive.KindLocal, "inst:147/2025-03-01/g1"))

	kept, err := provider.List(context.Background(), other)
	require.NoError(t, err)
	require.Equal(t, []string{"raw/files/keep.gz"}, kept, "其它日期的对象必须原样保留")

	gone, err := provider.List(context.Background(), key)
	require.NoError(t, err)
	require.Empty(t, gone)
}
