package acquire

// 纯文本轮转归档的导入（BungeeCord 的 `proxy.log.N`）。
//
// 背景：BungeeCord 的轮转产物是**纯文本**且**没有扩展名**（java.util.logging.FileHandler
// 把 proxy.log.0 轮转成 proxy.log.1、proxy.log.2…），而此前的归档导入器只有 gzip 一种实现
// ⇒ 任何被发现的 proxy.log.N 都以 ARCHIVE_GZIP_CORRUPT 失败，而该原因不在自动消解白名单
// （ledger/gap_bounds.go），源一旦被暂停就永久停采。故 bungee 规则的归档 glob 被如实置空。
//
// 本文件守住新能力的四条边界：
//  ① 纯文本能导入，且事件口径与 gzip **完全一致**（同内容 ⇒ 同 event_id / canonical hash /
//     record 范围）——「走完全相同的下游链路」在事件层就是这一条；
//  ② 导入仍是**有界分批**的：单轮不读完、位置落回账本、下一轮从断点续读（不丢、不重读前缀）；
//  ③ 同一文件不会被重复导入（第二次扫描被跳过）；
//  ④ 形态判定按**文件头**（gzip 魔数），不按扩展名；退化形态（0 字节锁文件）不得留下
//     不可自愈的缺口。
//
// 变异验证记录（2026-10-16，逐字还原后 sha256 与变异前一致）：
//
//   M1 —— 把 pipeline.ImportArchive 的形态分流回退为「一律 ImportGzipBounded」：
//     pipeline.TestImportArchiveDispatchesByFileHeader / ...TextIsBoundedAcrossRounds /
//     ...TextDegenerateFormsLeaveNoUnhealableGap → FAIL（纯文本被当 gzip：0 事件 +
//     ARCHIVE_GZIP_CORRUPT 缺口），ingest.TestRegisterInstanceBungeeRuleImportsPlainTextRotations
//     同时转红（端到端链路上「轮转正文必须进事件段」的断言失败）。
//
//   M2 —— 把 SegmentRotatedText 从 ledger.IsArchiveSegmentKind 的 true 分支删掉
//     （等价于「新增 kind 不参与归档类判定」）：
//     ledger.TestIsArchiveSegmentKindCoversArchiveFormats → FAIL；
//     本文件的 TestImportTextProducesSameEventsAsGzip（"已导入登记不得把形态改写成 gzip"）
//     与 TestImportTextIsBoundedAndResumes（"置「已导入」不得改写形态"）同时转红——
//     形态判定缺失会让 MarkImported/markFailed 把纯文本归档记成 gzip（账本失真）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// writePlainText 写入一个纯文本轮转归档（无扩展名，逐行以 \n 结尾）。
func writePlainText(t *testing.T, path string, lines []string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

// archiveSegmentOf 取出账本里该路径的分段（不存在返回 nil）。
func archiveSegmentOf(led *ledger.Ledger, key ledger.SourceKey, path string) *ledger.Segment {
	entry := led.Get(key)
	if entry == nil {
		return nil
	}
	for i := range entry.Segments {
		if entry.Segments[i].Path == path {
			seg := entry.Segments[i]
			return &seg
		}
	}
	return nil
}

// gapReasonsOf 返回账本里的缺口原因列表。
func gapReasonsOf(led *ledger.Ledger, key ledger.SourceKey) []string {
	entry := led.Get(key)
	if entry == nil {
		return nil
	}
	out := make([]string, 0, len(entry.Gaps))
	for _, gap := range entry.Gaps {
		out = append(out, gap.Reason)
	}
	return out
}

// TestDetectArchiveFormatByFileHeader 锁定形态判定的判据顺序。
//
// 主判据是**内容**：BungeeCord 的轮转产物 proxy.log.N **没有扩展名**，故「无 .gz 后缀」
// 推不出「不是压缩归档」——必须读文件头（gzip 魔数 1f 8b）才敢分流。
//
// 唯一的按名分支是「名字声明自己是压缩归档（.gz 结尾）」：那是**既有契约**（归档发现给
// bukkit 系匹配的就是 logs/*.gz），内容不是 gzip 时必须仍走 gzip 路径并报
// ARCHIVE_GZIP_CORRUPT —— 写坏/截断的归档要可见、要隔离，不能被当成纯文本静默导入
// 一堆垃圾事件（既有回归 TestManagerQuarantinesUnchangedCorruptGzipWithoutRepeatingGap 守这条）。
func TestDetectArchiveFormatByFileHeader(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "proxy.log.1"),
		[]byte("[12:00:00] [INFO] plain\n"), 0o644))
	writeGzip(t, filepath.Join(dir, "proxy.log.2"), []string{"[12:00:00] [INFO] gz"})
	// 1 字节与 0 字节的非 .gz 文件：内容不足以构成 gzip 头（不足 2 字节）⇒ 判为纯文本。
	require.NoError(t, os.WriteFile(filepath.Join(dir, "proxy.log.3"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "proxy.log.1.lck"), nil, 0o644))
	// 0 字节的 .gz：名字声明压缩形态 ⇒ 交回既有 gzip 路径，
	// 使「空/截断 .gz」的既有诊断（ARCHIVE_GZIP_CORRUPT）逐字不变。
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.gz"), nil, 0o644))
	// 内容是垃圾（不是 gzip 流）但名字声明 .gz：同样交回 gzip 路径（既有契约）。
	require.NoError(t, os.WriteFile(filepath.Join(dir, "2026-09-22-1.log.gz"), []byte("not-gzip"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "proxy.log.9"), 0o755))

	cases := []struct {
		name string
		path string
		want ledger.SegmentKind
	}{
		{"纯文本轮转（无扩展名）", filepath.Join(dir, "proxy.log.1"), ledger.SegmentRotatedText},
		{"gzip 归档（按魔数识别，即使没有 .gz 后缀）", filepath.Join(dir, "proxy.log.2"), ledger.SegmentGzip},
		{"1 字节文件", filepath.Join(dir, "proxy.log.3"), ledger.SegmentRotatedText},
		{"0 字节锁文件", filepath.Join(dir, "proxy.log.1.lck"), ledger.SegmentRotatedText},
		{"0 字节 .gz（名字声明压缩形态）", filepath.Join(dir, "empty.gz"), ledger.SegmentGzip},
		{"内容写坏的 .gz（既有契约：报损坏而不是当纯文本导入）",
			filepath.Join(dir, "2026-09-22-1.log.gz"), ledger.SegmentGzip},
		{"目录（内容不可知 ⇒ 交回既有 gzip 路径）", filepath.Join(dir, "proxy.log.9"), ledger.SegmentGzip},
		{"不存在的文件（内容不可知 ⇒ 交回既有 gzip 路径）", filepath.Join(dir, "nope"), ledger.SegmentGzip},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, DetectArchiveFormat(c.path))
		})
	}
}

