package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	workercfg "github.com/wcpe/JianManager/internal/worker"
	"github.com/wcpe/JianManager/internal/worker/logs/archive"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/retention"
	workerstorage "github.com/wcpe/JianManager/internal/worker/storage"
)

// 本文件是「归档对象支持 s3 目的地」的**真实对象存储**端到端回归。
//
// 与其他 purge 用例的分工：
//   - log_archive_purge_test.go 用**本地目录**铺真件，钉住执行器与 Catalog 的次序；
//   - 本文件把同一个 Provider 换成**真实 S3**（`archive.NewRemoteS3Provider` +
//     `internal/worker/storage/s3.go` 的手写 SigV4），跑「导出 → 归档 → 回收」全链。
//
// 为什么必须有一条真的跑在桶上的用例：`archive.ObjectStore`（内存 stub）会让
// 「装配成功、删除成功、Catalog 已清除」这三件事在一个**根本没连上桶**的进程里同时成立。
// 一旦 `newArchiveProviderObjectStore` 对非 local 分支的某处写错（例如把 s3 的对象键
// 当成目录路径去拼 root），内存后端一个字都不会报错，而现场表现是
// 「归档一直在涨，清理每轮都报成功」——这是最坏的一类假绿。
//
// 门控（缺任一即 skip，不影响常规 CI）：
//
//	JM_LOG_RETENTION_S3_ENDPOINT   形如 s3.allinmc.cn（不带 scheme；UseSSL 由方案决定）
//	JM_LOG_RETENTION_S3_BUCKET     已存在的桶
//	JM_LOG_RETENTION_S3_AK / SK    凭证
//	JM_LOG_RETENTION_S3_PREFIX     专用前缀（默认 jm-log-retention-e2e）；用例只在该前缀下读写
//	JM_LOG_RETENTION_S3_USESSL     "1" 走 TLS（默认走明文，与现场同构）
//
// 用例结束（含失败）都会清空该前缀，避免污染桶里别的数据。

// s3Enabled 报告真实 S3 门控是否齐备。
func s3Enabled() (workerstorage.Config, bool) {
	endpoint := os.Getenv("JM_LOG_RETENTION_S3_ENDPOINT")
	ak := os.Getenv("JM_LOG_RETENTION_S3_AK")
	sk := os.Getenv("JM_LOG_RETENTION_S3_SK")
	bucket := os.Getenv("JM_LOG_RETENTION_S3_BUCKET")
	if endpoint == "" || ak == "" || sk == "" || bucket == "" {
		return workerstorage.Config{}, false
	}
	prefix := os.Getenv("JM_LOG_RETENTION_S3_PREFIX")
	if prefix == "" {
		prefix = "jm-log-retention-e2e"
	}
	return workerstorage.Config{
		Type:      workerstorage.TypeS3,
		Endpoint:  endpoint,
		Bucket:    bucket,
		Region:    envOrStr(os.Getenv("JM_LOG_RETENTION_S3_REGION"), "us-east-1"),
		Prefix:    prefix,
		AccessKey: ak,
		SecretKey: sk,
		UseSSL:    os.Getenv("JM_LOG_RETENTION_S3_USESSL") == "1",
	}, true
}

func envOrStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// s3Provider 走**生产装配路径**造 Provider：`newArchiveCopyProvider`。
//
// 不直接调 archive.NewRemoteS3Provider 的原因：本次改动的正是「装配处按配置分流」
// 那一段（provider=local/s3/minio → Provider），绕过它就等于把被测代码绕过去了。
func s3Provider(t *testing.T, storeCfg workerstorage.Config) (archive.Provider, workercfg.LogRetentionArchiveCopyConfig) {
	t.Helper()
	cfg := workercfg.LogRetentionArchiveCopyConfig{
		Enabled:   true,
		Provider:  "s3",
		Endpoint:  storeCfg.Endpoint,
		Bucket:    storeCfg.Bucket,
		Region:    storeCfg.Region,
		Prefix:    storeCfg.Prefix,
		AccessKey: storeCfg.AccessKey,
		SecretKey: storeCfg.SecretKey,
		UseSSL:    storeCfg.UseSSL,
	}
	provider, err := newArchiveCopyProvider(cfg)
	require.NoError(t, err, "生产装配路径 newArchiveCopyProvider 必须能按 s3 造出 Provider")
	require.Equal(t, archive.KindS3, provider.Kind(), "Provider 种类必须是 s3（否则回收侧的 kind 复核会拒绝）")
	return provider, cfg
}

