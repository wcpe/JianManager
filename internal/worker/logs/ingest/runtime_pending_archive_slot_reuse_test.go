package ingest

// 本文件守住 `pendingArchives` 的**全部三个判据分支**（2026-10-22 取证）。
//
// 背景（归档「已导入」判定的键只有路径）：
//   - `Imported=true` 的段：按**路径**判已导入，不做任何内容/时间戳复核；
//   - `ImportError != ""` 的段：按「size + mtime 与登记值一致」判「还是那份坏文件」，
//     变了才重新待导入（隔离 + 变化后重试，见 acquire.markFailed）。
//
// 取证结论（详见各用例注释与 tests 报告）：
//  1. 轮转序号复用（`proxy.log.1` 被换成新内容）**确实**会命中 Imported 分支而永久跳过；
//     但它同时是**避免重复导入的保护**：BungeeCord 的移位改名形态下，槽位里的新内容通常
//     正是原活文件内容，而那份内容已由 FileTailer 读过并投递。在 ingest 侧无法区分
//     「已被 tail 覆盖」与「从未被读过」，故此处**刻意保持保守**（宁可跳过也不重复导入）。
//  2. `ImportError` 分支的判据是可测的，且行为正确：文件未变则继续隔离、size 或 mtime
//     变化则重新待导入。本文件把这条钉住，防止有人把它"简化"成恒跳过（= 永久丢归档）
//     或恒放行（= 每轮重试同一份坏归档、缺口噪声）。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// bungeeSlotSource 造一个 BungeeCord 形态的源（活文件 proxy.log.0，轮转产物 proxy.log.N）。
func bungeeSlotSource(t *testing.T, dir string) (SourceConfig, string) {
	t.Helper()
	live := filepath.Join(dir, "proxy.log.0")
	return SourceConfig{
		LogSourceID:      "node:bungee-slot",
		SourceGeneration: "g1",
		Path:             live,
		Mode:             pipeline.ModeFilePrimary,
		StorageNamespace: "ns:bungee-slot",
		UTCDay:           runtimeTestUTCDay(),
	}, live
}

// newSlotPipeline 建一个只有单个 bungee 源的管理器，并返回该源的 pipeline/键。
func newSlotPipeline(t *testing.T, src SourceConfig) (*Manager, *pipeline.Pipeline, string) {
	t.Helper()
	client, _ := newProjectionVL(t)
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{
		Root: filepath.Dir(src.Path), VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{src},
	})
	require.NoError(t, err)
	key := src.LogSourceID + "/" + src.SourceGeneration
	p, ok := m.pipeFor(key)
	require.True(t, ok, "源必须已注册 pipeline")
	return m, p, key
}

// TestPendingArchivesReusedRotationSlotIsSkippedByPathKey 记录**已知缺口**：
// 同一路径上的归档内容已换（长度也变），本函数仍按「路径已导入」跳过。
//
// 为什么把它写成断言而不是"修掉"：`imported` 键只有路径，而 ingest 侧看不到"这份内容
// 是否已被活文件 tail 读过"——那要看 acquire 的轮转关联与覆盖前缀。BungeeCord 的
// java.util.logging 移位改名（proxy.log.0 → proxy.log.1 → proxy.log.2 …）让槽位里的
// 新内容**通常就是已被 tail 投递过的原活文件内容**，此时重导即重复入库；而
// `acquire.importArchive` 对「该路径已有 Imported 段」是**整包跳过**
// （reason: rotation already linked; whole-archive reimport forbidden），
// 所以只在 ingest 侧放行也导不进来，只会把 scanned 指标变成噪声。
//
// 真实的丢失窗口（本用例**不**覆盖，需跨包修复）：worker 停机期间跨过一次以上轮转时，
// 复用槽位里的内容**从未被任何链路读过**，而本判据会把它判为已导入 ⇒ 静默丢日志。
// 根治需要「导入成功时把内容身份（ledger.Segment.ArchiveObjectID 已存在）落账，并在
// acquire.importArchive 的整包跳过处比对，不同则按 covered-prefix 语义重导」。
func TestPendingArchivesReusedRotationSlotIsSkippedByPathKey(t *testing.T) {
	dir := t.TempDir()
	src, live := bungeeSlotSource(t, dir)
	require.NoError(t, os.WriteFile(live, []byte("[12:00:00] [Server thread/INFO]: live\n"), 0o644))
	slot := filepath.Join(dir, "proxy.log.1")
	require.NoError(t, os.WriteFile(slot, []byte("[11:00:00] [Server thread/INFO]: first\n"), 0o644))

	_, p, _ := newSlotPipeline(t, src)

	// 造出「上一轮已把这槽位导完」的状态：Imported=true 且位置覆盖整份文件。
	require.NoError(t, p.Ledger().RegisterSegment(p.Key(), ledger.Segment{
		Path: slot, Kind: ledger.SegmentRotatedText,
		StartPos: 0, EndPos: uint64(len("[11:00:00] [Server thread/INFO]: first\n")),
		Imported: true, ParserVersion: "acquire-v1",
	}))
	require.Empty(t, pendingArchives(p, []string{slot}), "同一份内容重复发现必须跳过（幂等）")

	// 槽位复用：内容与长度都换成新的。
	require.NoError(t, os.WriteFile(slot,
		[]byte("[12:30:00] [Server thread/INFO]: second cycle a\n[12:30:01] [Server thread/INFO]: second cycle b\n"), 0o644))

	require.Empty(t, pendingArchives(p, []string{slot}),
		"已知缺口：路径键判据会把复用槽位的新内容判为已导入（跨包修复前保持保守：不重复导入）")
}

