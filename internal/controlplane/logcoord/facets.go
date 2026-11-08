package logcoord

import "sort"

// ControlledFacetDimensions 受控（可精确合并）维度白名单。
//
// 与 FR-473 stream_fields 精神一致：低基数路由/分类字段。
// 不得包含 event_id、message、_time 或任意用户高基数字段。
//
// # ⚠️ 这是一份**重复实现**，必须与 Worker 侧同源
//
// 同一判据在两处各写了一遍：
//   - 本文件（CP 侧，决定「哪些维度可精确合并」）
//   - `internal/worker/logs/query/service.go` 的 `streamFieldAllowed`（Worker 侧，
//     决定「哪些维度允许透出/聚合」）
//
// 2026-11-05 实测到了分叉的后果：Worker 侧新增 `event_time_zone` 等字段后，
// CP 侧仍不认，于是请求能到达 Worker、结果也能回来，但 CP 侧按「非受控」处理。
//
// 两份清单的**语义**确实不同（CP 关心「能否精确合并」，Worker 关心「能否聚合」），
// 但取值范围必须一致——凡是 Worker 放行的，CP 也必须认，否则前端拿到的
// controlled 标记与实际能力不符。
//
// # 2026-11-05 变更
//
// 新增三个低基数、有查询价值的字段（生产实测基数，近 30 天全平台）：
//
//	event_time_zone  基数 2  跨时区排查「这条日志是哪的本地时间」
//	parser_version   基数 2  排查解析器升级前后的行为差异
//	parse_status     基数 4  找出没解析成功的行
//
// 与 Worker 侧 `streamFieldAllowed` 的白名单保持逐字一致。
var ControlledFacetDimensions = map[string]bool{
	"level":         true,
	"stream":        true,
	"worker_id":     true,
	"instance_id":   true,
	"log_source_id": true,
	// node_id / source：VL 里目前恒为空（生产实测基数 1，值为 ""），
	// 属预留字段。保留不影响正确性（Worker 侧不放行它们，聚合结果自然是空）。
	"node_id": true,
	"source":  true,
	// 2026-11-05 与 Worker 侧同步新增。
	"event_time_zone": true,
	"parser_version":  true,
	"parse_status":    true,
}

// 2026-11-05：**移除 source_generation**，与 Worker 侧 `streamFieldAllowed` 对齐。
//
// 此前 CP 侧认它、Worker 侧不认，形成分叉：请求能到 Worker、结果也回得来，
// 但 CP 标 controlled=true 而 Worker 返回空 values —— 前端会显示一个
// 「可选但永远没数据」的维度，比不提供更困惑。
//
// 移除的理由（与 Worker 侧注释同源）：它基数 21 与 log_source_id 同量级，
// 但**值极长**（形如 `instance:<uuid>@worker:<uuid>`，单值 85+ 字符），
// 作为分面维度聚合会让响应体积膨胀数倍；而「按源看」已由 log_source_id 覆盖。
// 它作为**内部身份**更适合出现在事件详情而非分面维度。

// IsControlledFacet 维度是否受控。
func IsControlledFacet(dimension string) bool {
	return ControlledFacetDimensions[dimension]
}

// MergeFacets 合并多 Worker Facet 结果。
//
// 规则（FR-480）：
//   - 受控维度：按 value 精确合并 count；
//   - 高基数维度：合并后仍可按 limit 裁剪，必须保留 Truncated + TruncatedCount；
//   - 任一 Worker 侧 Truncated 传染到合并结果（不得把截断结果标成 exact 完整）。
func MergeFacets(perWorker []WorkerFacetsResponse, dimensions []string, dimensionLimit uint32) []FacetDimension {
	type acc struct {
		counts         map[string]uint64
		workerTrunc    bool
		workerTruncCnt uint64
	}
	byDim := make(map[string]*acc)
	dimOrder := make([]string, 0, len(dimensions))
	for _, d := range dimensions {
		if _, ok := byDim[d]; ok {
			continue
		}
		byDim[d] = &acc{counts: make(map[string]uint64)}
		dimOrder = append(dimOrder, d)
	}

	for _, resp := range perWorker {
		for _, wd := range resp.Dimensions {
			a, ok := byDim[wd.Dimension]
			if !ok {
				// Worker 返回了未请求维度：仍合并但按高基数处理。
				a = &acc{counts: make(map[string]uint64)}
				byDim[wd.Dimension] = a
				dimOrder = append(dimOrder, wd.Dimension)
			}
			for _, v := range wd.Values {
				a.counts[v.Value] += v.Count
			}
			if wd.Truncated {
				a.workerTrunc = true
				a.workerTruncCnt += wd.TruncatedCount
			}
		}
	}

	out := make([]FacetDimension, 0, len(dimOrder))
	for _, dim := range dimOrder {
		a := byDim[dim]
		values := make([]FacetValue, 0, len(a.counts))
		for val, c := range a.counts {
			values = append(values, FacetValue{Value: val, Count: c})
		}
		// count DESC, value ASC 稳定排序。
		sort.SliceStable(values, func(i, j int) bool {
			if values[i].Count != values[j].Count {
				return values[i].Count > values[j].Count
			}
			return values[i].Value < values[j].Value
		})

		fd := FacetDimension{
			Dimension:      dim,
			Controlled:     IsControlledFacet(dim),
			Truncated:      a.workerTrunc,
			TruncatedCount: a.workerTruncCnt,
			Limit:          dimensionLimit,
		}
		if dimensionLimit > 0 && uint32(len(values)) > dimensionLimit {
			omitted := uint64(len(values)) - uint64(dimensionLimit)
			// 本地裁剪：受控维度在超限时同样必须标记截断（禁止静默丢桶）。
			fd.Truncated = true
			fd.TruncatedCount += omitted
			values = values[:dimensionLimit]
		}
		// 受控维度且未截断 → 精确合并；任一截断则 Truncated 保持 true。
		if fd.Controlled && !fd.Truncated {
			fd.TruncatedCount = 0
		}
		if !fd.Controlled && !fd.Truncated && len(values) > 0 && dimensionLimit == 0 {
			// 未设 limit 的高基数维度：无法证明完整，文档化要求返回截断语义。
			// 这里不伪造 TruncatedCount，但 Truncated 置位以禁止当作 exact。
			fd.Truncated = true
		}
		fd.Values = values
		out = append(out, fd)
	}
	return out
}

// FacetsTruncated 是否存在任一维度截断。
func FacetsTruncated(dims []FacetDimension) bool {
	for _, d := range dims {
		if d.Truncated {
			return true
		}
	}
	return false
}