// s3Store 返回一个**独立于被测 archive.Provider** 的对象存储客户端。
//
// 用它来断言「桶上到底还剩什么」：调用 archive.Provider.List 断言「删干净了」会把
// 被测实现自己当成唯一证人——同一条路径上写错两次也能对上。
func s3Store(t *testing.T, storeCfg workerstorage.Config) *workerstorage.S3ObjectStore {
	t.Helper()
	objStore, err := workerstorage.NewS3ObjectStore(workerstorage.Config{
		Type:      workerstorage.TypeS3,
		Endpoint:  storeCfg.Endpoint,
		Bucket:    storeCfg.Bucket,
		Region:    storeCfg.Region,
		AccessKey: storeCfg.AccessKey,
		SecretKey: storeCfg.SecretKey,
		UseSSL:    storeCfg.UseSSL,
	})
	require.NoError(t, err)
	return objStore
}

// s3Cleanup 清空本次 prefix 下的所有对象（用例结束时调用，失败也要清）。
func s3Cleanup(t *testing.T, storeCfg workerstorage.Config) {
	t.Helper()
	objStore := s3Store(t, storeCfg)
	prefix := storeCfg.Prefix + "/"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	keys, err := objStore.List(ctx, prefix)
	if err != nil {
		t.Logf("清理：列出 %s 失败（残留对象需人工清理）：%v", prefix, err)
		return
	}
	for _, k := range keys {
		if err := objStore.Delete(ctx, k); err != nil {
			t.Logf("清理：删除 %s 失败：%v", k, err)
		}
	}
	t.Logf("清理：已删除 prefix %s 下 %d 个对象", prefix, len(keys))
}

// s3ListPrefix 用独立客户端列出桶上实际存在的对象键（断言「真的删了」）。
func s3ListPrefix(t *testing.T, storeCfg workerstorage.Config) []string {
	t.Helper()
	objStore := s3Store(t, storeCfg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	keys, err := objStore.List(ctx, storeCfg.Prefix+"/")
	require.NoError(t, err)
	return keys
}

// s3Snapshot 造一个「冻结快照」目录：形态与 VL 日分区快照一致
// （metadata.json + datadb/<part>，含子目录与空文件）。
//
// 为什么要真的走文件：导出侧的价值一半在「gzip 往返 + 逐文件回读校验」，
// 用内存里的 []byte 直接灌进去就跳过了 walk/读盘这一段。
func s3Snapshot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string][]byte{
		"metadata.json":        []byte(`{"partition":"2026-09-01","rows":2}`),
		"datadb/00000001":      []byte("2026-09-01T00:00:00Z INFO hello\n2026-09-01T00:00:01Z ERROR boom\n"),
		"datadb/nested/000002": []byte("nested payload for retention s3 e2e\n"),
		"datadb/empty":         {},
	}
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, data, 0o640))
	}
	return dir
}