// TestImportTextNeverTouchesCorruptGzipNames 复核「写坏的 .gz 仍按 gzip 失败」这条既有契约
// 在形态分流之后依然成立：它是 gzip 路径「行为逐字不变」在分流边界上的定点证据。
func TestImportTextNeverTouchesCorruptGzipNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-09-22-1.log.gz")
	require.NoError(t, os.WriteFile(path, []byte("not-gzip"), 0o644))

	key := testKey("src-corrupt-gz", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)

	// 分流结果必须是 gzip ⇒ 走 gzip 实现 ⇒ 报损坏 + 隔离（而不是被纯文本实现当正文导入）。
	require.Equal(t, ledger.SegmentGzip, DetectArchiveFormat(path))
	res, err := imp.ImportGzip(path)
	require.NoError(t, err)
	require.True(t, res.Skipped)
	require.Empty(t, res.Events)
	require.Contains(t, gapReasonsOf(led, key), ledger.GapReasonArchiveGzipCorrupt)
	seg := archiveSegmentOf(led, key, path)
	require.Equal(t, ledger.SegmentGzip, seg.Kind)
	require.NotEmpty(t, seg.ImportError)
	require.False(t, seg.Imported)
}

// TestImportTextProducesSameEventsAsGzip 是「走与 gzip 完全相同的下游链路」的事件级判据。
//
// 同一份行内容分别以 gzip 与纯文本形态归档：**同源同代次**下，两条路径产出的 event_id、
// canonical hash、record 范围与正文必须逐条相等（形态不参与事件身份，只有 provenance
// archive_object_id 因字节不同而不同）。这条断言是「口径一致」的硬证据——若纯文本实现另起
// 一套行/位置口径（例如把位置算成解压后偏移、或丢掉行尾分隔符），它会立即转红。
func TestImportTextProducesSameEventsAsGzip(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"[12:00:00] [INFO] first line",
		"[12:00:01] [WARN] second line",
		"[12:00:02] [SEVERE] third line",
	}
	txtPath := filepath.Join(dir, "proxy.log.1")
	writePlainText(t, txtPath, lines)
	gzPath := filepath.Join(dir, "proxy.log.1.gz")
	writeGzip(t, gzPath, lines)

	key := testKey("src-text-parity", "g1")

	ledText := ledger.New()
	impText := NewArchiveImporter(ledText, key)
	resText, err := impText.ImportText(txtPath)
	require.NoError(t, err)
	require.False(t, resText.Skipped, "首次见到的纯文本归档必须被导入")
	require.Equal(t, len(lines), resText.ImportedCount)

	ledGzip := ledger.New()
	resGzip, err := NewArchiveImporter(ledGzip, key).ImportGzip(gzPath)
	require.NoError(t, err)
	require.False(t, resGzip.Skipped)

	require.Len(t, resText.Events, len(lines))
	require.Len(t, resGzip.Events, len(lines))
	for i := range resText.Events {
		got, want := resText.Events[i], resGzip.Events[i]
		require.Equal(t, lines[i], got.Message, "第 %d 行正文必须逐字保留", i)
		require.Equal(t, want.EventID, got.EventID, "第 %d 行 event_id 必须与 gzip 路径一致", i)
		require.Equal(t, want.CanonicalHash, got.CanonicalHash, "第 %d 行 canonical hash 必须一致", i)
		require.Equal(t, want.Record, got.Record, "第 %d 行 record 范围必须一致", i)
		require.Equal(t, want.Stream, got.Stream)
		require.Equal(t, want.Source, got.Source)
		// provenance 只标识「来自哪个归档对象」，不参与 event_id（两形态字节不同 ⇒ 取值不同）。
		require.NotEmpty(t, got.Fields["archive_object_id"])
		require.NotEqual(t, want.Fields["archive_object_id"], got.Fields["archive_object_id"])
	}

	// 形态如实登记为纯文本轮转，且**没有**任何缺口：这条路径不该产生「gzip 损坏」这种
	// 不可自愈的原因（它是源永久停采的入口）。
	seg := archiveSegmentOf(ledText, key, txtPath)
	require.NotNil(t, seg)
	require.Equal(t, ledger.SegmentRotatedText, seg.Kind)
	require.Empty(t, gapReasonsOf(ledText, key), "纯文本归档正常导入不得留下任何缺口")

	// MarkImported 是形态无关的「已导入」入口，但它必须**保留**既有分段的形态，
	// 否则账本会把纯文本归档记成 gzip（诊断失真），后续按形态判定的逻辑也跟着错。
	require.NoError(t, impText.MarkImported(txtPath, resText.ArchiveObjectID,
		resText.Events[0].Record.Start, resText.Events[len(lines)-1].Record.End))
	seg = archiveSegmentOf(ledText, key, txtPath)
	require.True(t, seg.Imported)
	require.Equal(t, ledger.SegmentRotatedText, seg.Kind, "已导入登记不得把形态改写成 gzip")
	require.NotEmpty(t, seg.ArchiveObjectID)
}

