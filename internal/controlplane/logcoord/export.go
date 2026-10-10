package logcoord

import (
	"bytes"
	"encoding/json"
	"strings"
)

// 导出物化的默认预算。与修复前的硬编常量一字不差：零配置部署的行为完全不变
// （见 .claude/rules/config-files.md「默认值必须合理，零配置即可启动」）。
const (
	// DefaultExportRows 单次导出默认行数上限。
	DefaultExportRows = 10_000
	// DefaultExportBytes 单次导出默认累计字节上限（64MiB）。
	DefaultExportBytes = 64 << 20
)

// ExportBudget 导出物化的服务端预算上限（CP 侧跨页累积）。
//
// 为什么必须由服务端裁决放宽：导出是「单次请求逐页读全量并在内存里物化」的操作，
// 允许请求方放大上限等于把无界资源风险交给任意调用者。请求侧参数只能**收紧**
// （见 Coordinator.Export），放宽一律走运维配置。
//
// 零值字段表示「用默认值」，使既有调用点（New / Assemble）无需改动即保持原行为。
type ExportBudget struct {
	// MaxRows 行数上限；<=0 时回退 DefaultExportRows。
	MaxRows int
	// MaxBytes 累计字节上限；0 时回退 DefaultExportBytes。
	MaxBytes uint64
}

// normalize 把零值/非法值回退到默认，保证调用点永远拿到可用预算。
func (b ExportBudget) normalize() ExportBudget {
	if b.MaxRows <= 0 {
		b.MaxRows = DefaultExportRows
	}
	if b.MaxBytes == 0 {
		b.MaxBytes = DefaultExportBytes
	}
	return b
}

// ExportLimits 本次导出实际采用的预算上限与实取行数。
//
// 只用于 UI 精确提示（「单次最多 N 行」而不是笼统「预算超了」），
// **不参与任何闸门判定**：闸门只看 ExportIncomplete / BudgetExceeded。
type ExportLimits struct {
	MaxRows  int    `json:"max_rows"`
	MaxBytes uint64 `json:"max_bytes"`
	Rows     int    `json:"rows"`
}

// ExportCut 本次导出的截断归因，决定 incomplete_reasons 里的精确码。
//
// 为什么需要显式归因，而不能只看「实取行数 > 上限」：导出在达到上限时**立即停止取页**，
// 实取行数往往恰好等于上限（不是大于），原判定 `len(items) > Limit` 因此永不成立——
// 生产上用户看到的只有笼统的 budget_exceeded，无法得知是「行数到顶」还是「字节到顶」，
// 也就无从判断该缩小时间范围还是调大配置。归因由停止取页的调用点给出才是准确的。
type ExportCut struct {
	// Budget 预算类截断（行或字节）。调用方无法细分时置位，保证既有 budget_exceeded 码不丢。
	Budget bool
	// Rows 因达到行数上限而停止取页。
	Rows bool
	// Bytes 因达到累计字节上限而停止取页。
	Bytes bool
	// Worker 列表或 Worker 侧截断。
	Worker bool
}

// BuildExport 兼容形态：仅区分「预算类截断」与「Worker/列表截断」。
//
// 保留此签名是因为它被既有测试与调用点直接使用；新代码请用 BuildExportWithCut
// 给出精确归因，否则前端只能给出笼统提示。
func BuildExport(view View, coverage Coverage, quality Quality, items []Event, budgetCut bool, workerCut bool) *ExportResult {
	return BuildExportWithCut(view, coverage, quality, items, ExportCut{Budget: budgetCut, Worker: workerCut})
}

// BuildExportWithCut 物化导出结果（带精确截断归因）。
//
// 与 Search 共用同一 View / Budget / Coverage 口径：
//   - 覆盖不完整（offline/not_ready/archive_missing/partial/stale/online_only 排除）→ incomplete；
//   - 列表或 Worker 截断（cut）→ incomplete；
//   - 行/字节预算截断或超限 → incomplete + BudgetExceeded；
//   - incomplete 时 Artifact 强制为 nil，不得交付成功产物。
//
// reason 分层：budget_exceeded 是既有 wire 契约（前端/测试/审计已消费），继续保留；
// row_limit_exceeded / byte_budget_exceeded 是叠加的精确码，供 UI 直说
// 「超过 N 行上限，请缩小时间范围或调大 log_export.max_rows」。
func BuildExportWithCut(view View, coverage Coverage, quality Quality, items []Event, cut ExportCut) *ExportResult {
	res := &ExportResult{
		View:     view,
		Coverage: coverage,
		Quality:  quality,
		Items:    items,
		Cut:      cut.Worker,
		Limits: ExportLimits{
			MaxRows:  view.Budget.Limit,
			MaxBytes: view.Budget.MaxBytes,
			Rows:     len(items),
		},
	}

	var reasons []string
	if !coverage.Complete {
		reasons = append(reasons, "coverage_incomplete")
		reasons = append(reasons, coverage.PartialReasons...)
	}
	if cut.Worker {
		reasons = append(reasons, "worker_or_list_truncated")
	}
	if cut.Budget || cut.Rows || cut.Bytes {
		reasons = append(reasons, "budget_exceeded")
		res.BudgetExceeded = true
	}
	if cut.Rows {
		reasons = append(reasons, "row_limit_exceeded")
	}
	if cut.Bytes {
		reasons = append(reasons, "byte_budget_exceeded")
	}

	var used uint64
	for _, ev := range items {
		used += ev.ApproxBytes()
	}
	res.RowBytes = used
	// 实测兜底：调用方漏标归因时仍不得把「实取超限」放行成成功产物。
	if view.Budget.Limit > 0 && len(items) > view.Budget.Limit {
		reasons = append(reasons, "budget_exceeded", "row_limit_exceeded")
		res.BudgetExceeded = true
	}
	if view.Budget.MaxBytes > 0 && used > view.Budget.MaxBytes {
		reasons = append(reasons, "budget_exceeded", "byte_budget_exceeded")
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
