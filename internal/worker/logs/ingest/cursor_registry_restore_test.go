package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/eventstore"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestSourceSurvivesRestartWithoutOptionsSources 是「源配置跨重启存活」的核心验收（2026-10-30 第二轮）。
//
// 缺陷形态（真机探针）：源只在 `Options.Sources` / `instances.go` 登记时进入内存，而索引库是
// `:memory:` ⇒ 重启后 `SourceConfigs=0 pipes=0`，而游标表里明明记着「那个源读到哪」。
// 结果：游标变成一条没人用的死数据，实例源**不再被采集**。
//
// 本用例就是那个探针的形状：阶段一给一个静态源并采集一轮；阶段二**不传 Sources** 重启，
// 断言源被重建出来、且游标接得上（不重读旧字节）。
func TestSourceSurvivesRestartWithoutOptionsSources(t *testing.T) {
	client, _ := newFlakyVL(t)
	// root 的**最后一段名字**必须唯一，且事件段目录要自己清理。
	//
	// 两件事都必须：eventstore.MemoryRoot 用 filepath.Base(Root) 做 tmpfs 目录名，
	// 而事件段**不在** root 里（在 /dev/shm）⇒ t.TempDir() 只清 root、不清段。用固定名字会
	// 在多次运行间残留上一次的段，本用例数的「历史行条数」基线就会被上一轮污染
	// （实测：同一基线在残留目录上数出两倍 ⇒ 误报「游标未生效」）。
	root := filepath.Join(t.TempDir(), uniqueSegmentRoot(t, "cursor-restore"))
	require.NoError(t, os.MkdirAll(root, 0o755))
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:persist/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:persist", UTCDay: runtimeTestUTCDay(),
	}
	key := source.LogSourceID + "/" + source.SourceGeneration

	before, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	require.Contains(t, before.pipes, key, "夹具：首轮必须有这个源")
	// 先登记、后产日志（新源默认从文件末尾起读，见裁定 D）。
	appendLogLine(t, logPath, "[02:56:53] [Server thread/INFO]: 历史行\n")
	appendLogLine(t, logPath, "[02:56:54] [Server thread/INFO]: 历史行二\n")
	before.pollOnce()
	// 重启前就存在的区间不得增条（重读会让同一区间再造一条）。
	//
	// 两个坑都得避开：
	//   - 不数“历史行”的条数：normalize 以「下一条事件头」闭合多行事件，最后一行总在缓冲里
	//     等下一个边界，重启后那条才落库（实测 baseline=1，重启后=2），条数天然会变；
	//   - 不比对区间**集合**：同一个 root（同一测试）下 before/after 共用一个事件段目录
	//     （eventstore 按 root 取名）⇒ after 的迭代结果里本来就包含 before 写的那些事件，
	//     集合法会把它们全当重读。
	//
	// 真正干净的不变量是「同一位置区间不得超过一条」：单进程内每个逻辑区间只被消费一次
	// （游标前进），出现两条就只能是游标没生效重读。它不受共目录与延迟闭合影响。
	checkpoint := eventRangeCounts(t, before, key)
	require.NotEmpty(t, checkpoint, "夹具：首轮必须真的落库过事件")
	require.NoError(t, before.Stop())

	// 阶段二：**不传 Sources**——源只能从游标表恢复。
	after, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
	})
	require.NoError(t, err)
	require.Contains(t, after.pipes, key, "重启后源必须被重建（原先这里是 0）")
	require.Equal(t, source.Path, effectiveConfigOf(t, after, key).Path)
	require.Equal(t, logtypes.SourceInstance, effectiveConfigOf(t, after, key).SourceCategory,
		"恢复出来的源必须带着类别（实例源要走 recalcInstanceSourceConfig）")
	require.Equal(t, source.StorageNamespace, effectiveConfigOf(t, after, key).StorageNamespace)

	// 游标必须接得上：新增一行后只应读到那一行，而不是重读前三行。
	cursor := after.resumeCursorFor(key, logPath)
	require.NotNil(t, cursor, "重启后游标必须仍然可用")
	require.EqualValues(t, fileSize(t, logPath), cursor.Cursor, "游标必须等于重启前已读字节")

	appendLogLine(t, logPath, "[02:56:55] [Server thread/INFO]: 重启后的新行\n")
	appendLogLine(t, logPath, "[02:56:56] [Server thread/INFO]: 闭合行\n")
	after.pollOnce()
	// 游标必须真的生效：重启后不得把重启前的字节再采一遍。
	for r, count := range eventRangeCounts(t, after, key) {
		require.LessOrEqual(t, count, 1,
			"恢复后的源重读了重启前的字节（位置区间 %d-%d 被重复采集：重启前 %d 条、重启后 %d 条）",
			r.start, r.end, checkpoint[r], count)
	}
	// 并且必须真的继续采新内容（新事件的位置在重启前的游标之后）。
	fresh := 0
	for _, ev := range durableEvents(t, after, key) {
		if ev.Record.Start >= uint64(cursor.Cursor) && strings.Contains(ev.Message, "重启后的新行") {
			fresh++
		}
	}
	require.NotZero(t, fresh, "恢复后的源必须继续采新内容（且新事件位置在游标之后）")
}

