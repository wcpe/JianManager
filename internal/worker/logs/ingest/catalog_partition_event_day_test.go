package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// TestDeliverCreatesCatalogPartitionForEventDayNotSourceDay 钉住 2026-11-05 真机
// 「日志页整页为空」的**第二个**根因（第一个是零水位，见 vlrange 的回归用例）。
//
// # 故障形态
//
// 补建 Catalog 记录时用的是 `source.UTCDay`——它是源**登记那一刻**的 UTC 日
// （runtime_lifecycle 只在为空时用 time.Now() 填一次，此后固定）。
// 而事件按各自的 EventTimeUTC 落进分区。一跨天两者就不一致：
//
//	真机：18 个源记录 utcDay=2026-10-30（登记日），
//	数据实际落 2026-11-04 分区（VL 直查 239 条）。
//	查询 11-04 → Planner.selectKeys 按时间范围过滤 ⇒ 选不到 10-30 的键
//	⇒ target 拿不到 Catalog 记录 ⇒ 日志页空。
//
// 既有代码里早有正确模式（hot_retention_window 的 daySource，注释写着
// 「必须把 UTCDay 设成该事件所属天 ... 若沿用源当前天 ⇒ 恒查不到」）——
// 本用例钉住投递路径也要照这个口径。
func TestDeliverCreatesCatalogPartitionForEventDayNotSourceDay(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	p := filepath.Join(logDir, "a.log")
	require.NoError(t, os.WriteFile(p, nil, 0o644))

	// 关键夹具：源的 UTCDay 是**登记日** 2026-10-30，而事件时间是 2026-11-04。
	// 真实生产的形态就是如此（源 10-30 登记，之后一直采集到今天）。
	const sourceDay = "2026-10-30"
	const eventDay = "2026-11-04"
	source := SourceConfig{
		LogSourceID: "inst:utcday-probe", SourceGeneration: "g1", Path: p,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:utcday-probe",
		UTCDay: sourceDay,
	}
	client, fixture := newProjectionVL(t)
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)

	// 真实采集链路：日志行的时间戳就是 eventDay，所以事件会落进 eventDay 分区。
	appendPollTestLines(t, p,
		"["+eventDay+"T17:23:19Z] [Server thread/INFO]: event day probe\n"+
			"["+eventDay+"T17:23:20Z] [Server thread/INFO]: event day probe 2\n")
	m.pollOnce()
	require.NotEmpty(t, fixture.writes, "夹具前提：一轮采集必须真的写进 VL")

	// 断言 1：**事件所属天**必须有 Catalog 记录——这才是查询会去查的键。
	_, storedEventDay := cat.Get(catalog.PartitionKey{
		StorageNamespace: source.StorageNamespace, UTCDay: eventDay,
	})
	require.True(t, storedEventDay,
		"投递后必须为**事件所属天**(%s) 建 Catalog 记录；否则查询该天时 selectKeys 选不到，日志页空",
		eventDay)
}

// TestDeliverCatalogCoversEveryDayInCrossDayBatch 一批跨天时，**每一天**都要有记录。
//
// 只补第一天会让其余天仍然查不到：长时间没 poll 会一次读回多天，
// 恢复/重放路径也会一次带上跨天区间。
func TestDeliverCatalogCoversEveryDayInCrossDayBatch(t *testing.T) {
	root := t.TempDir()
	logDir := filepath.Join(root, "src")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	p := filepath.Join(logDir, "b.log")
	require.NoError(t, os.WriteFile(p, nil, 0o644))

	source := SourceConfig{
		LogSourceID: "inst:utcday-cross", SourceGeneration: "g1", Path: p,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:utcday-cross",
		UTCDay: "2026-10-30",
	}
	client, _ := newProjectionVL(t)
	cat := catalog.New(catalog.NewMemJournal())
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source},
	})
	require.NoError(t, err)

	// 直接构造事件并投递：本用例要验的是「补建记录按事件天」，与日志行解析无关。
	// 走 pollOnce 会引入解析链（行内时间戳不被识别时会 fallback 到摄取时间，
	// 那样测出来的就不是跨天了——见 TestDeliverCreatesCatalogPartitionForEventDayNotSourceDay
	// 用真实采集链验的那一条）。
	identity := logtypes.SourceIdentity{
		LogSourceID: source.LogSourceID, SourceGeneration: source.SourceGeneration, ParserVersion: "v1",
	}
	batch := []logtypes.Event{
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 0, End: 50},
			"2026-11-04T23:59:00Z", "2026-11-04T23:59:00Z", "INFO", "stdout", "day1"),
		logtypes.BuildEvent(identity, logtypes.RecordRange{Start: 50, End: 100},
			"2026-11-05T00:01:00Z", "2026-11-05T00:01:00Z", "INFO", "stdout", "day2"),
	}
	_, err = m.deliver(source, batch, false)
	require.NoError(t, err)

	for _, day := range []string{"2026-11-04", "2026-11-05"} {
		_, ok := cat.Get(catalog.PartitionKey{StorageNamespace: source.StorageNamespace, UTCDay: day})
		require.True(t, ok, "跨天批次必须为每一天建记录；缺 %s 会让那天查不到", day)
	}
}
