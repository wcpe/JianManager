package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// 本文件实现 FR-501 的 `ActionArchiveCopy` 落地侧：「导出 → 压缩 → 校验」。
//
// 它在硬规则里的位置（见 retention 包 go 的文件头）：
//
//	① 首选：搬运到 COLD（既有 ROUTING_FROZEN→…→CLEANED 状态机）；
//	② 次选：**本文件**——导出为独立副本（gzip + manifest + 逐文件回读校验）；
//	③ 仅当配置显式 discard 才允许直接删。
//
// 三件必须复用、不许新造的东西：
//   - **冻结保护**：复用 `VLPartitionOps.snapshot`（内部先做
//     `catalog.CheckPhysicalDayDrained` 双闸「day.Frozen 为真且无在途写许可」，
//     再 `prepareHotSource` 做 force_flush，最后走 `/internal/partition/snapshot/create`
//     并要求返回**恰好一个**位于受管 snapshots 根下的稳定目录）。导出只读这个快照目录，
//     绝不直接读仍在写入的 HOT 数据目录。
//   - **manifest 契约**：复用 `Manifest`（schema_version / checksums /
//     coverage / raw_files / time_range），与 `logs/archive` 的 `manifest.json` 同一形态，
//     使已导出的副本将来能被同一套 Rehydrate/查询实现读懂。
//   - **内容身份**：复用 `ContentHash` / `ObjectIDFor`。
//
// 本文件**不**决定「能不能删热层」：那是策略与三闸的事（retention.Policy.DeleteGatesOpen）。
// 导出器只负责「副本是否真的可信」，删不删由调用方按闸门判定。
//
// **已知边界（如实登记）**：`Provider` 的契约是 `Put(... []byte)`，故单个源文件
// 会被整体载入内存再压缩。VL 的日分区里单个 part 文件通常只有几 MB 到几十 MB，
// 但极端情况下（超大单文件）会让 RSS 出现一个尖峰。缓解手段是调度：保留策略驱动器
// 默认在本地凌晨 3 点跑（`log_retention.trigger.sweep_at_hour`），与业务高峰错开。

// ExportSummary 是一次导出的结果摘要。
//
// 为什么用本包自己的类型而不是直接返回 retention.ArchiveExportResult：
// 依赖方向要单向——`retention`（策略/闸门）不该知道冻结快照与 manifest，
// `lifecycle`（物理操作）也不该知道保留策略。两者的桥由装配处（apps/worker）
// 用 `retention.ArchiveExporterFunc` 适配，各守一侧。
type ExportSummary struct {
	// ObjectCount 成功导出并**逐个回读校验通过**的对象数。
	ObjectCount int
	// SourceBytes 源侧（冻结快照）字节数合计，压缩前。
	SourceBytes int64
	// StoredBytes 归档侧字节数合计，即 gzip 压缩后的落盘量。
	StoredBytes int64
	// Location 归档位置的可读标识（审计「去哪」）。
	Location string
	// ProviderKind 归档目的地类型（如 local）。
	ProviderKind string
	// ObjectPrefix 归档对象前缀（namespace/day/g{generation}）。
	ObjectPrefix string
	// ManifestChecksum **回读**的 manifest 字节 sha256。
	//
	// 它是「这一天确实归档成了一份可核验清单」的唯一凭据：只有它被写进 Catalog 的
	// ArchiveEvidence，查询侧才有一份独立于归档目录的索引指纹可对。
	ManifestChecksum string
	// CoverageComplete 导出侧枚举是否已穷尽。
	CoverageComplete bool
}

// ErrArchiveCopyNotWired 表示导出器缺少必要装配。
var ErrArchiveCopyNotWired = fmt.Errorf("lifecycle: 导出归档未装配（需要冻结快照来源与归档 Provider）")

// FrozenSnapshotFunc 返回某个 UTC 日分区的**冻结快照目录**（相对/绝对路径由实现决定）。
//
// 它是本包与 VL 物理布局之间**唯一**的接缝：本包只认「一个装着源文件的目录」，
// 不知道 snapshots 根、不知道 force_flush、也不知道「物理日是否已冻结接纳」。
// 这些判断留在提供方（lifecycle.VLPartitionOps），由装配层注入——
// 与仓库既有的 Mover/MoverFunc、RemoverFunc、ArchiveExporterFunc 同一取舍：
// 消费者只声明自己需要的那一个动作，实现留在懂得物理布局的包里。
type FrozenSnapshotFunc func(storageNamespace, utcDay string) (string, error)

