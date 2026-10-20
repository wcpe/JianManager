package pipeline

// 归档形态分流（pipeline.ImportArchive）：按**文件头**把纯文本轮转归档与 gzip 归档分到各自的
// 导入实现，两者共用同一条下游链路（WAL → 投递 → 投影校验 → 账本标记）。
//
// 为什么分流点必须在 pipeline 而不是让导入器自己猜：导入器是「已选定形态之后」的执行者，
// 而形态判定要读文件头（一次额外的 open/read），属于「要不要处理这个文件」的决策；
// 把它放在 ImportArchive 这一层，形态判定与预算/回收/采样等准入决策处在同一处，
// 且分流结果可被单点断言（本文件的用例即断言「哪条实现被选中」）。

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// segmentKindOf 返回账本里该路径分段的形态（不存在返回空串）。
func segmentKindOf(t *testing.T, p *Pipeline, path string) ledger.SegmentKind {
	t.Helper()
	entry := p.Ledger().Get(p.Key())
	if entry == nil {
		return ""
	}
	for _, seg := range entry.Segments {
		if filepath.Clean(seg.Path) == filepath.Clean(path) {
			return seg.Kind
		}
	}
	return ""
}

// gapReasons 返回当前源的缺口原因列表。
func gapReasons(p *Pipeline) []string {
	gaps := p.Gaps()
	out := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		out = append(out, gap.Reason)
	}
	return out
}