// TestImportTextIsBoundedAndResumes 守住有界分批与断点续读（缺陷二修复的机制在纯文本路径同样成立）。
//
// 断言四件事：
//  1. 单轮只放行 maxEvents 条，且 Partial=true（归档还有剩余）；
//  2. 已消费位置**落回账本**（EndPos = 本批末条事件的范围终点），归档**未**被标为已导入；
//  3. 下一轮从断点续读：首条事件正是上一轮末条之后的那一行（不重读前缀、不跳过任何行）；
//  4. 读完（Partial=false）才置已导入；三轮事件的正文集合恰好等于原文件（不丢、不重、不乱序）。
func TestImportTextIsBoundedAndResumes(t *testing.T) {
	const (
		total   = 1200
		perCall = 500
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log.1")
	lines := make([]string, 0, total)
	for i := 0; i < total; i++ {
		lines = append(lines, fmt.Sprintf("[12:00:00] [INFO] rotated line %d", i))
	}
	writePlainText(t, path, lines)

	key := testKey("src-text-bounded", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)

	collected := make([]string, 0, total)
	var previousEnd uint64

	first, err := imp.ImportTextBounded(path, perCall)
	require.NoError(t, err)
	require.Len(t, first.Events, perCall, "单轮放行量必须是上限本身，而不是整个归档")
	require.True(t, first.Partial, "归档未读完必须如实报 Partial（否则尾部会被静默丢弃）")
	require.Equal(t, uint64(0), first.ResumedFrom)
	seg := archiveSegmentOf(led, key, path)
	require.NotNil(t, seg)
	require.Equal(t, first.Events[perCall-1].Record.End, seg.EndPos,
		"已消费位置必须落回账本分段（下一轮据此续读）")
	require.False(t, seg.Imported, "未读完的归档不得被标为已导入")
	require.Equal(t, ledger.SegmentRotatedText, seg.Kind)
	previousEnd = seg.EndPos
	for _, ev := range first.Events {
		collected = append(collected, ev.Message)
	}

	second, err := imp.ImportTextBounded(path, perCall)
	require.NoError(t, err)
	require.Len(t, second.Events, perCall)
	require.True(t, second.Partial)
	require.Equal(t, previousEnd, second.ResumedFrom, "第二轮必须从账本记录的断点续读")
	require.Equal(t, lines[perCall], second.Events[0].Message, "续读的第一条必须是断点之后的那一行")
	require.Equal(t, previousEnd, second.Events[0].Record.Start, "续读位置的 record 必须与账本断点一致")
	seg = archiveSegmentOf(led, key, path)
	require.Equal(t, second.Events[perCall-1].Record.End, seg.EndPos)
	require.False(t, seg.Imported)
	previousEnd = seg.EndPos
	for _, ev := range second.Events {
		collected = append(collected, ev.Message)
	}

	third, err := imp.ImportTextBounded(path, perCall)
	require.NoError(t, err)
	require.Len(t, third.Events, total-2*perCall)
	require.False(t, third.Partial, "读到归档末尾必须报 Partial=false")
	seg = archiveSegmentOf(led, key, path)
	require.Equal(t, third.Events[len(third.Events)-1].Record.End, seg.EndPos,
		"末轮的结束位置必须覆盖到文件末尾")
	require.Equal(t, uint64(len([]byte(strings.Join(lines, "\n")+"\n"))), seg.EndPos,
		"末轮的结束位置必须等于文件长度（全部字节都已消费）")
	// 「已导入」由调用方在事件 durable 之后置位（pipeline.ImportArchive 的收口），
	// 导入器本身在**产出事件**的那一轮不置位——这是「未 durable 不得标成已导入」的判据。
	require.False(t, seg.Imported, "导入器不得在产出事件的同一轮自行置「已导入」（须由 durable 覆盖后置位）")
	require.NoError(t, imp.MarkImported(path, first.ArchiveObjectID,
		third.Events[0].Record.Start, seg.EndPos))
	seg = archiveSegmentOf(led, key, path)
	require.True(t, seg.Imported)
	require.Equal(t, ledger.SegmentRotatedText, seg.Kind, "置「已导入」不得改写形态")
	for _, ev := range third.Events {
		collected = append(collected, ev.Message)
	}

	require.Equal(t, lines, collected, "跨轮收集的正文必须与原文件逐行相等（不丢、不重、不乱序）")
	require.Zero(t, imp.SkippedAlreadyLinked(), "三轮都在产出事件，没有任何一轮应被算作跳过")
}

// TestImportTextSkipsAlreadyImportedFile 守住「同一文件不会重复导入」。
//
// 为什么必须有这条：归档发现是**每轮**做的（采集轮 250ms 一跳），一个已导入的归档会在下一轮
// 再次出现在待导入集合里。若导入器不按（路径 + 已导入）拒绝整包，同一份字节就会以相同
// event_id 反复进 WAL/投递——重复投递会被 VL 侧去重掩盖，但本地积压与投递量会持续无谓增长。
func TestImportTextSkipsAlreadyImportedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log.1")
	lines := []string{"[12:00:00] [INFO] one", "[12:00:01] [INFO] two"}
	writePlainText(t, path, lines)

	key := testKey("src-text-idempotent", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)

	first, err := imp.ImportText(path)
	require.NoError(t, err)
	require.False(t, first.Skipped)
	require.NoError(t, imp.MarkImported(path, first.ArchiveObjectID,
		first.Events[0].Record.Start, first.Events[len(lines)-1].Record.End))

	again, err := imp.ImportText(path)
	require.NoError(t, err)
	require.True(t, again.Skipped, "已导入的归档不得被重复导入")
	require.Empty(t, again.Events)
	require.Equal(t, 0, again.ImportedCount)
	require.Equal(t, first.ArchiveObjectID, again.ArchiveObjectID)
	require.Equal(t, 1, imp.SkippedAlreadyLinked())
}

