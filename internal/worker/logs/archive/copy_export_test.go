package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件守导出侧的两条硬性质：
//
//  ① **往返一致性**：gzip 压缩后的归档对象必须能解压还原成源文件的逐字节内容；
//  ② **不通过就报错**：任何一个对象/ manifest 回读校验不通过，Export 必须返回错误——
//     执行器把「没有错误」当作「副本可信、可以回收热层」的唯一前提。
//
// 这里刻意直接测 exportTree（而不是 Export）：后者要真实 VL 的冻结快照与 HTTP 交互，
// 而本文件关心的是「压缩与校验本身对不对」，那是纯文件操作。

// writeSnapshot 造一棵形如 VL 日分区快照的目录树（含子目录与一个空文件）。
func writeSnapshot(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		"metadata.json":        []byte(`{"partition":"20260901"}`),
		"datadb/00000001":      bytes.Repeat([]byte("log-line-0123456789\n"), 512),
		"datadb/00000002":      bytes.Repeat([]byte("another-part\n"), 300),
		"datadb/nested/000003": []byte("nested payload"),
		// 空文件是最容易被「压缩 + 校验」这段代码弄丢的边界：
		// 压缩后非空、解压后必须恰好 0 字节。
		"datadb/empty": {},
	}
	for rel, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("创建目录失败：%v", err)
		}
		if err := os.WriteFile(path, data, 0o640); err != nil {
			t.Fatalf("写入夹具失败：%v", err)
		}
	}
	return files
}

// newTestExporter 返回一个只用于 exportTree 的导出器（不需要 Physical）。
func newTestExporter(provider Provider) *ArchiveCopyExporter {
	return &ArchiveCopyExporter{
		Provider: provider,
		Now:      func() time.Time { return time.Date(2026, 10, 2, 3, 0, 0, 0, time.UTC) },
	}
}

// TestExportTreeGzipRoundTrip 钉住「压缩不能改变内容」。
func TestExportTreeGzipRoundTrip(t *testing.T) {
	srcDir := t.TempDir()
	files := writeSnapshot(t, srcDir)

	provider := NewMemoryProvider()
	x := newTestExporter(provider)
	key := PartitionKey{StorageNamespace: "inst:147", UTCDay: "2026-09-01", Generation: 1}

	sum, err := x.exportTree(context.Background(), key, srcDir)
	if err != nil {
		t.Fatalf("导出失败：%v", err)
	}
	if sum.ObjectCount != len(files) {
		t.Fatalf("对象数应为 %d，得到 %d", len(files), sum.ObjectCount)
	}

	// 逐个对象解压还原并与源字节比对。
	for rel, want := range files {
		objRel := "raw/files/" + rel + ".gz"
		stored, err := provider.Get(context.Background(), key, objRel)
		if err != nil {
			t.Fatalf("读取归档对象 %s 失败：%v", objRel, err)
		}
		zr, err := gzip.NewReader(bytes.NewReader(stored))
		if err != nil {
			t.Fatalf("%s 不是合法 gzip 流：%v", objRel, err)
		}
		got, err := io.ReadAll(zr)
		if closeErr := zr.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatalf("解压 %s 失败：%v", objRel, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s 解压后内容与源不一致（源 %d 字节，还原 %d 字节）", objRel, len(want), len(got))
		}
	}

	// 压缩确实省了空间（源是高度重复的文本，这条断言在这里是可信的）。
	if sum.StoredBytes >= sum.SourceBytes {
		t.Errorf("归档侧应小于源侧：source=%d stored=%d", sum.SourceBytes, sum.StoredBytes)
	}
}