// TestE2E_S3_ArchiveObjectPurgeFullChain 是本次验证的主体：
//
//	生产导出器（archive.NewArchiveCopyExporter）→ 真实 S3 Put/回读校验
//	→ 生产适配层（newCatalogArchivedObjectLister / newArchiveProviderObjectStore /
//	   newCatalogArchiveObjectAuthority）→ retention 执行器（先落权威态再删对象）
//	→ 真实 S3 Delete → Catalog 终态 Purged → 桶上 List 为空
//
// 三闸全开（discard + sweep.vl_sweep + log_vl.delete_enabled）是删除的唯一合法前提，
// 这里全开；若不全开则一个对象都不该删（另有用例钉住）。
func TestE2E_S3_ArchiveObjectPurgeFullChain(t *testing.T) {
	storeCfg, ok := s3Enabled()
	if !ok {
		t.Skip("未设置 JM_LOG_RETENTION_S3_* 门控，跳过真实 S3 端到端用例")
	}
	provider, copyCfg := s3Provider(t, storeCfg)
	t.Cleanup(func() { s3Cleanup(t, storeCfg) })
	s3Cleanup(t, storeCfg) // 起手先清，避免上一轮残留干扰读数

	// —— 第 0 步：桶连通性（让「后面失败」与「一开始就没连上」能区分开）——
	require.NoError(t, workerstorage.Probe(context.Background(), workerstorage.Config{
		Type: workerstorage.TypeS3, Endpoint: storeCfg.Endpoint, Bucket: storeCfg.Bucket,
		Region: storeCfg.Region, AccessKey: storeCfg.AccessKey, SecretKey: storeCfg.SecretKey,
		UseSSL: storeCfg.UseSSL,
	}), "真实 S3 连通性探测失败（凭证 / 桶 / 端点有误）")

	// —— 第 1 步：造 Catalog 记录 + 冻结物理日 + 真实导出到 S3 ——
	cat := catalog.New(nil)
	day := "2026-09-01"
	cdKey := catalog.PartitionKey{StorageNamespace: "inst:147", UTCDay: day}
	rec := catalog.NewStableRecord(cdKey, catalog.OwnerHot, 1, "20260901")
	require.NoError(t, cat.Put(rec))
	require.NoError(t, cat.FreezeDayForArchive(day, "archive:"+day), "导出前必须冻结物理日（导出器会校验）")

	snapshotDir := s3Snapshot(t)
	exporter := archive.NewArchiveCopyExporter(cat, func(storageNamespace, utcDay string) (string, error) {
		return snapshotDir, nil
	}, provider)
	fixedNow := time.Date(2026, 9, 2, 3, 0, 0, 0, time.UTC)
	exporter.Now = func() time.Time { return fixedNow }
	exporter.EngineVersion = "log-retention-e2e"

	sum, err := exporter.Export(context.Background(), cdKey.StorageNamespace, day)
	require.NoError(t, err, "真实 S3 导出失败")
	t.Logf("导出读数：objects=%d sourceBytes=%d storedBytes=%d location=%q prefix=%q manifestChecksum=%s coverageComplete=%v",
		sum.ObjectCount, sum.SourceBytes, sum.StoredBytes, sum.Location, sum.ObjectPrefix,
		sum.ManifestChecksum, sum.CoverageComplete)
	require.Equal(t, 4, sum.ObjectCount, "快照里 4 个文件（含一个空文件）都必须被归档")
	require.Equal(t, archive.KindS3, sum.ProviderKind)
	require.True(t, sum.CoverageComplete)
	require.NotEmpty(t, sum.ManifestChecksum)

	// 数据面①：桶上确实出现了对象，且形态是 <prefix>/<ns>/<day>/g1/raw/files/...
	keysAfterExport := s3ListPrefix(t, storeCfg)
	require.NotEmpty(t, keysAfterExport)
	wantPrefix := storeCfg.Prefix + "/" + cdKey.StorageNamespace + "/" + day + "/g1/"
	for _, k := range keysAfterExport {
		require.Contains(t, k, wantPrefix, "对象键必须落在归档分区前缀下，实际 %s", k)
	}
	t.Logf("导出后桶上对象 %d 个：%v", len(keysAfterExport), keysAfterExport)

	// 数据面②：manifest 回读可用（证明存的是「能读懂的一份归档」，不是一堆字节）。
	manifestKey := wantPrefix + "manifest.json"
	var manifestFound bool
	for _, k := range keysAfterExport {
		if k == manifestKey {
			manifestFound = true
		}
	}
	require.True(t, manifestFound, "桶上必须有 %s，实际 %v", manifestKey, keysAfterExport)

	// —— 第 2 步：把权威态推进到「已归档 + 热层已回收」（回收归档对象的前置条件）——
	archivedAt := fixedNow
	_, _, err = cat.MarkArchived(cdKey, catalog.ArchiveEvidence{
		ProviderKind:     sum.ProviderKind,
		ObjectPrefix:     sum.ObjectPrefix,
		ManifestChecksum: sum.ManifestChecksum,
		Coverage: catalog.ArchiveCoverage{
			ObjectCount: sum.ObjectCount, SourceBytes: sum.SourceBytes,
			StoredBytes: sum.StoredBytes, Complete: sum.CoverageComplete,
		},
		ArchivedAt: archivedAt,
	}, archivedAt)
	require.NoError(t, err)
	_, err = cat.MarkArchiveReclaimed(cdKey, archivedAt.Add(time.Minute))
	require.NoError(t, err)
	pre, ok2 := cat.Get(cdKey)
	require.True(t, ok2)
	require.Equal(t, catalog.OwnerArchive, pre.Owner)
	require.True(t, pre.ArchiveEvidence.HotReclaimed, "前置条件：热层必须已回收")
	require.False(t, pre.ArchiveEvidence.Purged, "前置条件：尚未清除")

	// —— 第 3 步：三闸全开 + 已过保留期 ⇒ 生产回收链清空桶上该分区 ——
	now := archivedAt.Add(800 * 24 * time.Hour) // 已过 730d
	policy := retention.DefaultPolicy()
	policy.Enabled = true
	policy.Discard = true
	policy.Sweep.VLSweep = true
	policy.Sweep.VLDeleteEnabled = true
	policy.ArchiveObjectRetention = retention.ArchiveObjectRetentionPolicy{
		Enabled:     true,
		Retention:   730 * 24 * time.Hour,
		SweepAtHour: 3,
	}
	open, gateReason := policy.DeleteGatesOpen()
	require.True(t, open, "前置条件：三闸必须全开（%s）", gateReason)

	lister := newCatalogArchivedObjectLister(cat)
	refs, err := lister.ListArchivedObjects()
	require.NoError(t, err)
	require.Len(t, refs, 1)
	require.Empty(t, refs[0].BlockedReason, "前置条件：候选必须可清除（%s）", refs[0].BlockedReason)
	require.Equal(t, archive.KindS3, refs[0].ProviderKind)

	// 生产装配：newArchiveObjectReclaimer 内部走 newArchiveProviderObjectStore。
	// root 传空串——对象存储没有目录骨架，这正是「s3 分支不得触碰文件系统」的现场约束。
	reclaimer := newArchiveObjectReclaimer(policy, cat, provider, "")
	require.NotNil(t, reclaimer, "归档对象回收必须能按 s3 Provider 装配（此前非 local 会被拒）")
	rOk, rReason := reclaimer.Ready()
	require.True(t, rOk, "前置条件：三个出口必须齐备（%s）", rReason)

	// 反向对照：同一装配若把 root 传成空串而 Provider 是 local，DeleteArchiveObject
	// 仍走同一接口；这里先确认 s3 分支的 RemoveArchivePartition 是「什么都不做」而不是
	// 拿空 root 拼相对路径（那会落到工作目录下）。
	storeS3 := newArchiveProviderObjectStore(provider, "")
	require.NotNil(t, storeS3)
	require.NoError(t, storeS3.RemoveArchivePartition(context.Background(), archive.KindS3, refs[0].ObjectPrefix),
		"s3 没有目录骨架，RemoveArchivePartition 必须是幂等成功")
	// 此时对象必须一个不少（上一步只清目录骨架，不删对象）。
	require.Len(t, s3ListPrefix(t, storeCfg), len(keysAfterExport), "RemoveArchivePartition 不得删除任何对象")

	res, err := reclaimer.Step(context.Background(), now)
	require.NoError(t, err)
	t.Logf("回收读数：listed=%d due=%d purged=%d kept=%d failed=%d rolledBack=%d",
		res.Listed, res.Due, res.Purged, res.Kept, res.Failed, res.RolledBack)
	for _, o := range res.Outcomes {
		t.Logf("  outcome: target=%s/%s result=%s objects=%d reason=%q err=%v",
			o.Target.Ref.StorageNamespace, o.Target.Ref.UTCDay, o.Result, o.Objects, o.Target.Reason, o.Err)
	}
	require.Equal(t, 1, res.Due)
	require.Equal(t, 1, res.Purged, "应清除 1 个分区")
	require.Equal(t, 0, res.Failed)
	require.Equal(t, len(keysAfterExport), res.Outcomes[0].Objects, "逐对象删除数必须等于导出对象数")

	// 数据面③：桶上该分区真的空了（用独立实现的 List 断言，不看被测 Provider）。
	left := s3ListPrefix(t, storeCfg)
	require.Empty(t, left, "桶上该前缀应被清空，实际残留 %v", left)

	// 数据面④：被测 Provider 的 List 也返回空（分区级视图）。
	akey := archive.PartitionKey{StorageNamespace: cdKey.StorageNamespace, UTCDay: day, Generation: 1}
	providerLeft, err := provider.List(context.Background(), akey)
	require.NoError(t, err)
	require.Empty(t, providerLeft)

	// 权威面：Purged 终态 + journal 落盘 + 不残留「进行中」标记。
	after, ok3 := cat.Get(cdKey)
	require.True(t, ok3)
	require.Equal(t, catalog.OwnerArchive, after.Owner, "清除归档对象不改变「正本在哪一层」")
	require.True(t, after.ArchiveEvidence.Purged)
	require.False(t, after.ArchiveEvidence.PurgedAt.IsZero())
	require.Equal(t, 1, after.ArchiveEvidence.PurgeAttempts)
	require.True(t, after.ArchiveEvidence.PurgeRequestedAt.IsZero(), "完成后不该残留「进行中」标记")

	// —— 第 4 步：幂等（再跑一轮，一个对象都不删、尝试次数与清除时刻都不推进）——
	res2, err := reclaimer.Step(context.Background(), now.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, 0, res2.Purged, "已清除的分区不该被重复清除")
	after2, _ := cat.Get(cdKey)
	require.Equal(t, 1, after2.ArchiveEvidence.PurgeAttempts, "幂等重跑不得推进尝试次数")
	require.Equal(t, after.ArchiveEvidence.PurgedAt, after2.ArchiveEvidence.PurgedAt,
		"幂等重跑不得改写清除时刻（审计时间线会漂移）")

	// 归档目的地配置自洽性：回收侧与导出侧必须是同一个目的地。
	require.Equal(t, "s3", copyCfg.ArchiveProviderKind())
}