// TestImportTextEmptyRotatedFileLeavesNoGap 守住退化形态不产生**不可自愈**缺口。
//
// 现场形态：java.util.logging 会为每个日志文件创建同名的 0 字节锁文件（proxy.log.N.lck），
// 而 bungee 的归档 glob `proxy.log.[1-9]*` 会匹配到它们。它们不是归档，但**绝不能**走
// gzip 路径：那会给每个锁文件留一条 ARCHIVE_GZIP_CORRUPT 缺口，而该原因不在自动消解白名单
// （ledger/gap_bounds.go）⇒ 源一旦被暂停就永久停采（这正是本能力要消灭的形态）。
//
// 纯文本路径的处置是「空归档」：0 条事件、无缺口、直接记为已导入（不会每轮重试）。
func TestImportTextEmptyRotatedFileLeavesNoGap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log.1.lck")
	require.NoError(t, os.WriteFile(path, nil, 0o644))

	key := testKey("src-text-empty", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)

	res, err := imp.ImportText(path)
	require.NoError(t, err)
	require.True(t, res.Skipped)
	require.Empty(t, res.Events)
	require.Empty(t, gapReasonsOf(led, key), "空轮转文件不得留下任何缺口（更不得是 ARCHIVE_GZIP_CORRUPT）")

	seg := archiveSegmentOf(led, key, path)
	require.NotNil(t, seg)
	require.Equal(t, ledger.SegmentRotatedText, seg.Kind)
	require.True(t, seg.Imported, "空归档必须记为已导入，否则每轮都会重试同一个文件")
	require.False(t, ledger.IsGapReasonHealableOnReResume(ledger.GapReasonArchiveGzipCorrupt),
		"前提复核：ARCHIVE_GZIP_CORRUPT 不可自动消解 —— 故它绝不能出现在轮转文件的常规路径上")
}