// eventRange 是一个事件在逻辑源上的位置区间。
type eventRange struct{ start, end uint64 }

// eventRangeCounts 返回「位置区间 → 事件条数」。同一区间的条数超过 1 就说明发生了重读。
func eventRangeCounts(t *testing.T, m *Manager, key string) map[eventRange]int {
	t.Helper()
	out := make(map[eventRange]int)
	for _, ev := range durableEvents(t, m, key) {
		out[eventRange{start: ev.Record.Start, end: ev.Record.End}]++
	}
	return out
}

// uniqueSegmentRoot 生成一个**进程内与跨运行都唯一**的事件段目录名，并登记清理。
//
// 为什么要它：eventstore.MemoryRoot(filepath.Base(opts.Root)) 把事件段放到 /dev/shm 下，
// 而这个目录**不在** t.TempDir() 的清理范围内。t.TempDir() 的 basename 恒为 "001"，多个用例
// 于是共用一个段根；即使自己包一层，固定名字也会跨运行残留。
// 两者都会让「按事件条数做断言」的用例变得时好时坏。
func uniqueSegmentRoot(t *testing.T, prefix string) string {
	t.Helper()
	name := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	t.Cleanup(func() { _ = os.RemoveAll(eventstore.MemoryRoot(name)) })
	return name
}

// TestV1CursorRegistryKeepsCursorButSkipsSourceRebuild 是向后兼容用例（v1 ⇒ v2 升级路径）。
//
// 现场形态：升级前落盘的 `ingest.cursor` 只有 6 列（key/path/dev/ino/cursor/offset）。
// 升级后启动必须：
//   - 不报错（否则升级即停采）；
//   - 游标照常生效（否则会从文件头重读整份历史日志并重投 VL）；
//   - **不**据此重建源（6 列里没有 mode/类别/命名空间，拿它建源只能靠猜，
//     而猜错的后果是读错文件——比不重建更糟）。
func TestV1CursorRegistryKeepsCursorButSkipsSourceRebuild(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	content := "line-1\nline-2\n"
	require.NoError(t, os.WriteFile(logPath, []byte(content), 0o644))
	key := "inst:v1/file/g1"

	// 手写一份 v1 格式文件（6 列，与 f5b882f0 的写出格式逐字节一致）。
	cursorPath := filepath.Join(root, "var", "log", "ingest.cursor")
	require.NoError(t, os.MkdirAll(filepath.Dir(cursorPath), 0o755))
	v1 := "# jianmanager ingest cursor registry v1\n" +
		key + "\t" + logPath + "\t1\t2\t" + strconv.Itoa(len(content)) + "\t" + strconv.Itoa(len(content)) + "\n"
	require.NoError(t, os.WriteFile(cursorPath, []byte(v1), 0o644))

	// 读得回来、不报错。
	loaded := loadCursorRegistry(cursorPath)
	require.Len(t, loaded, 1, "v1 行必须能读（不能整表失败）")
	require.Equal(t, logPath, loaded[key].Path)
	require.EqualValues(t, len(content), loaded[key].Cursor)
	require.Nil(t, loaded[key].Source, "v1 行不得凭空造出源配置")

	// 进 Manager：游标生效，但不重建源。
	m := &Manager{root: root, pipes: map[string]*pipeline.Pipeline{}, sources: map[string]SourceConfig{}}
	m.loadCursorRegistryIntoManager()
	require.NotNil(t, m.resumeCursorFor(key, logPath), "v1 游标必须照常生效")
	require.Empty(t, m.sourcesFromCursorRegistry(), "v1 行不得被当作源登记表")
}

// fileSize 返回文件当前字节数（断言游标等于已读字节）。
func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size()
}