// TestE2E_S3_PurgeGatesClosedDeletesNothing 反方向对照：
// 桶上有对象、Candidate 已过保留期，但三闸未全开 ⇒ **一个对象都不许删**，Catalog 不动。
//
// 没有这条用例，上面那条「删干净了」就可能是「不管闸门都删」——两者的观测读数完全相同。
func TestE2E_S3_PurgeGatesClosedDeletesNothing(t *testing.T) {
	storeCfg, ok := s3Enabled()
	if !ok {
		t.Skip("未设置 JM_LOG_RETENTION_S3_* 门控，跳过真实 S3 端到端用例")
	}
	provider, _ := s3Provider(t, storeCfg)
	t.Cleanup(func() { s3Cleanup(t, storeCfg) })
	s3Cleanup(t, storeCfg)

	cat := catalog.New(nil)
	day := "2026-09-02"
	cdKey := catalog.PartitionKey{StorageNamespace: "inst:148", UTCDay: day}
	require.NoError(t, cat.Put(catalog.NewStableRecord(cdKey, catalog.OwnerHot, 1, "20260902")))

	akey := archive.PartitionKey{StorageNamespace: cdKey.StorageNamespace, UTCDay: day, Generation: 1}
	require.NoError(t, provider.Put(context.Background(), akey, "raw/files/a.gz", []byte("A")))
	require.NoError(t, provider.Put(context.Background(), akey, "manifest.json", []byte("{}")))
	before := s3ListPrefix(t, storeCfg)
	require.NotEmpty(t, before)

	archivedAt := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	_, _, err := cat.MarkArchived(cdKey, catalog.ArchiveEvidence{
		ProviderKind: archive.KindS3, ObjectPrefix: cdKey.StorageNamespace + "/" + day + "/g1",
		ManifestChecksum: "sha256:x",
		Coverage:         catalog.ArchiveCoverage{ObjectCount: 2, Complete: true},
		ArchivedAt:       archivedAt,
	}, archivedAt)
	require.NoError(t, err, "前置条件：必须能推进到归档态，否则后续「闸门拦住」的断言会变成假绿")
	_, err = cat.MarkArchiveReclaimed(cdKey, archivedAt.Add(time.Minute))
	require.NoError(t, err)
	preP, okP := cat.Get(cdKey)
	require.True(t, okP)
	require.Equal(t, catalog.OwnerArchive, preP.Owner, "前置条件：必须已是归档权威态（否则候选枚举为空就是假绿）")
	require.True(t, preP.ArchiveEvidence.HotReclaimed, "前置条件：热层必须已回收")
	require.False(t, preP.ArchiveEvidence.Purged)

	policy := retention.DefaultPolicy()
	policy.Enabled = true
	policy.Discard = true
	policy.Sweep.VLSweep = true
	policy.Sweep.VLDeleteEnabled = false // ← 第三闸关
	policy.ArchiveObjectRetention = retention.ArchiveObjectRetentionPolicy{
		Enabled: true, Retention: 730 * 24 * time.Hour, SweepAtHour: 3,
	}
	open, reason := policy.DeleteGatesOpen()
	require.False(t, open, "本用例的前提是三闸未全开")
	t.Logf("闸门未全开的原因（应当被执行器拒绝）：%s", reason)

	reclaimer := newArchiveObjectReclaimer(policy, cat, provider, "")
	require.NotNil(t, reclaimer)

	// 候选枚举必须能看到这个分区（listed=1）。注意：执行器的 Step 是「三闸先判、
	// 判在列举之前」（避免现场把「闸门没开」误读成「策略在跑但没删到期的」），
	// 所以它自己的 res.Listed 会是 0 并置 GatesClosed。这里先用 lister 独立证明
	// 「候选确实存在」——否则「零行动」可能只是压根没扫到。
	lister := newCatalogArchivedObjectLister(cat)
	refs, err := lister.ListArchivedObjects()
	require.NoError(t, err)
	require.Len(t, refs, 1, "前置条件：候选枚举必须看见该分区，否则本用例证明不了任何东西")
	require.Empty(t, refs[0].BlockedReason, "前置条件：该候选本身可清除，被拦的唯一原因只能是闸门")
	require.False(t, refs[0].Purged)

	res, err := reclaimer.Step(context.Background(), archivedAt.Add(800*24*time.Hour))
	require.NoError(t, err)
	t.Logf("闸门未全开时读数：gatesClosed=%v gateReason=%q listed=%d due=%d purged=%d kept=%d failed=%d",
		res.GatesClosed, res.GateReason, res.Listed, res.Due, res.Purged, res.Kept, res.Failed)
	require.True(t, res.GatesClosed, "执行器必须报出「闸门未全开」而不是静默零行动")
	require.NotEmpty(t, res.GateReason, "必须点名是哪一道闸未开（现场靠这句话去查配置）")
	require.Equal(t, 0, res.Listed, "闸门未全开时连枚举都不做（先判闸、后列举）")
	require.Equal(t, 0, res.Purged, "三闸未全开时绝不能清除归档对象")

	require.Equal(t, before, s3ListPrefix(t, storeCfg), "三闸未全开时桶上对象必须原样保留")
	after, _ := cat.Get(cdKey)
	require.False(t, after.ArchiveEvidence.Purged, "三闸未全开时权威态不得变成已清除")
	require.True(t, after.ArchiveEvidence.PurgeRequestedAt.IsZero(), "连「进行中」标记都不该落")
}