// writePlainArchive 写一个纯文本轮转归档（无扩展名，逐行 \n 结尾）。
func writePlainArchive(t *testing.T, path string, lines []string) {
	t.Helper()
	content := ""
	for _, ln := range lines {
		content += ln + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// TestImportArchiveDispatchesByFileHeader 是形态分流的主回归（可变异转红）。
//
// 同一条管道上先后导入两种形态，断言「形态 → 实现 → 账本 kind」三者一致：
//   - 纯文本（`proxy.log.1`，**无扩展名**）：走纯文本实现 ⇒ kind=rotated_text，且产出正确正文；
//   - gzip（按魔数识别，与扩展名无关）：走 gzip 实现 ⇒ kind=gzip。
//
// 转红方式（实测）：把 ImportArchive 的分流改回「一律 ImportGzipBounded」，第一条断言即红
// （纯文本归档被当作 gzip ⇒ 0 事件 + ARCHIVE_GZIP_CORRUPT 缺口）。
func TestImportArchiveDispatchesByFileHeader(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "proxy.log.0")
	require.NoError(t, os.WriteFile(live, nil, 0o644))

	var got []logtypes.Event
	p := newFilePipeline(t, live, "src-dispatch", "g1", okDelivery(&got))

	txtPath := filepath.Join(dir, "proxy.log.1")
	writePlainArchive(t, txtPath, []string{"[12:00:00] [INFO] rotated plain one"})
	res, err := p.ImportArchive(txtPath)
	require.NoError(t, err)
	require.False(t, res.Skipped, "纯文本轮转归档必须被导入（不是被跳过、更不是失败）")
	require.Len(t, res.Events, 1)
	require.Contains(t, res.Events[0].Message, "rotated plain one")
	require.Equal(t, ledger.SegmentRotatedText, segmentKindOf(t, p, txtPath),
		"纯文本归档必须登记为 rotated_text，而不是 gzip")
	require.Empty(t, gapReasons(p),
		"纯文本轮转不得留下缺口（尤其不得是 ARCHIVE_GZIP_CORRUPT：该原因不可自动消解，源暂停即永久停采）")

	// 压缩归档（名字里没有日期也无所谓，形态按文件头判定）仍走 gzip 实现。
	gzPath := filepath.Join(dir, "proxy.log.2")
	writeGzipLines(t, gzPath, []string{"[12:00:05] [INFO] gzip line"})
	resGz, err := p.ImportArchive(gzPath)
	require.NoError(t, err)
	require.False(t, resGz.Skipped)
	require.Len(t, resGz.Events, 1)
	require.Contains(t, resGz.Events[0].Message, "gzip line")
	require.Equal(t, ledger.SegmentGzip, segmentKindOf(t, p, gzPath),
		"内容是 gzip 的归档必须登记为 gzip（形态由文件头判定，不看扩展名）")
	require.Empty(t, gapReasons(p))

	require.Len(t, got, 2, "两种形态的事件都必须真的走完投递")
}

// TestImportArchiveTextIsBoundedAcrossRounds 守住纯文本路径的有界分批与跨轮续传。
//
// 形态与现场一致：归档事件数（1200）是单源上限（300）的 4 倍。断言：
//  1. 归档被**完整**导入（总数守恒：不丢、不重）；
//  2. 每轮放行量 ≤ 上限一半（预算由生效上限派生，不是写死的常数）；
//  3. 追加之后即刻不越闸、不暂停（有界性不得因新形态退化）；
//  4. 真的分了多轮（否则等同于修复前的一次灌入）；
//  5. 读完才置已导入，且第二次扫描被跳过（同一文件不会重复导入）。
func TestImportArchiveTextIsBoundedAcrossRounds(t *testing.T) {
	const (
		totalEvents = 1200
		walMax      = int64(300)
	)
	dir := t.TempDir()
	path := filepath.Join(dir, "proxy.log.1")
	lines := make([]string, 0, totalEvents)
	for i := 0; i < totalEvents; i++ {
		lines = append(lines, fmt.Sprintf("[12:%02d:%02d] [Server thread/INFO]: rotated line %d",
			(i/60)%60, i%60, i))
	}
	writePlainArchive(t, path, lines)

	var (
		p                *Pipeline
		peakBacklog      int64
		pausedAtDelivery bool
	)
	p = newLimitedPipeline(t, walMax, FuncHook(func(events []logtypes.Event, replay bool) (DeliveryResult, error) {
		if backlog, _ := p.wal.Backlog(); backlog > peakBacklog {
			peakBacklog = backlog
		}
		if entry := p.Ledger().Get(p.Key()); entry != nil && entry.AcquirePaused {
			pausedAtDelivery = true
		}
		return DeliveryResult{HTTPStatus: 200}, nil
	}))

	delivered, perRound, err := drainArchive(t, p, path, 64)
	require.NoError(t, err, "纯文本大归档必须能被跨轮完整导入（不得因越闸暂停而卡死）")
	require.Len(t, delivered, totalEvents, "跨轮收集的事件总数必须守恒（不丢、不重）")

	limit := int(walMax / 2)
	for index, size := range perRound {
		require.LessOrEqual(t, size, limit,
			"第 %d 轮放行 %d 条超过上限一半 %d：单次追加不得把源推过闸", index, size, limit)
	}
	require.Greater(t, len(perRound), 1, "归档远大于上限时必须真的分多轮（否则等同于一次灌入）")
	require.False(t, pausedAtDelivery, "投递时刻源已处于暂停态：本次追加越闸了")
	require.LessOrEqual(t, peakBacklog, walMax, "追加后的积压峰值不得越过上限")
	require.True(t, archiveImported(t, p, path), "读完的纯文本归档必须被标为已导入")
	require.Equal(t, ledger.SegmentRotatedText, segmentKindOf(t, p, path))

	// 第二次扫描被跳过：不再产出任何事件（同一文件不会重复导入）。
	again, err := p.ImportArchive(path)
	require.NoError(t, err)
	require.True(t, again.Skipped, "已导入的纯文本归档必须被跳过")
	require.Empty(t, again.Events)
	require.Len(t, delivered, totalEvents)
}

// TestImportArchiveTextDegenerateFormsLeaveNoUnhealableGap 守住退化形态的缺口红线。
//
// 现场形态：bungee 的归档 glob `proxy.log.[1-9]*` 会匹配到 java.util.logging 的 0 字节锁文件
// （proxy.log.N.lck）。它们不是归档，但绝不能走 gzip 路径：那会给每个锁文件留一条
// ARCHIVE_GZIP_CORRUPT 缺口，而该原因不在自动消解白名单（ledger/gap_bounds.go），
// 源一旦被暂停就永久停采——这正是本能力要消灭的形态。
func TestImportArchiveTextDegenerateFormsLeaveNoUnhealableGap(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "proxy.log.0")
	require.NoError(t, os.WriteFile(live, nil, 0o644))
	p := newFilePipeline(t, live, "src-degenerate", "g1", NopHook{})

	lck := filepath.Join(dir, "proxy.log.1.lck")
	require.NoError(t, os.WriteFile(lck, nil, 0o644))
	res, err := p.ImportArchive(lck)
	require.NoError(t, err, "0 字节锁文件不得硬失败（否则会拖死整轮采集）")
	require.True(t, res.Skipped)
	require.Empty(t, res.Events)
	require.Empty(t, gapReasons(p), "空轮转文件不得留下任何缺口")
	require.Equal(t, ledger.SegmentRotatedText, segmentKindOf(t, p, lck))

	// 第二次扫描：空归档已被记为已导入 ⇒ 跳过（不会每轮重试）。
	again, err := p.ImportArchive(lck)
	require.NoError(t, err)
	require.True(t, again.Skipped)

	require.NotContains(t, gapReasons(p), ledger.GapReasonArchiveGzipCorrupt,
		"ARCHIVE_GZIP_CORRUPT 不可自动消解，绝不允许出现在轮转文件的常规路径上")
}
