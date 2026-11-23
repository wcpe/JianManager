package query

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 白名单是**安全边界**：查询面只在白名单内的字段才允许被 Fields()/Facets() 暴露，
// 避免调用方用任意 event 字段做高基数聚合把 VL 拖垮。
//
// 本用例把两侧都钉住：
//   - 该放行的必须放行（否则用户看不到有查询价值的维度）
//   - 该拦下的必须拦下（否则一次高基数聚合就能拖垮 VL）
//
// 加字段前必须像下面注释那样先测基数——「有查询价值」不等于「可以开放」。
func TestStreamFieldAllowedWhitelist(t *testing.T) {
	// 放行：低基数、有查询价值。括号内是 2026-11-05 生产实测基数（近 30 天全平台）。
	allowed := []string{
		"level",           // 基数 4
		"stream",          // 基数 3
		"instance_id",     // 历史维度
		"log_source_id",   // 基数 31（与源数同量级，2026-10-31 开放）
		"event_time_zone", // 基数 2（2026-11-05 开放）
		"parser_version",  // 基数 2（2026-11-05 开放）
		"parse_status",    // 基数 4（2026-11-05 开放）
	}
	for _, field := range allowed {
		require.True(t, streamFieldAllowed(field), "白名单应放行 %q", field)
	}

	// 拦下：高基数（任意 event 字段）或值极长（聚合后响应膨胀）。
	blocked := []string{
		"_msg",                   // 正文，基数≈行数
		"event_id",               // 基数 = 事件数
		"canonical_content_hash", // 基数 = 事件数
		"record_start",           // 基数 = 事件数（单调递增）
		"record_end",             // 同上
		"source_generation",      // 基数 21，但单值 85+ 字符 ⇒ 响应膨胀
		"_time",                  // 时间戳
		"",                       // 空串
		"not_a_real_field",       // 未知字段一律拒绝（default-deny）
	}
	for _, field := range blocked {
		require.False(t, streamFieldAllowed(field), "白名单不应放行 %q", field)
	}
}

// source_generation 有明确理由**不**开放，单独一条防止以后被"顺手加上"。
//
// 它基数 21（与 log_source_id 的 31 同量级），单看基数像是可以开放；
// 但值形如 `instance:<uuid>@worker:<uuid>`，单值 85+ 字符 —— 作为分面维度聚合时，
// 响应体积会随维度值长度线性膨胀，而「按源看」已经有 log_source_id 覆盖。
func TestSourceGenerationStaysOutOfWhitelist(t *testing.T) {
	require.False(t, streamFieldAllowed("source_generation"),
		"source_generation 值极长（85+ 字符），聚合会让响应膨胀数倍；按源看用 log_source_id")
	require.True(t, streamFieldAllowed("log_source_id"),
		"对照：按源的维度由 log_source_id 承担")
}
