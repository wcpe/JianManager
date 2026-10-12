package ingest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	// time.Local 的初始化在进程内缓存；以独立进程固定 TZ，不能指望 Setenv 重置先前测试的缓存。
	const childFlag = "JM_INGEST_LOCAL_TZ_CHILD"
	if os.Getenv(childFlag) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDefaultTimeZoneLocalFollowsProcessTZ$", "-test.count=1")
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "TZ=") && !strings.HasPrefix(value, childFlag+"=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Env = append(cmd.Env, "TZ=Asia/Hong_Kong", childFlag+"=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "隔离进程的严格换算回归失败：%s", output)
		return
	}
	// 子进程启动前已固定 TZ；不在父进程修改或重置 time.Local。
	require.Equal(t, "Asia/Hong_Kong", time.Local.String())
	_, offsetSeconds := time.Date(2026, time.October, 8, 23, 54, 2, 0, time.Local).Zone()
	require.Equal(t, 8*60*60, offsetSeconds, "本地时区解析必须得到 UTC+8 的精确偏移")

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
	// 只断言「时刻换算」，不把**日期归属**写进契约：无日期日志行（`[23:54:02]`）的归日由
	// normalize 的 applyClock 以 base 为锚就近取号（±12h），而实时路径的 base=time.Now()
	// （effectiveBase 的兜底；pipeline 只在归档日导入时才显式给 BaseTime=该日中午）。
	// 于是同一行会落到 now 的前一日或当日，取决于**运行时的钟点**——这是既定契约，
	// 由 normalize/TestSourceTimeZoneCrossMidnightBoundary 与 applyClock 的注释固定。
	// 原断言写死「当日」，等于把运行时刻当成契约：HKT 00:00–11:54 窗口内必红
	// （CI 每天约一半时间会红），与另两条同类用例（这里上面两条用 HasSuffix）不一致。
	require.True(t, strings.HasSuffix(events[0].EventTimeUTC, "T15:54:02Z"),
		"local 时区（进程 TZ=HKT）下 23:54:02 必须换算为 15:54:02Z，实得 %s", events[0].EventTimeUTC)
	// 归日仍须落在 now 附近（当日或前一日）：避免日期被算到更远处而不被察觉。
	requiredDay := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	day := strings.TrimSuffix(events[0].EventTimeUTC, "T15:54:02Z")
	require.Contains(t, []string{source.UTCDay, requiredDay}, day,
		"归日必须落在 now 附近（当日 %s 或前一日 %s），实得 %s", source.UTCDay, requiredDay, day)
	// 审计字段按 normalize 的现行契约记录解析后的 IANA 位置名，而不是配置别名 local。
	require.Equal(t, "Asia/Hong_Kong", events[0].Fields[normalize.FieldEventTimeZone])
}