// TestExportTreeManifestFollowsArchiveContract 钉住 manifest 的**契约字段**。
//
// 复用 `logs/archive` 的 Manifest 是本次实现的硬要求：字段少一个，将来用同一套
// Rehydrate/查询实现读这份副本时就会缺信息。故这里逐项断言，而不只看「能解析」。
func TestExportTreeManifestFollowsArchiveContract(t *testing.T) {
	srcDir := t.TempDir()
	files := writeSnapshot(t, srcDir)

	provider := NewMemoryProvider()
	x := newTestExporter(provider)
	x.ParserVersion = "worker-log-parser/v1"
	key := PartitionKey{StorageNamespace: "inst:147", UTCDay: "2026-09-01", Generation: 7}

	sum, err := x.exportTree(context.Background(), key, srcDir)
	if err != nil {
		t.Fatalf("导出失败：%v", err)
	}
	if sum.Location == "" {
		t.Error("必须给出归档位置（审计「去哪」）")
	}

	stored, err := provider.Get(context.Background(), key, ManifestRelPath)
	if err != nil {
		t.Fatalf("读取 manifest 失败：%v", err)
	}
	m, err := UnmarshalManifest(stored)
	if err != nil {
		t.Fatalf("manifest 不符合 archive 契约：%v", err)
	}
	if m.SchemaVersion != SchemaVersionV1 {
		t.Errorf("schema_version 应为 %q，得到 %q", SchemaVersionV1, m.SchemaVersion)
	}
	if m.Generation != key.Generation {
		t.Errorf("manifest generation 应随 Catalog generation：期望 %d 得到 %d", key.Generation, m.Generation)
	}
	if !m.Coverage.Complete || m.Coverage.EnumerationState != EnumerationExhausted {
		t.Errorf("完整的一日导出必须标记为已封存：complete=%v state=%q",
			m.Coverage.Complete, m.Coverage.EnumerationState)
	}
	if len(m.RawFiles) != len(files) {
		t.Fatalf("raw_files 应有 %d 项，得到 %d", len(files), len(m.RawFiles))
	}
	// 每个对象都要有内容身份 + 校验和（manifest.Validate 已保证，这里再逐项核对语义）。
	for _, rf := range m.RawFiles {
		if rf.State != ObjectStateManaged {
			t.Errorf("%s 的状态应为受管：%q", rf.RelPath, rf.State)
		}
		if rf.ObjectID != ObjectIDFor(rf.ContentSHA256, rf.SizeBytes) {
			t.Errorf("%s 的 object_id 与内容身份不一致", rf.RelPath)
		}
		if sum, ok := m.Checksums.Objects[rf.RelPath]; !ok || sum != rf.ContentSHA256 {
			t.Errorf("%s 的 checksums 与 raw_files 不一致", rf.RelPath)
		}
		if rf.SourcePath == "" {
			t.Errorf("%s 缺少 source_path（无法追溯到快照内哪个文件）", rf.RelPath)
		}
	}
	// byte_count 是**归档侧**字节数（压缩后），与 stored 摘要一致。
	if m.Coverage.ByteCount != uint64(sum.StoredBytes) {
		t.Errorf("coverage.byte_count 应为归档侧字节数 %d，得到 %d", sum.StoredBytes, m.Coverage.ByteCount)
	}
	if m.TimeRange.FromUTC == "" || m.TimeRange.ToUTC == "" {
		t.Error("日分区导出必须登记时间范围")
	}
	// —— 归档证据（供 Catalog 的 ARCHIVED 权威态使用）——
	// 这四项缺一，Catalog 侧的证据校验就会拒绝回收热层（宁可不删，不可少一项）。
	if sum.ProviderKind == "" {
		t.Error("必须给出 provider kind（证据①：归档目的地类型）")
	}
	if sum.ObjectPrefix != key.String() {
		t.Errorf("object prefix 应为归档分区键 %q，得到 %q", key.String(), sum.ObjectPrefix)
	}
	if sum.ManifestChecksum == "" {
		t.Error("必须给出 manifest 校验和（证据②）")
	}
	if sum.ManifestChecksum != ContentHash(stored) {
		t.Error("manifest 校验和必须是**回读字节**的 sha256（读取方据此确认索引未被改动）")
	}
	if !sum.CoverageComplete {
		t.Error("完整导出的 coverage 必须为 complete（证据③：覆盖范围）")
	}
	if sum.ObjectCount != len(files) {
		t.Errorf("对象数应为 %d，得到 %d", len(files), sum.ObjectCount)
	}
}