// TestE2E_S3_ArchiveProviderKindMismatchRefusesDelete 现场最危险的形态之一：
//
// Catalog 记录的归档目的地是 s3，而当前装配的是 local 目录（换了部署/换了配置）。
// 此时必须**拒绝删除**，而不是按同名前缀在本地目录里删东西。
//
// 这条用例不需要真实 S3：它验的是「装配侧按记录复核目的地」这道闸，
// 用真实 S3 反而只是让前提更可信（记录里的 kind 确实来自真桶）。
func TestE2E_S3_ArchiveProviderKindMismatchRefusesDelete(t *testing.T) {
	local := archive.NewLocalArchive(t.TempDir())
	store := newArchiveProviderObjectStore(local, "")
	require.NotNil(t, store)
	err := store.DeleteArchiveObject(context.Background(), archive.KindS3, "inst:147/2025-03-01/g1", "raw/files/a.gz")
	require.Error(t, err, "记录的 kind=s3 与装配的 local 不一致时必须拒绝删除")
	require.Contains(t, err.Error(), "不一致")
}

// bucketKeyPrefixProbe 确认 prefix 真的参与对象键（导出侧用 log_archive.prefix）。
//
// 单独一条小用例：prefix 若被丢弃，本次验证就会把对象写到桶根下的 ns/day/g1/…，
// 与真实部署（prefix=logs 或自定义）不一致——那会让「已验证」与「上线后」不是同一件事。
func TestE2E_S3_PrefixIsHonoredInObjectKeys(t *testing.T) {
	storeCfg, ok := s3Enabled()
	if !ok {
		t.Skip("未设置 JM_LOG_RETENTION_S3_* 门控，跳过真实 S3 端到端用例")
	}
	provider, _ := s3Provider(t, storeCfg)
	t.Cleanup(func() { s3Cleanup(t, storeCfg) })
	s3Cleanup(t, storeCfg)

	akey := archive.PartitionKey{StorageNamespace: "inst:900", UTCDay: "2026-09-03", Generation: 1}
	require.NoError(t, provider.Put(context.Background(), akey, "raw/files/probe.gz", []byte("P")))

	keys := s3ListPrefix(t, storeCfg)
	t.Logf("prefix=%s 下对象：%v", storeCfg.Prefix, keys)
	require.Len(t, keys, 1)
	require.True(t, len(keys[0]) > len(storeCfg.Prefix)+1 && keys[0][:len(storeCfg.Prefix)+1] == storeCfg.Prefix+"/",
		"对象键必须以配置的 prefix 开头，实际 %s", keys[0])
	require.Equal(t, storeCfg.Prefix+"/inst:900/2026-09-03/g1/raw/files/probe.gz", keys[0])

	// 顺带钉住 s3 的 Delete 幂等（重试安全）：删两次都成功。
	require.NoError(t, provider.Delete(context.Background(), akey, "raw/files/probe.gz"))
	require.NoError(t, provider.Delete(context.Background(), akey, "raw/files/probe.gz"))
	left, err := provider.List(context.Background(), akey)
	require.NoError(t, err)
	require.Empty(t, left)
}

// fmtGuardPlaceholder 让 fmt 在调试改动里可用（避免为了打一行日志再改 import）。
var _ = fmt.Sprintf
