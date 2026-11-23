package main

// 启动时「VL 流标签生效形态」那行日志的回归（2026-10-31）。
//
// 为什么要为一行日志写测试：这行日志回答的是运维将来一定会问的问题 ——
// 「为什么查询里有的日志没有 stream 标签？」。它真正的信息量在两处：
//   1. 启用时**必须**说出“旧数据不会自动迁移”（否则运维会把它当故障排查）；
//   2. 关闭时**必须**说出这是配置显式关的（否则同样会被当成没接上）。
// 少了任何一句，这行日志就退化成一句无信息的“已启用”，不如不写。
//
// 转红方式：把 disabled 分支里那句“所有日志落在同一个 stream”删掉
// ⇒ TestLogStreamFieldsStateDisabledSaysAllInOneStream 立红。

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// captureSlog 把默认 slog 的输出接到一个 buffer 上，返回还原函数。
func captureSlog(t *testing.T, buf *bytes.Buffer) func() {
	t.Helper()
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() { slog.SetDefault(prev) }
}

func TestLogStreamFieldsStateEnabledMentionsNoBackfill(t *testing.T) {
	// 启用：必须包含生效取值、且必须说清“旧数据不会自动迁移”。
	buf := &bytes.Buffer{}
	restore := captureSlog(t, buf)
	defer restore()

	logStreamFieldsState(false, "")
	out := buf.String()

	require.Contains(t, out, "流标签已启用")
	require.Contains(t, out, vlsup.DefaultStreamFields,
		"必须把实际生效的取值打出来（否则运维无从知晓配置缺省到底生成了什么）")
	require.Contains(t, out, "不会自动迁移",
		"必须点名历史数据不会回填 —— 这是运维把「有的日志没流标签」误当故障的根因")
	require.Contains(t, out, "retention", "必须给出旧数据的去向")
}

func TestLogStreamFieldsStateEnabledHonorsOverride(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := captureSlog(t, buf)
	defer restore()

	logStreamFieldsState(false, " log_source_id ")
	require.Contains(t, buf.String(), "stream_fields=log_source_id",
		"显式覆盖值必须原样出现在日志里（去掉两端空白）")
}

func TestLogStreamFieldsStateDisabledSaysAllInOneStream(t *testing.T) {
	buf := &bytes.Buffer{}
	restore := captureSlog(t, buf)
	defer restore()

	logStreamFieldsState(true, "")
	out := buf.String()

	require.Contains(t, out, "已关闭")
	require.Contains(t, out, "同一个 stream",
		"关闭时必须说清后果（不分流），否则运维无法判断自己要不要开")
	require.Contains(t, out, "disable_stream_fields", "要点名是哪个键关的")
	require.NotContains(t, out, "不会自动迁移", "关闭时不应误报迁移相关提示")
	// 关闭时即使传了 override 也不应把取值打出来——那会造成“似乎生效了”的误解。
	require.NotContains(t, strings.TrimSpace(out), "log_source_id,stream")
}
