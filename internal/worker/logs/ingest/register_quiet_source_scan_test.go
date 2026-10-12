package ingest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// removeSegmentFiles 删除某源事件段目录下的段文件（保留 MANIFEST.json），
// 使任何真实遍历都必然失败。
//
// 这是本用例的判别器（已实测确认）：`eventstore.Iterate` 先按 key 取 section，
// 再逐段 `readSegment` ⇒ `os.Open` 失败即返回 `ErrCorrupt`，`eventBodyLookup` 随即打
// 「WAL 引用恢复：遍历事件段存储失败」Warn。于是「是否真的遍历过事件段」变成**可观测事实**，
// 不依赖计时、内部计数器或对实现细节的假设。
//
// 注意必须**保留 MANIFEST.json**：`Open` 靠清单内的 key 建 byKey 索引；目录整体不存在时
// `Iterate` 会因 byKey 未命中而**静默返回 nil**（不打日志），判别器就失效了。
func removeSegmentFiles(t *testing.T, eventsRoot, key string) {
	t.Helper()
	// 目录名按 eventstore 的命名惯例做字符替换（`/`→`_`、`:`→`-`，见 sectionDirName）。
	dirName := strings.NewReplacer("/", "_", "\\", "_", ":", "-").Replace(key)
	dir := filepath.Join(eventsRoot, dirName)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	removed := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ndjson") {
			require.NoError(t, os.Remove(filepath.Join(dir, e.Name())))
			removed++
		}
	}
	require.Positive(t, removed, "前置：段目录 %s 必须已有段文件，否则探针失去判别力", dir)
}

// 回归（N-2，2026-10-12 终审修复）：**安静源**（索引里的 WAL 条目全是内联形式、无引用条目）
// 登记时不得触发事件段遍历——`eventBodyLookup` 必须**按需**构造，而不是无条件预构造。
//
// 现场形态：`eventstore.Iterate` 逐段 `readSegment` 做磁盘全量遍历（生产单源实测
// 126MB / 135113 行、单次解析约 1.7s）。旧形态在 `m.mu` 之前**无条件**构造该函数，
// 于是 WAL 已排空的安静源（`WALRefs` 为空 ⇒ `RestoreMixed` 根本不需要 hydrate）也要白付
// 一次全段磁盘读 + 对全部事件建 map 的常驻内存；而启动路径对全部源**串行**登记
// （`New → Register`），60 源即多出分钟级启动时间与 GB 级内存峰值（与 e8e44105 事故同类）。
//
// 转红方式（实测）：把 Register 里 `needHydrate` 的两段式判断改回无条件
// `bodyLookup := m.eventBodyLookup(key)` ⇒ 断言①立即红（安静源出现遍历失败日志）。
func TestRegisterSkipsEventScanForQuietSource(t *testing.T) {
	const scanWarn = "遍历事件段存储失败"

	root := t.TempDir()
	cat := catalog.New(catalog.NewMemJournal())
	m, err := New(Options{Root: root, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Stop() })

	eventsRoot := filepath.Join(root, "var", "log", "events")
	quietKey := "inst:quiet/file"
	refKey := "inst:withrefs/file"

	// 用 Manager 自己的 store 造段（保证清单已落盘、byKey 已建立），再删段文件。
	// 两个源都造同一形态，差别只在其索引状态（见下方对照臂）。
	//
	// **段 key 必须含代次**（`<ns>/<generation>`）：生产的事件段落键取自 `m.state.Sources` 的键
	// （见 appendEvents 的调用点），即带 `/g1` 后缀的 pipe key；`eventBodyLookup` 也按同一口径
	// 查询。若这里用裸命名空间造段，`Iterate` 会因 byKey 未命中而**静默返回 nil**（不当成失败），
	// 判别器就失效了。
	for _, ns := range []string{quietKey, refKey} {
		require.NoError(t, m.events.Append(ns+"/g1", []logtypes.Event{{
			EventID: ns + "-1", Record: logtypes.RecordRange{Start: 1, End: 9},
			EventTimeUTC: "2026-10-11T00:00:01Z", Message: "seed",
		}}))
	}
	for _, ns := range []string{quietKey, refKey} {
		removeSegmentFiles(t, eventsRoot, ns+"/g1")
	}

	// 对照臂的索引状态：该源的 WAL 条目全是**引用形式** ⇒ RestoreMixed 需要 hydrate
	// ⇒ 必须触发事件段遍历。直接塞进内存索引（模拟重启后从索引恢复的场景）。
	m.mu.Lock()
	m.state.Sources[refKey+"/g1"] = persistedSource{
		WALRefs: []acquire.WALRef{{Seq: 1, EventID: refKey + "-1", RecordStart: 1, RecordEnd: 9, Durable: true}},
	}
	m.mu.Unlock()

	quiet := SourceConfig{
		LogSourceID: quietKey, SourceGeneration: "g1",
		Path: filepath.Join(root, "quiet.log"), Mode: pipeline.ModeFilePrimary,
		StorageNamespace: quietKey, UTCDay: runtimeTestUTCDay(),
	}
	require.NoError(t, os.WriteFile(quiet.Path, []byte("[12:00:01] [Server thread/INFO]: x\n"), 0o644))

	var buf bytes.Buffer
	restore := captureSlogOutput(&buf)
	defer restore()

	require.NoError(t, m.Register(quiet))

	// ① 核心断言：安静源（无引用条目）不得触碰事件段存储。
	require.NotContains(t, buf.String(), scanWarn,
		"安静源（无 WALRefs）登记不得触碰事件段存储：旧形态无条件预构造 hydrate 函数 ⇒ "+
			"白付一次 O(段行数) 磁盘遍历（生产 126MB/135k 行；启动 60 源串行 ⇒ 分钟级 + GB 级内存峰值）")

	// ② 对照：带引用条目的源**必须**触发遍历（证明探针本身有效，而非恒不触发）。
	buf.Reset()
	withRefs := SourceConfig{
		LogSourceID: refKey, SourceGeneration: "g1",
		Path: filepath.Join(root, "refs.log"), Mode: pipeline.ModeFilePrimary,
		StorageNamespace: refKey, UTCDay: runtimeTestUTCDay(),
	}
	require.NoError(t, os.WriteFile(withRefs.Path, []byte("[12:00:01] [Server thread/INFO]: y\n"), 0o644))
	require.NoError(t, m.Register(withRefs))
	require.Contains(t, buf.String(), scanWarn,
		"带引用条目的源必须触发事件段遍历（按需判断不得把该路径也省掉——"+
			"那会让引用条目取不到正文，反而被误记成真实数据丢失）")
}
