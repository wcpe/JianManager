package logcoord

import (
	"context"
	"strings"
)

// ResolvedTargets 目标解析结果。
//
// 规则（FR-480）：
//   - All：CP 全部授权目标（含历史持有者）；
//   - Queried：本次扇出集合。默认 = All；仅当 OnlineOnly 显式为 true 时收缩为在线目标；
//   - Excluded：因显式 online_only 而未扇出的目标，必须进入 coverage，禁止静默丢弃。
type ResolvedTargets struct {
	All        []TargetInfo
	Queried    []TargetInfo
	Excluded   []TargetInfo
	OnlineOnly bool
}

// TargetIDs 返回扇出目标 ID 列表。
func (r ResolvedTargets) TargetIDs() []string {
	ids := make([]string, 0, len(r.Queried))
	for _, t := range r.Queried {
		ids = append(ids, t.ID)
	}
	return ids
}

// ByWorker 将扇出目标按 WorkerID 分组（保持稳定 worker 顺序）。
func (r ResolvedTargets) ByWorker() (order []string, by map[string][]TargetInfo) {
	by = make(map[string][]TargetInfo)
	seen := make(map[string]bool)
	for _, t := range r.Queried {
		if !seen[t.WorkerID] {
			seen[t.WorkerID] = true
			order = append(order, t.WorkerID)
		}
		by[t.WorkerID] = append(by[t.WorkerID], t)
	}
	return order, by
}

// ResolveTargets 解析授权目标集合。
//
// online_only 仅在显式 true 时收缩；否则即使目标离线/未就绪/归档缺失也保留在 Queried
// 中尝试查询，并在 coverage 中记录失败状态——绝不能静默丢弃后宣称 complete。
func ResolveTargets(ctx context.Context, resolver TargetResolver, authorizedIDs []string, onlineOnly bool) (*ResolvedTargets, error) {
	if resolver == nil {
		return nil, errNoResolver
	}
	infos, err := resolver.Resolve(ctx, authorizedIDs)
	if err != nil {
		return nil, err
	}
	// 始终以解析返回的全集为 All；调用方传入的 authorizedIDs 仅作输入。
	out := &ResolvedTargets{
		All:        append([]TargetInfo(nil), infos...),
		OnlineOnly: onlineOnly,
	}
	for _, t := range infos {
		if onlineOnly && t.Readiness != ReadyOnline {
			out.Excluded = append(out.Excluded, t)
			continue
		}
		out.Queried = append(out.Queried, t)
	}
	return out, nil
}

// CoverageFromReadiness 为未成功参与查询的目标生成覆盖项。
//
// 当 reasons 明确表示目标不可查询（unsupported / dial 失败等）时，
// 即使注册表 readiness 为 online，也不得映射为 success。
func CoverageFromReadiness(t TargetInfo, reasons []string) TargetCoverage {
	state := ResolveReadinessToCoverage(t.Readiness)
	rs := append([]string(nil), reasons...)
	switch t.Readiness {
	case ReadyUnsupported:
		rs = append(rs, "log_rpc_unsupported")
		state = CoverageNotReady
	case ReadyArchiveMissing:
		rs = append(rs, "archive_missing")
	case ReadyOffline:
		rs = append(rs, "offline")
	case ReadyNotReady:
		rs = append(rs, "not_ready")
	}
	for _, r := range rs {
		// worker_view_failed：该 Worker 的本地 Query View 未建立（未启用日志查询服务、
		// 混版 Unimplemented、绑定缺失等）。其语义是「该 Worker 的数据整体不可查询」，
		// 而非「查到了但有缺口」，故归 NotReady 而非 Partial。
		// 若不在此列出，原因既不改 state、又无法被 normalizeCoverageReason 识别，
		// 目标会停留 success 并使 BuildCoverage 误判 complete=true；导出据此放行，
		// 用户会拿到「成功」附件却静默缺失整个 Worker 的日志（B-1）。
		if r == "log_rpc_unsupported" || r == "online_only_excluded" || r == "missing_coverage" ||
			r == "worker_view_failed" {
			if state == CoverageSuccess {
				state = CoverageNotReady
			}
		}
		if r == "worker_error" || r == "search_failed" || r == "stats_failed" ||
			r == "facets_failed" || r == "fields_failed" || r == "empty_worker_response" ||
			r == "target_missing_in_worker_response" || r == "fanout_budget_exceeded" {
			if state == CoverageSuccess {
				state = CoveragePartial
			}
		}
		if r == "dial_failed" && state == CoverageSuccess {
			state = CoverageOffline
		}
	}
	if t.HistoricalHolder {
		rs = append(rs, "historical_holder")
	}
	return TargetCoverage{
		TargetID:          t.ID,
		State:             state,
		Reasons:           rs,
		ClosedVisibleSeq:  t.ClosedVisibleSeq,
		CatalogGeneration: t.CatalogGeneration,
		WorkerID:          t.WorkerID,
		HistoricalHolder:  t.HistoricalHolder,
	}
}

