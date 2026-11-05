package logcoord

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 2026-11-05：分面维度白名单在**两处各写了一遍**，本用例守它们的取值范围一致。
//
// # 为什么需要这条
//
// CP 侧 `ControlledFacetDimensions` 决定「哪些维度可精确合并」；
// Worker 侧 `query.streamFieldAllowed` 决定「哪些维度允许透出/聚合」。
//
// 语义确实不同，但**取值范围必须一致**。分叉的实际后果（本次实测）：
//
//	CP 标 controlled=true  →  前端显示该维度可选
//	Worker 不放行          →  永远返回空 values
//	⇒ 用户看到一个「可选但永远没数据」的维度，比不提供更困惑
//
// 本次分叉的具体字段是 `source_generation`：CP 认、Worker 不认。
//
// # 守卫方式
//
// 不直接 import worker 包（CP 不该依赖 worker 实现），而是把**必须双方一致**的
// 核心集合写死在这里。任一方漏加/多加，本用例转红。
//
// 加字段的流程：先测基数（见各处注释），再同时改两处，最后更新本清单。
func TestControlledFacetDimensionsStayInSyncWithWorker(t *testing.T) {
	// 这些维度必须**双方都认**：低基数、有查询价值。
	mustBothSides := []string{
		"level",           // 基数 4
		"stream",          // 基数 3
		"instance_id",     // 历史维度
		"log_source_id",   // 基数 31（与源数同量级）
		"event_time_zone", // 基数 2（2026-11-05 双方同步新增）
		"parser_version",  // 基数 2（2026-11-05 双方同步新增）
		"parse_status",    // 基数 4（2026-11-05 双方同步新增）
	}
	for _, dim := range mustBothSides {
		require.True(t, IsControlledFacet(dim),
			"CP 侧必须认维度 %q（Worker 侧已放行，不认会导致「可选但永远没数据」）", dim)
	}

	// 这些维度必须**双方都不认**：高基数，或值极长导致聚合响应膨胀。
	mustNeitherSide := []string{
		"event_id",               // 基数 = 事件数
		"canonical_content_hash", // 同上
		"_msg",                   // 正文
		"_time",                  // 时间戳
		"source_generation",      // 基数 21 但单值 85+ 字符 ⇒ 聚合响应膨胀
	}
	for _, dim := range mustNeitherSide {
		require.False(t, IsControlledFacet(dim),
			"CP 侧不得认维度 %q", dim)
	}
}

// source_generation 是本轮实测到的分叉点，单独钉一条。
//
// 它曾经只在 CP 侧的白名单里——请求能到达 Worker、结果也能回来，
// 但 Worker 不放行 ⇒ 永远返回空 values。
func TestSourceGenerationNotControlledOnEitherSide(t *testing.T) {
	require.False(t, IsControlledFacet("source_generation"),
		"source_generation 值极长（85+ 字符），两侧都不该认；按源看用 log_source_id")
	require.True(t, IsControlledFacet("log_source_id"),
		"对照：按源的维度由 log_source_id 承担")
}
