package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// 缺陷 C 的端到端回归：源配置（或节点默认）的时区必须一路接到归一化并改变落库时间。
//
// 现场（2026-10-01/02）：日志文本 `[23:54:02]`（本地 HKT=UTC+8）被存成
// `event_time_utc=2026-10-01T23:54:02Z`（应为 15:54:02Z），时间窗/排序/实时跟随全部错位。
//
// 转红：改动前没有 SourceConfig.TimeZone 这条配置面，事件时间恒为 23:54:02Z → 必红。
func TestSourceTimeZoneReachesStoredEvent(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[23:54:02] [Server thread/INFO]: 香港时间\n[23:54:03] [Server thread/INFO]: 结束\n"), 0o644))

	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:tz/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:tz", UTCDay: runtimeTestUTCDay(),
		TimeZone: "Asia/Hong_Kong",
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{source},
	})
	require.NoError(t, err)
	m.pollOnce()

	events := durableEvents(t, m, source.LogSourceID+"/"+source.SourceGeneration)
	require.NotEmpty(t, events)
	require.True(t, strings.HasSuffix(events[0].EventTimeUTC, "T15:54:02Z"),
		"HKT 23:54:02 必须落库为 15:54:02Z，实得 %s", events[0].EventTimeUTC)
	require.Equal(t, "Asia/Hong_Kong", events[0].Fields[normalize.FieldEventTimeZone])
}

// TestDefaultTimeZoneAppliesWhenSourceDoesNotConfigure 覆盖「跟随节点配置」：
// 节点级默认（Options.DefaultTimeZone）在源未显式配置时生效。
func TestDefaultTimeZoneAppliesWhenSourceDoesNotConfigure(t *testing.T) {
	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[23:54:02] [Server thread/INFO]: 节点默认时区\n[23:54:03] [Server thread/INFO]: 结束\n"), 0o644))

	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:tz-default/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:tz-default", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source}, DefaultTimeZone: "Asia/Shanghai",
	})
	require.NoError(t, err)
	m.pollOnce()

	events := durableEvents(t, m, source.LogSourceID+"/"+source.SourceGeneration)
	require.NotEmpty(t, events)
	require.True(t, strings.HasSuffix(events[0].EventTimeUTC, "T15:54:02Z"),
		"节点默认时区必须生效，实得 %s", events[0].EventTimeUTC)
}

// TestUnknownTimeZoneIsRejectedAtRegistration 守住「配置错误必须显式暴露」：
// 时区配错会让整源时间轴静默偏移，登记阶段就应失败而不是等查询结果对不上。
func TestUnknownTimeZoneIsRejectedAtRegistration(t *testing.T) {
	require.True(t, IsValidTimeZone(""))
	require.True(t, IsValidTimeZone("utc"))
	require.True(t, IsValidTimeZone("local"))
	require.True(t, IsValidTimeZone("Asia/Hong_Kong"))
	require.False(t, IsValidTimeZone("HKT+8"))
	require.False(t, IsValidTimeZone("Not/AZone"))

	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath, []byte("[23:54:02] [Server thread/INFO]: x\n"), 0o644))
	cat := catalog.New(catalog.NewMemJournal())
	_, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{{
			LogSourceID: "inst:tz-bad/file", SourceGeneration: "g1", Path: logPath,
			Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:tz-bad", UTCDay: runtimeTestUTCDay(),
			TimeZone: "HKT+8",
		}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "时区")
}

// TestDefaultTimeZoneLocalFollowsProcessTZ 覆盖生产建议的 `local`：节点 JVM 与 Worker 同机部署，
// 进程 TZ 就是日志的本地时区（生产现场 HKT）。
//
// 与 IANA 名用例的区别：local 走 time.Local 分支，正确性取决于进程 TZ 是否被真正读取——
// 只断言 IsValidTimeZone("local") 为真抓不到这一层。转红：local 分支未接 time.Local 时
// 事件时间恒为 23:54:02Z（现场缺陷）。
func TestDefaultTimeZoneLocalFollowsProcessTZ(t *testing.T) {
	// Go ≥1.22 在 TZ 变更后重载 time.Local（未显式赋值 time.Local 时）。
	t.Setenv("TZ", "Asia/Hong_Kong")

	client, _ := newFlakyVL(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "latest.log")
	require.NoError(t, os.WriteFile(logPath,
		[]byte("[23:54:02] [Server thread/INFO]: local 时区\n[23:54:03] [Server thread/INFO]: 结束\n"), 0o644))

	cat := catalog.New(catalog.NewMemJournal())
	source := SourceConfig{
		LogSourceID: "inst:tz-local/file", SourceGeneration: "g1", Path: logPath,
		Mode: pipeline.ModeFilePrimary, StorageNamespace: "inst:tz-local", UTCDay: runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: root, VL: client, Catalog: cat, Journal: cat.Journal(),
		Sources: []SourceConfig{source}, DefaultTimeZone: "local",
	})
	require.NoError(t, err)
	m.pollOnce()

	events := durableEvents(t, m, source.LogSourceID+"/"+source.SourceGeneration)
	require.NotEmpty(t, events)
	require.True(t, strings.HasSuffix(events[0].EventTimeUTC, "T15:54:02Z"),
		"local 时区（进程 TZ=HKT）下 23:54:02 必须落库为 15:54:02Z，实得 %s", events[0].EventTimeUTC)
	// 审计字段记录解析后的位置名：time.Local 的 String() 恒为 "Local"。
	require.Equal(t, "Local", events[0].Fields[normalize.FieldEventTimeZone])
}