// TestPendingArchivesFailedArchiveRependsOnlyAfterFileChanges 钉住 `ImportError` 分支的
// 完整判据：坏归档被隔离后，只有**文件本身变了**（size 或 mtime）才重新待导入。
//
// 为什么两个方向都必须断言：
//   - 恒跳过 ⇒ 归档修好后永远不再尝试，等于永久丢；
//   - 恒放行 ⇒ 每轮重试同一份坏归档，缺口噪声 + 空转读盘（`markFailed` 设计意图正是消除它）。
func TestPendingArchivesFailedArchiveRependsOnlyAfterFileChanges(t *testing.T) {
	dir := t.TempDir()
	src, live := bungeeSlotSource(t, dir)
	require.NoError(t, os.WriteFile(live, []byte("[12:00:00] [Server thread/INFO]: live\n"), 0o644))
	bad := filepath.Join(dir, "proxy.log.1")
	require.NoError(t, os.WriteFile(bad, []byte("corrupt-ish\n"), 0o644))
	info, err := os.Stat(bad)
	require.NoError(t, err)

	_, p, _ := newSlotPipeline(t, src)
	// 造出「已隔离」的失败态：Observed 记的是当前 size/mtime（与 acquire.markFailed 一致）。
	require.NoError(t, p.Ledger().RegisterSegment(p.Key(), ledger.Segment{
		Path: bad, Kind: ledger.SegmentRotatedText, StartPos: 0, EndPos: 0,
		ImportError:  "ARCHIVE_READ_ERROR: boom",
		ObservedSize: info.Size(), ObservedModNano: info.ModTime().UnixNano(),
		ParserVersion: "acquire-v1",
	}))

	require.Empty(t, pendingArchives(p, []string{bad}),
		"文件未变时不得重试（否则每轮都重试同一份坏归档）")

	// ① size 变化 ⇒ 重新待导入。
	require.NoError(t, os.WriteFile(bad, []byte("corrupt-ish but longer now\n"), 0o644))
	require.Equal(t, []string{bad}, pendingArchives(p, []string{bad}),
		"size 变了必须重新待导入（归档被修好/被替换的唯一可见信号）")

	// ② 重置 Observed 后只动 mtime（size 不变）⇒ 也必须重新待导入。
	info, err = os.Stat(bad)
	require.NoError(t, err)
	require.NoError(t, p.Ledger().RegisterSegment(p.Key(), ledger.Segment{
		Path: bad, Kind: ledger.SegmentRotatedText, StartPos: 0, EndPos: 0,
		ImportError:  "ARCHIVE_READ_ERROR: boom",
		ObservedSize: info.Size(), ObservedModNano: info.ModTime().UnixNano(),
		ParserVersion: "acquire-v1",
	}))
	require.Empty(t, pendingArchives(p, []string{bad}), "对照：先确认这一刻判为未变")
	later := info.ModTime().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(bad, later, later))
	require.Equal(t, []string{bad}, pendingArchives(p, []string{bad}),
		"mtime 变了也必须重新待导入（原地改写/运维重写后的重试入口）")
}

// TestPendingArchivesStatFailureKeepsFailedArchivePending 钉住「stat 失败」的处置方向。
//
// 为什么必须是「继续待导入」而不是「判为已导入」：文件不可读只是**当下**的事实，
// 把它当成「没变、别再试了」会让归档永久停摆；反之继续待导入最坏只是每轮多一次 stat。
// 这条同时解释了零值 Observed 的真实影响：它只会让判据**永不相等**（不会「零值恒等」
// 而误判已导入），因为真实文件的 mtime 纳秒值不可能是 0。
func TestPendingArchivesStatFailureKeepsFailedArchivePending(t *testing.T) {
	dir := t.TempDir()
	src, live := bungeeSlotSource(t, dir)
	require.NoError(t, os.WriteFile(live, []byte("[12:00:00] [Server thread/INFO]: live\n"), 0o644))

	_, p, _ := newSlotPipeline(t, src)
	// 登记一个**不存在**的失败归档（模拟 stat 必然失败），并刻意用零值 Observed。
	missing := filepath.Join(dir, "proxy.log.9")
	require.NoError(t, p.Ledger().RegisterSegment(p.Key(), ledger.Segment{
		Path: missing, Kind: ledger.SegmentRotatedText,
		ImportError: "ARCHIVE_UNREADABLE: gone",
	}))

	require.Equal(t, []string{missing}, pendingArchives(p, []string{missing}),
		"stat 失败必须保持待导入（零值 Observed 不会与真实文件恒等）")
}