// TestImportTextFailureKeepsPlainTextKind 守住失败路径的形态与原因码：
// 读不了的纯文本归档必须记 ARCHIVE_UNREADABLE（既有原因，语义正确），且分段形态仍是
// 纯文本轮转——不得被改写成 gzip（那会让「这个源有没有 gzip 归档」这一诊断失真）。
func TestImportTextFailureKeepsPlainTextKind(t *testing.T) {
	dir := t.TempDir()
	// 目录不是可读归档：HashFile 取内容摘要失败 ⇒ ARCHIVE_UNREADABLE（既有原因码，不新增）。
	path := filepath.Join(dir, "proxy.log.1")
	require.NoError(t, os.Mkdir(path, 0o755))

	key := testKey("src-text-fail", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)

	res, err := imp.ImportText(path)
	require.NoError(t, err, "归档面 IO 故障不得硬失败（否则会拖死整轮采集）")
	require.True(t, res.Skipped)
	require.Contains(t, gapReasonsOf(led, key), ledger.GapReasonArchiveUnreadable)

	seg := archiveSegmentOf(led, key, path)
	require.NotNil(t, seg)
	require.Equal(t, ledger.SegmentRotatedText, seg.Kind, "失败登记不得把形态改写成 gzip")
	require.False(t, seg.Imported)
	require.NotEmpty(t, seg.ImportError)
}