// TestExportTreeIsIdempotent 钉住「重跑导出不改变结果」。
//
// 冻结快照的内容不会变，故同一分区的重复导出必须产出同样的对象与同样的字节。
// 若非如此，「导出成功但进程在写 manifest 前退出」将无法自愈。
func TestExportTreeIsIdempotent(t *testing.T) {
	srcDir := t.TempDir()
	writeSnapshot(t, srcDir)

	provider := NewMemoryProvider()
	key := PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-01", Generation: 1}
	ctx := context.Background()

	first, err := newTestExporter(provider).exportTree(ctx, key, srcDir)
	if err != nil {
		t.Fatalf("首轮导出失败：%v", err)
	}
	before, err := provider.Get(ctx, key, ManifestRelPath)
	if err != nil {
		t.Fatalf("读取首轮 manifest 失败：%v", err)
	}
	second, err := newTestExporter(provider).exportTree(ctx, key, srcDir)
	if err != nil {
		t.Fatalf("重跑导出失败：%v", err)
	}
	after, err := provider.Get(ctx, key, ManifestRelPath)
	if err != nil {
		t.Fatalf("读取重跑后的 manifest 失败：%v", err)
	}
	if first.ObjectCount != second.ObjectCount || first.StoredBytes != second.StoredBytes {
		t.Errorf("重跑结果不一致：%+v vs %+v", first, second)
	}
	if !bytes.Equal(before, after) {
		t.Error("重跑导出必须产出逐字节相同的 manifest")
	}
}

// brokenProvider 让指定键的 Get 返回被篡改的字节，用来验证「回读校验」真的在跑。
type brokenProvider struct {
	Provider
	tamperRel string
}

func (b *brokenProvider) Get(ctx context.Context, key PartitionKey, relPath string) ([]byte, error) {
	data, err := b.Provider.Get(ctx, key, relPath)
	if err != nil {
		return nil, err
	}
	if relPath == b.tamperRel {
		// 返回一份长度相同但内容不同的流：只有真的做了 sha256 比对才会发现。
		poisoned := append([]byte(nil), data...)
		poisoned[len(poisoned)-1] ^= 0xff
		return poisoned, nil
	}
	return data, nil
}

// TestExportTreeFailsWhenReadBackTampered 钉住「校验不通过必须报错」。
//
// 这是整条回收链路的安全底座：导出侧一旦把「写成功」当成「校验通过」，
// 热层就会在一份不可信的副本之上被回收。
func TestExportTreeFailsWhenReadBackTampered(t *testing.T) {
	srcDir := t.TempDir()
	writeSnapshot(t, srcDir)

	provider := &brokenProvider{
		Provider:  NewMemoryProvider(),
		tamperRel: "raw/files/datadb/00000001.gz",
	}
	x := newTestExporter(provider)
	key := PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-01", Generation: 1}

	_, err := x.exportTree(context.Background(), key, srcDir)
	if err == nil {
		t.Fatal("回读内容被篡改时必须报错，实际返回成功")
	}
	if !strings.Contains(err.Error(), "回读校验失败") {
		t.Fatalf("错误信息应指明「回读校验失败」，得到 %v", err)
	}
}

// TestExportTreeRejectsEmptySnapshot 钉住「空快照不得产出成功」。
//
// 空副本 + 三闸全开 = 一天的数据被永久删除，且归档里什么都没有。
func TestExportTreeRejectsEmptySnapshot(t *testing.T) {
	srcDir := t.TempDir() // 空目录
	x := newTestExporter(NewMemoryProvider())
	key := PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-01", Generation: 1}

	if _, err := x.exportTree(context.Background(), key, srcDir); err == nil {
		t.Fatal("空快照必须报错")
	}
}

// TestExportTreeRejectsSymlink 钉住「快照内的符号链接一律拒绝」。
//
// 与 treeDigest / copyTree 同一口径：符号链接可能指向快照之外（另一天、
// 仍在写入的目录、系统文件），导出它等于把边界之外的东西带进归档。
func TestExportTreeRejectsSymlink(t *testing.T) {
	srcDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("写入外部文件失败：%v", err)
	}
	if err := os.Symlink(outside, filepath.Join(srcDir, "link")); err != nil {
		t.Skipf("当前环境不支持创建符号链接：%v", err)
	}

	x := newTestExporter(NewMemoryProvider())
	key := PartitionKey{StorageNamespace: "inst:1", UTCDay: "2026-09-01", Generation: 1}
	if _, err := x.exportTree(context.Background(), key, srcDir); err == nil {
		t.Fatal("快照内出现符号链接必须报错")
	}
}

// TestExportRequiresWiring 钉住「没装配就必须报错，而不是导出空副本」。
func TestExportRequiresWiring(t *testing.T) {
	x := &ArchiveCopyExporter{}
	if _, err := x.Export(context.Background(), "inst:1", "2026-09-01"); !errors.Is(err, ErrArchiveCopyNotWired) {
		t.Fatalf("未装配时应返回 ErrArchiveCopyNotWired，得到 %v", err)
	}
}
