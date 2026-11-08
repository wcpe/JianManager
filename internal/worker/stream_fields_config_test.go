package config

// 流标签（`_stream_fields`）的配置面回归（2026-10-31 对齐 VL 官方接入方式）。
//
// 与 vlsup 那侧的分工（刻意分开，不重复）：
//   - vlsup：请求 URL 到底带了什么？无界字段拒了吗？（client_stream_fields_test.go）
//   - 本文件：`log_vl.*` 键能读进来吗？默认是什么意思？写错了会不会在启动时就拒？
//
// 为什么流标签的错配必须在启动阶段拦：它是**单向不可逆**的——写进 VL 的 stream 元数据
// 只能靠删分区回收，而症状（元数据/内存膨胀）要等写入量上来才显形。等到首次写入才报，
// 意味着在此之前已经写了不知道多少行进去。
//
// 转红方式：从 Load 里删掉 `cfg.LogVL.ValidateStreamFields()` ⇒
// TestStreamFieldsUnboundedRejectedAtLoad 立红。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
)

// 默认开：不写任何键时，生效的就是架构裁定的 `log_source_id,stream`。
func TestStreamFieldsEnabledByDefault(t *testing.T) {
	cfg, err := Load(t.TempDir() + "/nonexistent.yaml")
	require.NoError(t, err)
	require.False(t, cfg.LogVL.DisableStreamFields, "流标签默认开启（对齐 VL 官方接入方式）")
	require.Empty(t, cfg.LogVL.StreamFields, "空值 = 走 vlsup.DefaultStreamFields（单一真源在 vlsup）")
	require.Equal(t, "log_source_id,stream", vlsup.DefaultStreamFields)
	require.NoError(t, cfg.LogVL.ValidateStreamFields())
}

// 两个键都能真正从 yml 读进来（否则“可关掉”只是文档幻觉）。
func TestStreamFieldsKeysReadableFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.yml")
	require.NoError(t, os.WriteFile(path, []byte(
		"log_vl:\n  stream_fields: log_source_id\n  disable_stream_fields: true\n"), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "log_source_id", cfg.LogVL.StreamFields)
	require.True(t, cfg.LogVL.DisableStreamFields)
	// 关掉时不再校验取值（反正一个参数都不发）。
	require.NoError(t, cfg.LogVL.ValidateStreamFields())
}

// 无界基数字段入流 ⇒ **Load 即拒**（不静默、不告警后继续）。
//
// 无界基数字段是能毁掉 VL 的操作：每行不同 ⇒ VL 为每行建一个 stream。
func TestStreamFieldsUnboundedRejectedAtLoad(t *testing.T) {
	for _, bad := range []string{
		"log_source_id,event_id",
		"record_start",
		"record_end",
		"ingest_time_utc",
		"canonical_content_hash",
		"log_source_id,,stream",
	} {
		path := filepath.Join(t.TempDir(), "worker.yml")
		require.NoError(t, os.WriteFile(path, []byte(
			"log_vl:\n  stream_fields: "+bad+"\n"), 0o600))
		_, err := Load(path)
		require.Error(t, err, "stream_fields=%q 必须在启动时被拒绝", bad)
		require.ErrorContains(t, err, "_stream_fields")
	}
}

// 合法的覆盖值（小基数字段）仍然放行：拒绝的边界是“无界”，不是“非默认”。
func TestStreamFieldsBoundedOverrideAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.yml")
	require.NoError(t, os.WriteFile(path, []byte(
		"log_vl:\n  stream_fields: log_source_id,stream,level\n"), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.NoError(t, cfg.LogVL.ValidateStreamFields(),
		"level 是小基数字段：不推荐入流（会随内容漂移），但不是“绝对禁止”级别")
}