// BuildCoverage 汇总目标覆盖。
//
// complete 仅当授权全集内每个目标均为 success；online_only 排除项、离线、未就绪、
// 归档缺失、partial/stale 均使 complete=false，并写入 partial_reasons。
func BuildCoverage(all []TargetInfo, byID map[string]TargetCoverage, onlineOnly bool, excluded []TargetInfo, enum EnumerationState) Coverage {
	cov := Coverage{EnumerationState: enum}
	seen := make(map[string]bool)

	appendOne := func(tc TargetCoverage) {
		if seen[tc.TargetID] {
			return
		}
		seen[tc.TargetID] = true
		cov.Targets = append(cov.Targets, tc)
	}

	// 按授权全集顺序输出，保证覆盖侧信道稳定。
	for _, t := range all {
		if tc, ok := byID[t.ID]; ok {
			appendOne(tc)
			continue
		}
		// 未出现在 byID：若在 excluded 中，用 readiness + online_only 原因。
		excludedHit := false
		for _, e := range excluded {
			if e.ID == t.ID {
				excludedHit = true
				tc := CoverageFromReadiness(e, []string{"online_only_excluded"})
				appendOne(tc)
				break
			}
		}
		if !excludedHit {
			// 查询扇出内但无结果：标记 not_ready/unknown，禁止当作 success。
			tc := CoverageFromReadiness(t, []string{"missing_coverage"})
			if tc.State == CoverageSuccess {
				tc.State = CoverageNotReady
			}
			appendOne(tc)
		}
	}

	// 工具：判定非 success。
	for _, tc := range cov.Targets {
		if tc.State != CoverageSuccess {
			cov.Complete = false
			beforeReasons := len(cov.PartialReasons)
			for _, rawReason := range tc.Reasons {
				if reason := normalizeCoverageReason(rawReason); reason != "" {
					cov.PartialReasons = appendUniqueReason(cov.PartialReasons, reason)
				}
			}
			if len(cov.PartialReasons) == beforeReasons {
				cov.PartialReasons = appendUniqueReason(cov.PartialReasons, reasonForCoverageState(tc.State))
			}
		}
	}
	if len(cov.Targets) > 0 && len(cov.PartialReasons) == 0 {
		cov.Complete = true
	}
	if onlineOnly && len(excluded) > 0 {
		cov.PartialReasons = appendUniqueReason(cov.PartialReasons, "OFFLINE")
		cov.Complete = false
	}
	if len(cov.Targets) == 0 {
		cov.Complete = false
		cov.PartialReasons = appendUniqueReason(cov.PartialReasons, "ENGINE_NOT_READY")
	}
	return cov
}

func normalizeCoverageReason(reason string) string {
	upper := strings.ToUpper(strings.TrimSpace(reason))
	switch upper {
	case "ENGINE_NOT_READY", "LOG_UNSUPPORTED", "LEGACY", "REHYDRATE_FAILED", "BACKLOG",
		"TRUNCATED", "OFFLINE", "ARCHIVE_MISSING", "GAP", "VIEW_STALE",
		"DUPLICATE_UNRESOLVED", "PERMISSION_REVOKED", "CANCELLED", "BUDGET_EXCEEDED",
		"EXPORT_INCOMPLETE", "RECOVERY_REQUIRED":
		return upper
	case "ARCHIVE_NOT_RESTORED":
		return "ARCHIVE_MISSING"
	case "LOG_RPC_UNSUPPORTED":
		return "LOG_UNSUPPORTED"
	case "NOT_READY", "MISSING_COVERAGE", "TARGET_MISSING_IN_WORKER_RESPONSE", "EMPTY_WORKER_RESPONSE",
		"WORKER_VIEW_FAILED":
		return "ENGINE_NOT_READY"
	case "ROW_OR_BYTE_BUDGET_CUT", "FACET_TRUNCATED", "FANOUT_BUDGET_EXCEEDED", "WORKER_TRUNCATED":
		return "TRUNCATED"
	case "JOURNAL_INCOMPLETE", "CONFLICT_GENERATION", "PROJECTION_INCOMPLETE":
		return "RECOVERY_REQUIRED"
	default:
		return ""
	}
}

func reasonForCoverageState(state CoverageState) string {
	switch state {
	case CoverageOffline:
		return "OFFLINE"
	case CoverageArchiveMissing:
		return "ARCHIVE_MISSING"
	case CoverageStale:
		return "VIEW_STALE"
	case CoveragePartial:
		return "GAP"
	default:
		return "ENGINE_NOT_READY"
	}
}

func appendUniqueReason(reasons []string, reason string) []string {
	if reason == "" {
		return reasons
	}
	for _, existing := range reasons {
		if existing == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func joinReasons(rs []string) string {
	if len(rs) == 0 {
		return ""
	}
	out := rs[0]
	for _, r := range rs[1:] {
		out += "|" + r
	}
	return out
}
