package logcoord

import "sort"

// ControlledFacetDimensions 受控（可精确合并）维度白名单。
//
// 与 FR-472 stream_fields 精神一致：低基数路由/分类字段。
// 不得包含 event_id、message、_time 或任意用户高基数字段。
var ControlledFacetDimensions = map[string]bool{
	"level":             true,
	"stream":            true,
	"worker_id":         true,
	"instance_id":       true,
	"log_source_id":     true,
	"source_generation": true,
	"node_id":           true,
	"source":            true,
}

// IsControlledFacet 维度是否受控。
func IsControlledFacet(dimension string) bool {
	return ControlledFacetDimensions[dimension]
}

// MergeFacets 合并多 Worker Facet 结果。
//
// 规则（FR-479）：
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
