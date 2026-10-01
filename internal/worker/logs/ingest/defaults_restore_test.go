package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 生产实证（2026-10-02 真机复验）：节点级默认时区**没有落到「从索引恢复」的源上**。
//
// 现场：部署含时区接线的版本 + `worker.yml` 配 `log_ingest: time_zone: local` 并重启后，
// 新入库条目仍是 +8h（日志行 `[02:56:53]`（HKT）落库为 `2026-10-02T02:56:53Z`，应为
// `2026-10-01T18:56:53Z`）——即恢复出来的源仍在按 UTC 解释 `[HH:MM:SS]`。
//
// 本用例复现生产形态：**阶段一**用「接线前」的节点配置（没有默认时区）登记并落库一个源，
// 使其持久化配置里 `TimeZone` 为空；**阶段二**带节点默认时区重启，且**不再把该源放进
// `Options.Sources`**（实例源正是这种形态：重启后只能从索引恢复）。
//
// 转红：恢复路径不把节点默认应用到「配置里为空的字段」→ 新事件仍是 `T02:56:53Z`（未换算）→ 必红。
func TestDefaultTimeZoneAppliesToRestoredSource(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[02:56:53] [Server thread/INFO]: 历史行\n[02:56:54] [Server thread/INFO]: 历史行二\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:tz-restore/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:tz-restore", UTCDay: runtimeTestUTCDay(),
	}
	key := source.LogSourceID + "/" + source.SourceGeneration

	// 阶段一：接线前的节点（无默认时区）——源的持久化配置里 TimeZone 为空。
	before, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	before.pollOnce()
	require.Equal(t, "", savedConfigOf(t, before, key).TimeZone, "夹具：历史状态的源配置里 TimeZone 为空（=UTC）")
	require.NoError(t, before.Stop())

	// 阶段二：带节点默认时区重启，源**只从索引恢复**（不在 Options.Sources 里）。
	// 两行起步：normalize 以「下一条事件头」闭合多行事件，只追加一行会停在缓冲里不产生投递。
	appendLogLine(t, logPath, "[02:56:55] [Server thread/INFO]: 重启后的新行\n")
	appendLogLine(t, logPath, "[02:56:56] [Server thread/INFO]: 闭合行\n")
	after, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		DefaultTimeZone: "Asia/Hong_Kong",
	})
	require.NoError(t, err)
	after.pollOnce()

	events := durableEvents(t, after, key)
	require.NotEmpty(t, events)
	var fresh []string
	for _, event := range events {
		if strings.Contains(event.Message, "重启后的新行") {
			fresh = append(fresh, event.EventTimeUTC)
		}
	}
	require.NotEmpty(t, fresh, "夹具：重启后的新行必须已落库")
	for _, when := range fresh {
		require.True(t, strings.HasSuffix(when, "T18:56:55Z"),
			"恢复出来的源必须拿到节点默认时区：HKT 02:56:55 应落库为 2026-10-01T18:56:55Z，实得 %s", when)
	}
	// 只有**阶段二之后**落库的事件带 event_time_zone（阶段一的历史事件是按 UTC 解析的，
	// 语义上不该有该字段）；用新鲜事件断言「继承的默认确实一路走到了归一化」。
	for _, event := range events {
		if !strings.Contains(event.Message, "重启后的新行") {
			continue
		}
		require.Equal(t, "Asia/Hong_Kong", event.Fields[normalize.FieldEventTimeZone],
			"恢复出来的源必须记账 event_time_zone（否则整源时间轴仍按 UTC 解释）")
	}
}

// TestDefaultCharsetAppliesToRestoredSource 是上一条的**同构**用例（复审新增键 log_ingest.charset）：
// 恢复路径同样必须把节点默认字符集应用到「配置里为空」的源，否则历史源永远停在 auto。
//
// 生产含义：把 `log_ingest.charset: gbk` 配上并重启后，从索引恢复的源若仍按 auto 解码，
// 中文 locale 的 JVM 日志（纯 ASCII 与中文混排）只能靠粘滞启发式判定——显式声明被静默忽略。
//
// 转红：恢复路径不把节点默认应用到空字符集 → 该源仍按 auto（本用例断言显式 `gbk` 口径）→ 必红。
func TestDefaultCharsetAppliesToRestoredSource(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[02:56:53] [Server thread/INFO]: 历史行\n[02:56:54] [Server thread/INFO]: 历史行二\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:charset-restore/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:charset-restore", UTCDay: runtimeTestUTCDay(),
	}
	key := source.LogSourceID + "/" + source.SourceGeneration

	before, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	before.pollOnce()
	require.Equal(t, "", savedConfigOf(t, before, key).Charset, "夹具：历史状态的源配置里 Charset 为空（=auto）")
	require.NoError(t, before.Stop())

	// 重启后的新行是 GBK 编码：显式 gbk 与 auto 的分歧点在于**记账口径**（显式声明用 gbk）。
	encoded, _, err := gbkEncode("[02:56:55] [Server thread/INFO]: 重启后的新行\n" +
		"[02:56:56] [Server thread/INFO]: 闭合行\n")
	require.NoError(t, err)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.Write(encoded)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	after, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		DefaultCharset: "gbk",
	})
	require.NoError(t, err)
	after.pollOnce()

	require.Equal(t, "gbk", effectiveConfigOf(t, after, key).Charset,
		"恢复出来的源必须拿到节点默认字符集（而不是留在空 = auto）")
	require.Equal(t, "", savedConfigOf(t, after, key).Charset,
		"索引里必须记「显式」口径：继承来的值不得写回（否则改节点默认对已登记源永不生效）")
	events := durableEvents(t, after, key)
	require.NotEmpty(t, events)
	var fresh []logtypes.Event
	for _, event := range events {
		if strings.Contains(event.Message, "重启后的新行") {
			fresh = append(fresh, event)
		}
	}
	require.NotEmpty(t, fresh, "夹具：重启后的新行必须已落库")
	require.Equal(t, "[02:56:55] [Server thread/INFO]: 重启后的新行", fresh[0].Message,
		"GBK 新行必须被正确解码")
	require.Equal(t, "gbk", fresh[0].Fields[normalize.FieldSourceCharset],
		"必须按节点默认的显式字符集口径记账（auto 会记 gb18030）")
}