// TestImportGzipKeepsGzipKindAfterMarkImported 是 gzip 路径「行为逐字不变」的定点复核：
// 形态感知的 MarkImported/MarkFailed 改动之后，gzip 归档仍必须被记为 SegmentGzip。
func TestImportGzipKeepsGzipKindAfterMarkImported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2026-01-01-1.log.gz")
	writeGzip(t, path, []string{"[12:00:00] [INFO] gz"})

	key := testKey("src-gzip-kind", "g1")
	led := ledger.New()
	imp := NewArchiveImporter(led, key)

	res, err := imp.ImportGzip(path)
	require.NoError(t, err)
	require.False(t, res.Skipped)
	require.Equal(t, ledger.SegmentGzip, archiveSegmentOf(led, key, path).Kind)

	require.NoError(t, imp.MarkImported(path, res.ArchiveObjectID,
		res.Events[0].Record.Start, res.Events[0].Record.End))
	seg := archiveSegmentOf(led, key, path)
	require.True(t, seg.Imported)
	require.Equal(t, ledger.SegmentGzip, seg.Kind)

	// 无既存分段时（ingest 侧直接调 MarkImported 的形态）：仍按 gzip 登记，与历史逐字一致。
	orphan := filepath.Join(dir, "2026-01-02-1.log.gz")
	writeGzip(t, orphan, []string{"[12:00:00] [INFO] orphan"})
	require.NoError(t, imp.MarkImported(orphan, "obj-orphan", 0, 5))
	require.Equal(t, ledger.SegmentGzip, archiveSegmentOf(led, key, orphan).Kind)
}

// TestImportTextRecordRangesMatchRawBytes 复核纯文本路径的位置口径 = **文件原始字节偏移**
// （含行尾分隔符），这样「已消费字节数」与归档文件长度可直接对照（有界续读的跳字节依据）。
func TestImportTextRecordRangesMatchRawBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log.1")
	lines := []string{"[12:00:00] [INFO] alpha", "[12:00:01] [INFO] beta"}
	writePlainText(t, path, lines)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	key := testKey("src-text-offsets", "g1")
	led := ledger.New()
	res, err := NewArchiveImporter(led, key).ImportText(path)
	require.NoError(t, err)
	require.Len(t, res.Events, 2)
	require.EqualValues(t, 0, res.Events[0].Record.Start)
	require.EqualValues(t, len(lines[0])+1, res.Events[0].Record.End, "范围含行尾分隔符")
	require.EqualValues(t, len(lines[0])+1, res.Events[1].Record.Start, "行与行连续无缝")
	require.EqualValues(t, len(raw), res.Events[1].Record.End, "末条范围终点 = 文件长度")

	// 与 gzip 路径的对照：gzip 的位置是**解压后**偏移，对同一份文本应与原始字节偏移一致。
	gzPath := filepath.Join(dir, "proxy.log.1.gz")
	writeGzip(t, gzPath, lines)
	gzRes, err := NewArchiveImporter(ledger.New(), key).ImportGzip(gzPath)
	require.NoError(t, err)
	require.Equal(t, res.Events[1].Record.End, gzRes.Events[1].Record.End)
}

// TestImportTextHandlesCRLFAndMissingFinalNewline 复核行口径与 gzip 路径同源：
// CRLF 与「末行无换行」都按既有语义处理（末行无换行也必须产出事件，不得丢尾）。
func TestImportTextHandlesCRLFAndMissingFinalNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log.1")
	content := "[12:00:00] [INFO] crlf line\r\n[12:00:01] [INFO] tail without newline"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	key := testKey("src-text-crlf", "g1")
	led := ledger.New()
	res, err := NewArchiveImporter(led, key).ImportText(path)
	require.NoError(t, err)
	require.Len(t, res.Events, 2, "末行无换行同样必须产出事件（截断不得静默丢尾）")
	require.Equal(t, "[12:00:00] [INFO] crlf line\r", res.Events[0].Message,
		"行尾按既有口径只剥离 \\n（\\r 留给下游归一处理，与 gzip 路径同源）")
	require.Equal(t, "[12:00:01] [INFO] tail without newline", res.Events[1].Message)
	require.False(t, res.Partial)
	require.Equal(t, ledger.SegmentRotatedText, archiveSegmentOf(led, key, path).Kind)
}