// ArchiveCopyExporter 把 HOT 日分区的冻结快照导出为独立归档副本。
//
// 只依赖 `Provider`（本地目录或对象存储），不含任何网络协议或凭证模型——
// 那些属于 Provider 实现的责任。
type ArchiveCopyExporter struct {
	// Catalog 用于读分区修订（generation）与「物理日已冻结接纳」的前置校验。
	Catalog *catalog.Catalog
	// Snapshot 提供冻结快照目录（由装配层注入 lifecycle 的物理实现）。
	Snapshot FrozenSnapshotFunc
	// Provider 归档目的地。
	Provider Provider
	// ParserVersion / EngineVersion 写入 manifest 的契约版本；空则用 archive 包的默认值。
	//
	// 不在这里发明取值：manifest 的版本字段是**契约冻结项**，填错会让将来的读取方
	// 按错误的解析规则解释这份归档。
	ParserVersion string
	EngineVersion string
	// Now 可注入时钟（manifest 的 created_at/updated_at、raw_files 的 registered_at）。
	Now func() time.Time
}

// NewArchiveCopyExporter 创建导出器。
func NewArchiveCopyExporter(cat *catalog.Catalog, snapshot FrozenSnapshotFunc, provider Provider) *ArchiveCopyExporter {
	return &ArchiveCopyExporter{
		Catalog:  cat,
		Snapshot: snapshot,
		Provider: provider,
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

func (x *ArchiveCopyExporter) now() time.Time {
	if x == nil || x.Now == nil {
		return time.Now().UTC()
	}
	return x.Now().UTC()
}

// Export 完成「冻结快照 → 逐文件 gzip → 写 manifest → 逐文件回读校验」。
//
// 返回 nil 错误的前提是**每一个对象都已回读并比对 sha256 通过、且能解压还原成源字节**，
// 且 manifest 本身也回读并通过 `Manifest.Validate`。
// 调用方据此才可能获准回收热层——这是「校验」在这条路径上的全部意义。
func (x *ArchiveCopyExporter) Export(ctx context.Context, storageNamespace, utcDay string) (ExportSummary, error) {
	if x == nil || x.Snapshot == nil || x.Provider == nil || x.Catalog == nil {
		return ExportSummary{}, ErrArchiveCopyNotWired
	}
	if err := ctx.Err(); err != nil {
		return ExportSummary{}, err
	}
	catalogKey := catalog.PartitionKey{StorageNamespace: storageNamespace, UTCDay: utcDay}

	// 第一步：确认该物理日已冻结接纳，再取冻结快照。**不许**退回「直接读 HOT 目录」的捷径——
	// 那个目录正在被写入，读出来的东西既不是一致视图，也无从证明它不再变化。
	//
	// 冻结由**调用方**（保留策略执行器的归档权威态出口）在导出前建立、并在
	// 「导出成功但最终未归档」的每条退出路径上解除。这里只做**前置校验、不自己冻结**：
	// 若由导出器临时冻结再自行解冻，会存在一个窗口——快照已取、冻结已解、
	// 新写入落进热层，随后归档删除热层就把这批新数据一起删掉。
	// 因此冻结必须横跨「快照 → 归档标记 → 删除/回滚」全过程。
	if err := x.Catalog.CheckPhysicalDayDrained(canonicalDayOf(catalogKey)); err != nil {
		return ExportSummary{}, fmt.Errorf("lifecycle: 物理日未冻结接纳，拒绝导出（调用方须先冻结）：%w", err)
	}
	// 快照本身、以及「读哪里」的路径边界校验，都由注入的 Snapshot 实现负责（在 lifecycle 侧）：
	// 下面会 WalkDir 读取该目录下的每一个文件，那道校验是这条路径上唯一的边界。
	snapshotDir, err := x.Snapshot(storageNamespace, utcDay)
	if err != nil {
		return ExportSummary{}, err
	}

	akey := PartitionKey{
		StorageNamespace: storageNamespace,
		UTCDay:           utcDay,
		Generation:       x.generationFor(catalogKey),
	}
	if err := akey.Validate(); err != nil {
		return ExportSummary{}, fmt.Errorf("lifecycle: 归档分区键非法：%w", err)
	}
	return x.exportTree(ctx, akey, snapshotDir)
}

// generationFor 返回归档分区使用的 generation。
//
// 取 Catalog 记录的 generation（>0）而不是固定 1：归档分区路径形如
// `ns/day/g<generation>/`，跟着 Catalog 的 generation 走，就能保证
// 「同一个 UTC 日在不同 generation 下的导出互不覆盖」——这正是 archive 包
// `Generation` 字段存在的理由（不同 generation 物理路径隔离）。
// Catalog 尚无记录（或 generation 为 0）时用 1：archive 要求 generation > 0，
// 而这里是「第一次导出」，用 1 是稳定且可复现的起点。
func (x *ArchiveCopyExporter) generationFor(key catalog.PartitionKey) uint64 {
	if x != nil && x.Catalog != nil {
		if rec, ok := x.Catalog.Get(key); ok && rec.Generation > 0 {
			return rec.Generation
		}
	}
	return 1
}

// exportTree 把 sourceDir 下的每个文件 gzip 后写入归档分区，并生成+校验 manifest。
//
// 单独成函数是为了可测：它只需要一个源目录与一个 Provider（不需要真实 VL），
// 于是「压缩往返一致性」「校验失败必须报错」这些性质可以直接断言。
func (x *ArchiveCopyExporter) exportTree(ctx context.Context, akey PartitionKey, sourceDir string) (ExportSummary, error) {
	if x == nil || x.Provider == nil {
		return ExportSummary{}, ErrArchiveCopyNotWired
	}
	files, err := listSnapshotFiles(sourceDir)
	if err != nil {
		return ExportSummary{}, err
	}
	if len(files) == 0 {
		// 空快照绝不产出「成功」：一份零对象的副本不能作为回收热层的依据，
		// 否则归档成功 + 三闸全开 = 一天的数据被永久删掉。
		return ExportSummary{}, fmt.Errorf("lifecycle: 冻结快照为空（%s），拒绝生成空归档", sourceDir)
	}

	now := x.now()
	parserVersion := x.ParserVersion
	if parserVersion == "" {
		parserVersion = DefaultParserVersion
	}
	manifest := NewManifest(akey, x.ParserVersion, x.EngineVersion)
	// 覆盖 NewManifest 内部取的真实时间：本类型的时钟是可注入的，若这里漏掉，
	// 「冻结快照内容不变 ⇒ 重跑导出产出同样字节」这条性质就不成立（created_at 每次都变），
	// 而幂等正是「导出成功但进程在写 manifest 前退出」能够自愈的前提。
	manifest.CreatedAt = now
	manifest.UpdatedAt = now
	var sourceBytes, storedBytes int64

	for _, rel := range files {
		if err := ctx.Err(); err != nil {
			return ExportSummary{}, err
		}
		srcPath := filepath.Join(sourceDir, filepath.FromSlash(rel))
		raw, err := os.ReadFile(srcPath)
		if err != nil {
			return ExportSummary{}, fmt.Errorf("lifecycle: 读取快照文件 %s 失败：%w", rel, err)
		}
		compressed, err := gzipBytes(raw)
		if err != nil {
			return ExportSummary{}, fmt.Errorf("lifecycle: 压缩 %s 失败：%w", rel, err)
		}

		// 归档内路径按「源快照内的相对路径」一一对应（不是按内容哈希）：
		//   - 同一分区内不会出现两个文件映射到同一路径 ⇒ manifest 的 rel_path 唯一；
		//   - 重复导出是**幂等**的（冻结快照的内容不会变）⇒ 允许重跑。
		objRel := "raw/files/" + rel + ".gz"
		if err := validateArchiveRelPath(objRel); err != nil {
			return ExportSummary{}, err
		}
		contentHash := ContentHash(compressed)
		if err := x.Provider.Put(ctx, akey, objRel, compressed); err != nil {
			return ExportSummary{}, fmt.Errorf("lifecycle: 写入归档对象 %s 失败：%w", objRel, err)
		}

		// 校验①：回读字节必须与刚写入的字节**完全一致**（sha256）。
		// 这一步防的是「Put 返回成功但对象已被截断/篡改/落到别的键上」。
		got, err := x.Provider.Get(ctx, akey, objRel)
		if err != nil {
			return ExportSummary{}, fmt.Errorf("lifecycle: 回读归档对象 %s 失败：%w", objRel, err)
		}
		if gotHash := ContentHash(got); gotHash != contentHash {
			return ExportSummary{}, fmt.Errorf(
				"lifecycle: 归档对象 %s 回读校验失败（写入 %s，回读 %s）", objRel, contentHash, gotHash)
		}
		// 校验②：回读的字节必须能解压**还原成源文件的原始字节**。
		// 只有 sha256 一致还不够——它证明「存的东西没变」，不证明「存的东西就是源」。
		if err := verifyGzipEquals(got, raw); err != nil {
			return ExportSummary{}, fmt.Errorf("lifecycle: 归档对象 %s 解压还原失败：%w", objRel, err)
		}

		manifest.RawFiles = append(manifest.RawFiles, RawFile{
			// ObjectID 是内容身份（sha256(压缩内容)+大小），与 archive 包同一口径。
			ObjectID:      ObjectIDFor(contentHash, int64(len(compressed))),
			RelPath:       objRel,
			SizeBytes:     int64(len(compressed)),
			ContentSHA256: contentHash,
			// Origin 记 worker_generated：这份副本由 Worker 自己生成（不是实例 .gz 源，
			// 也不是 STDIO 分段），与 RawOrigin 的既有取值对齐。
			Origin:        OriginWorkerGenerated,
			State:         ObjectStateManaged,
			SourcePath:    rel,
			ParserVersion: parserVersion,
			RegisteredAt:  now,
		})
		manifest.Checksums.Objects[objRel] = contentHash
		sourceBytes += int64(len(raw))
		storedBytes += int64(len(compressed))
	}

	// coverage：完整的一天（快照目录已穷尽），且这是**已封存**的一份归档。
	//
	// EventCount 保持 0 并在注释里写明：本路径按**物理文件**导出，没有做事件级解析，
	// 因此不谎报事件条数（与 STDIO 分段登记的语义不同）。Complete=true 表达的是
	// 「源侧枚举已穷尽」，不是「事件已解析」。
	manifest.Coverage.Complete = true
	manifest.Coverage.EnumerationState = EnumerationExhausted
	manifest.Coverage.ByteCount = uint64(storedBytes)
	if from, to, ok := dayRange(akey.UTCDay); ok {
		manifest.TimeRange = TimeRange{FromUTC: from, ToUTC: to}
	}
	manifest.UpdatedAt = now

	if err := manifest.Validate(); err != nil {
		// Validate 失败说明我们自己生成的 manifest 不自洽（例如 rel_path 重复、
		// checksum 与 raw_files 不一致）。此时**不能**把它写下去：一份自相矛盾的
		// manifest 比没有 manifest 更危险（读取方会按它去找对象）。
		return ExportSummary{}, fmt.Errorf("lifecycle: 生成的 manifest 不自洽：%w", err)
	}
	data, err := MarshalManifest(manifest)
	if err != nil {
		return ExportSummary{}, err
	}
	if err := x.Provider.Put(ctx, akey, ManifestRelPath, data); err != nil {
		return ExportSummary{}, fmt.Errorf("lifecycle: 写入 manifest 失败：%w", err)
	}
	// 校验③：manifest 自己也回读一遍并重新通过 Validate——
	// 它是这整份副本的索引，索引坏了则所有对象都等于不存在。
	stored, err := x.Provider.Get(ctx, akey, ManifestRelPath)
	if err != nil {
		return ExportSummary{}, fmt.Errorf("lifecycle: 回读 manifest 失败：%w", err)
	}
	reloaded, err := UnmarshalManifest(stored)
	if err != nil {
		return ExportSummary{}, fmt.Errorf("lifecycle: 回读的 manifest 非法：%w", err)
	}
	if len(reloaded.RawFiles) != len(files) {
		return ExportSummary{}, fmt.Errorf(
			"lifecycle: 回读的 manifest 对象数不一致（写入 %d，回读 %d）", len(files), len(reloaded.RawFiles))
	}

	return ExportSummary{
		ObjectCount:      len(files),
		SourceBytes:      sourceBytes,
		StoredBytes:      storedBytes,
		Location:         fmt.Sprintf("%s %s", x.Provider.Kind(), akey.String()),
		ProviderKind:     x.Provider.Kind(),
		ObjectPrefix:     akey.String(),
		ManifestChecksum: ContentHash(stored),
		CoverageComplete: reloaded.Coverage.Complete,
	}, nil
}

// listSnapshotFiles 列出快照目录下的全部普通文件（相对路径，slash 分隔，已排序）。
//
// 与 `treeDigest` / `copyTree` 同一套拒绝口径：符号链接一律拒绝（它可能指向快照
// 之外，甚至指向另一天或仍在写入的目录），非普通文件（设备、FIFO、socket）同样拒绝。
// 排序是为了让导出顺序与 manifest 内容稳定可复现。
func listSnapshotFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("lifecycle: 快照内不允许符号链接：%s", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("lifecycle: 快照内不支持的文件类型：%s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// validateArchiveRelPath 保证归档内的相对路径不会逃出分区根。
//
// 这道校验的理由与 archive 包 `objectPath` 相同：rel_path 直接参与拼物理路径，
// 一个 `../` 就能把写入引到分区之外。
func validateArchiveRelPath(rel string) error {
	if rel == "" || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("lifecycle: 归档对象路径非法：%q", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("lifecycle: 归档对象路径逃出分区：%q", rel)
	}
	return nil
}

// gzipBytes 以 gzip 压缩单个文件。
//
// 压缩级别取 BestCompression：这条路径一天只跑一次（每个日分区一份），不在热路径上，
// 而用户对它的期待就是「和归档 gz 一样省空间」。用默认级别（6）能快 2~3 倍，
// 但空间只差几个百分点——在一个「省不下盘就等于没做」的功能里，省空间优先。
//
// 头部字段刻意留空：
//   - Name 不写：避免把源路径（含实例/节点标识）带进压缩头，也避免同一内容产生不同字节；
//   - ModTime 不写（零值）：gzip 会省略该字段 ⇒ 同样输入产生同样输出，
//     于是「重跑导出」是幂等的，content hash 也可复现。
func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(data); err != nil {
		_ = zw.Close()
		return nil, err
	}
	// Close 才会写出 gzip trailer（CRC32 + 原始长度）。漏掉它得到的是一份
	// 「gzip 头 + 数据但无校验尾」的残缺流，解压时才报 unexpected EOF。
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// verifyGzipEquals 解压 compressed 并与 want 逐字节比较。
func verifyGzipEquals(compressed, want []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("gzip 头非法：%w", err)
	}
	defer zr.Close()
	// 上限按 want 的长度收口：多出来的字节说明「压缩流里装的不只是源内容」，
	// 这同样必须报错而不是悄悄截断。
	got, err := io.ReadAll(io.LimitReader(zr, int64(len(want))+1))
	if err != nil {
		return fmt.Errorf("解压失败：%w", err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("解压后长度不一致（源 %d，还原 %d）", len(want), len(got))
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("解压后内容与源不一致")
	}
	return nil
}

// dayRange 返回某个 UTC 日分区覆盖的时间范围 [当日 00:00Z, 次日 00:00Z)。
func dayRange(utcDay string) (string, string, bool) {
	day, err := time.Parse("2006-01-02", utcDay)
	if err != nil {
		if day, err = time.Parse("20060102", utcDay); err != nil {
			return "", "", false
		}
	}
	const layout = time.RFC3339
	return day.UTC().Format(layout), day.UTC().Add(24 * time.Hour).Format(layout), true
}

// canonicalDayOf 返回 Catalog 使用的日期写法（`2006-01-02`）；无法解析时原样返回。
//
// Catalog 的日键与 retention 的 `20060102` 不一致，物理日的冻结/查询都按 Catalog 写法，
// 因此这里必须归一——否则同一物理日会因写法不同而「冻结了一天、快照另一天」。
func canonicalDayOf(key catalog.PartitionKey) string {
	if day, err := time.Parse("2006-01-02", key.UTCDay); err == nil {
		return day.Format("2006-01-02")
	}
	if day, err := time.Parse("20060102", key.UTCDay); err == nil {
		return day.Format("2006-01-02")
	}
	return key.UTCDay
}

// archiveDayDirID 返回归档日冻结使用的目录标识。
//
// 它只是「该日已进入归档」的稳定标记，不是物理路径：归档目的地与对象前缀由 manifest
// 与 Catalog 的 ArchiveEvidence 承载。用日期派生而非随机值，使重跑冻结是幂等的。
func archiveDayDirID(key catalog.PartitionKey) string {
	return "archive:" + canonicalDayOf(key)
}
