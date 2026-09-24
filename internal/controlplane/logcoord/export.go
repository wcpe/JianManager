package logcoord

import (
	"bytes"
	"encoding/json"
	"strings"
)

// BuildExport 物化导出结果。
//
// 与 Search 共用同一 View / Budget / Coverage 口径：
//   - 覆盖不完整（offline/not_ready/archive_missing/partial/stale/online_only 排除）→ incomplete；
//   - 列表或 Worker 截断（cut）→ incomplete；
//   - 行/字节预算截断或超限 → incomplete + BudgetExceeded；
//   - incomplete 时 Artifact 强制为 nil，不得交付成功产物。
func BuildExport(view View, coverage Coverage, quality Quality, items []Event, budgetCut bool, workerCut bool) *ExportResult {
	res := &ExportResult{
		View:     view,
		Coverage: coverage,
		Quality:  quality,
		Items:    items,
		Cut:      workerCut,
	}

	var reasons []string
	if !coverage.Complete {
		reasons = append(reasons, "coverage_incomplete")
		reasons = append(reasons, coverage.PartialReasons...)
	}
	if workerCut {
		reasons = append(reasons, "worker_or_list_truncated")
	}
	if budgetCut {
		reasons = append(reasons, "budget_exceeded")
		res.BudgetExceeded = true
	}
	if view.Budget.Limit > 0 && len(items) > view.Budget.Limit {
		reasons = append(reasons, "row_limit_exceeded")
		res.BudgetExceeded = true
	}

	var used uint64
	for _, ev := range items {
		used += ev.ApproxBytes()
	}
	res.RowBytes = used
	if view.Budget.MaxBytes > 0 && used > view.Budget.MaxBytes {
		reasons = append(reasons, "byte_budget_exceeded")
		res.BudgetExceeded = true
	}

	if len(reasons) > 0 {
		res.ExportIncomplete = true
		res.IncompleteReasons = dedupeStrings(reasons)
		res.Artifact = nil
		res.Items = items // 保留明细供诊断，但不发布成功附件
		return res
	}

	// 契约 §4.5：projection 未决/冲突时 Export 不得发布成功附件（Quality 与 Coverage 正交）。
	// 枚举大小写不敏感（wire 可能为 exact/unresolved 或 EXACT/UNRESOLVED）。
	dq := strings.ToLower(string(quality.DuplicateQuality))
	sq := strings.ToLower(string(quality.StatsQuality))
	if dq == "unresolved" || dq == "conflict" || sq == "partial" || sq == "unavailable" {
		res.ExportIncomplete = true
		res.IncompleteReasons = []string{"quality_" + dq, "stats_" + sq}
		res.Artifact = nil
		return res
	}

	// 完整通过：生成 NDJSON 物化产物。
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, ev := range items {
		if err := enc.Encode(ev); err != nil {
			res.ExportIncomplete = true
			res.IncompleteReasons = []string{"artifact_encode_failed"}
			res.Artifact = nil
			return res
		}
	}
	if view.Budget.MaxBytes > 0 && uint64(buf.Len()) > view.Budget.MaxBytes {
		res.ExportIncomplete = true
		res.BudgetExceeded = true
		res.IncompleteReasons = []string{"artifact_byte_budget_exceeded"}
		res.Artifact = nil
		return res
	}
	res.Artifact = buf.Bytes()
	return res
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