// effectiveConfigOf 返回某源**本次运行的生效配置**（已应用节点默认）。
func effectiveConfigOf(t *testing.T, m *Manager, key string) SourceConfig {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sources[key]
}

// savedConfigOf 返回某源**落进索引**的配置（只含显式口径；恢复路径读的就是它）。
func savedConfigOf(t *testing.T, m *Manager, key string) SourceConfig {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state.SourceConfigs[key]
}

// gbkEncode 把文本编码为 GBK 字节（断言「显式 gbk」与「auto」口径差异时用）。
func gbkEncode(text string) ([]byte, int, error) {
	encoded, n, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(text))
	return encoded, n, err
}

// TestNodeDefaultsFollowRestartAndExplicitValuesWin 守住「继承 vs 显式」的语义边界（2026-10-02
// 真机复验的连带修复）：
//
//	① 未显式配置的源**每次都跟随当前节点默认**——改 `log_ingest.time_zone`/`charset` 重启即生效；
//	② 显式配置过的源**永远不被节点默认覆盖**（向后兼容）；
//	③ 索引里只记显式口径（继承值不写回），因此「上一次运行继承了什么」不会被固化成显式值。
//
// 转红：把继承值写回索引（`m.state.SourceConfigs[key] = source` 用生效配置）→ 第二轮（改默认重启）
// 不再生效 → 本用例红；把节点默认填充去掉 → 第一轮红。
func TestNodeDefaultsFollowRestartAndExplicitValuesWin(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	sourceID := "inst:defaults/file"
	key := sourceID + "/g1"
	base := SourceConfig{
		LogSourceID: sourceID, SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: sourceID, UTCDay: runtimeTestUTCDay(),
	}
	open := func(opts Options) *Manager {
		t.Helper()
		opts.Root, opts.VL = root, client
		opts.Catalog, opts.Journal = cat, cat.Journal()
		m, err := newTestManager(t, opts)
		require.NoError(t, err)
		return m
	}

	// 第一轮：节点默认 Asia/Hong_Kong + gbk；源未显式配置 ⇒ 生效值是继承来的，索引里保持空。
	first := open(Options{DefaultTimeZone: "Asia/Hong_Kong", DefaultCharset: "gbk",
		Sources: []SourceConfig{base}})
	require.Equal(t, "Asia/Hong_Kong", effectiveConfigOf(t, first, key).TimeZone)
	require.Equal(t, "gbk", effectiveConfigOf(t, first, key).Charset)
	require.Equal(t, "", savedConfigOf(t, first, key).TimeZone,
		"索引只记显式口径：继承来的值不得写回")
	require.NoError(t, first.Stop())

	// 第二轮：节点默认改成 Asia/Shanghai + gb18030 → 未显式配置的源必须跟随新默认（改配置即生效）。
	second := open(Options{DefaultTimeZone: "Asia/Shanghai", DefaultCharset: "gb18030"})
	require.Equal(t, "Asia/Shanghai", effectiveConfigOf(t, second, key).TimeZone,
		"未显式配置的源必须跟随**当前**节点默认：否则改了 yml 重启也不生效")
	require.Equal(t, "gb18030", effectiveConfigOf(t, second, key).Charset)
	require.NoError(t, second.Stop())

	// 第三轮：运维给该源**显式**值（现行装配路径：Options.Sources 携带；节点默认同时给别的值）。
	explicit := base
	explicit.TimeZone, explicit.Charset = "UTC", "utf-8"
	third := open(Options{DefaultTimeZone: "Asia/Hong_Kong", DefaultCharset: "gbk",
		Sources: []SourceConfig{explicit}})
	require.Equal(t, "UTC", effectiveConfigOf(t, third, key).TimeZone)
	require.Equal(t, "utf-8", effectiveConfigOf(t, third, key).Charset)
	require.Equal(t, "UTC", savedConfigOf(t, third, key).TimeZone, "显式值必须落索引")
	require.NoError(t, third.Stop())

	// 第四轮：节点默认再变 → 显式值必须保持（向后兼容：显式配置不得被节点默认覆盖）。
	fourth := open(Options{DefaultTimeZone: "Asia/Shanghai", DefaultCharset: "gb18030"})
	require.Equal(t, "UTC", effectiveConfigOf(t, fourth, key).TimeZone,
		"显式配置的源不得被节点默认覆盖（向后兼容）")
	require.Equal(t, "utf-8", effectiveConfigOf(t, fourth, key).Charset)
}
