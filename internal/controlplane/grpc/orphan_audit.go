package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// OrphanAuditRecorder 落孤儿处置审计（FR-455/456）。由 service.AuditService 实现。
// 在 grpc 包内以接口声明，避免 grpc→service 反向依赖（service 已 import grpc）。
type OrphanAuditRecorder interface {
	RecordResultSafe(userID uint, action, targetType, targetID, detail, ip string, success bool, errMsg string)
}

// SetOrphanAuditRecorder 注入孤儿处置审计落库器（FR-455/456）。不注入则丢弃（保持向后兼容/测试默认）。
func (h *ControlPlaneHandler) SetOrphanAuditRecorder(r OrphanAuditRecorder) {
	h.orphanAudit = r
}

// orphanAuditAllowedActions 是允许落库的孤儿审计 action 白名单（FR-456 N2）。
//
// 与 docs/specs/restart-resilience/spec.md §审计动作表、proto/worker.proto 的 action 注释逐一对应：
//   - orphan.scan_detected / scan_disposed / scan_dispose_blocked：运行期周期扫描三态发现与处置；
//   - orphan.dispose_blocked / dispose_reaped：启动期接管兜底的误杀拦截与真孤儿清理；
//   - health.dead_detected / health.selfheal_restart / health.selfheal_exhausted /
//     health.circuit_broken / health.circuit_released：FR-459 健康巡检的假死告警、自愈重启、
//     自愈重启达上限（重启风暴护栏）、崩溃熔断与熔断解除（detail 内标 operator=auto）。
//
// 白名单是**强校验**（不在表内一律拒绝）：审计库是运维追责面，不能让 Worker 侧以任意 action 名写入。
// 新增动作必须同时更新本表、spec 与 proto 注释，三者保持一致。
var orphanAuditAllowedActions = map[string]struct{}{
	"orphan.scan_detected":        {},
	"orphan.scan_disposed":        {},
	"orphan.scan_dispose_blocked": {},
	"orphan.dispose_blocked":      {},
	"orphan.dispose_reaped":       {},
	"health.dead_detected":        {},
	"health.selfheal_restart":     {},
	"health.selfheal_exhausted":   {},
	"health.circuit_broken":       {},
	"health.circuit_released":     {},
}

// auditTargetTypeFor 按 action 归正审计 targetType（FR-459 复审项 11）：
// health.* 系动作的靶子是**在册实例**，此前硬编码 "orphan" 会让健康自愈/熔断在审计面
// 被误标为孤儿处置，按 targetType 过滤时归错类。
func auditTargetTypeFor(action string) string {
	if strings.HasPrefix(action, "health.") {
		return "health"
	}
	return "orphan"
}

const (
	// maxOrphanAuditActionLen action 名长度上限（纵深防御；白名单已隐含长度上限）。
	maxOrphanAuditActionLen = 64
	// maxOrphanAuditTargetLen targetId 长度上限。direct 孤儿以工作目录绝对路径为 target，须放得下。
	maxOrphanAuditTargetLen = 512
	// maxOrphanAuditDetailLen detail **载荷**长度上限（不含外层 nodeUuid 包裹的固定开销）。
	// detail 由 Worker 生成（含 cmdline 摘录等），须设上限防日志/列膨胀；超限按 rune 边界截断。
	maxOrphanAuditDetailLen = 4096
)

// ReportOrphanAudit Worker 上报一条孤儿处置/误杀拦截审计（FR-455/456）。
//
// 走注册/心跳同信道（Worker→CP 出站 gRPC，NAT 节点天然可达），凭 node_uuid+node_secret 鉴权
// （与 ReportCrashSnapshot 同源校验）。落 CP 审计库（RecordResultSafe），使孤儿处置/rescan/
// 误杀拦截「不静默」（spec §2.2）。老 CP 返回 Unimplemented，Worker 记日志丢弃。
//
// 输入校验（FR-456 N2，与 ReportCrashSnapshot 对齐）：
//   - action 必须命中白名单（否则 InvalidArgument，不落库）；
//   - action/targetId/detail 长度受限，detail 超限截断；
//   - targetId 能在库中解析为实例时，该实例必须属于上报节点（节点只能报自己的实例）。
func (h *ControlPlaneHandler) ReportOrphanAudit(ctx context.Context, req *workerpb.ReportOrphanAuditRequest) (*workerpb.ReportOrphanAuditResponse, error) {
	if req.NodeUuid == "" || req.NodeSecret == "" {
		return nil, status.Errorf(codes.Unauthenticated, "缺少节点身份（node_uuid/node_secret）")
	}
	var node model.Node
	if err := h.db.Where("uuid = ?", req.NodeUuid).First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, status.Errorf(codes.PermissionDenied, "节点身份校验失败")
		}
		return nil, err
	}
	if node.Secret != req.NodeSecret {
		slog.Warn("孤儿审计上报被拒：node_secret 不匹配", "uuid", req.NodeUuid)
		return nil, status.Errorf(codes.PermissionDenied, "节点身份校验失败")
	}

	action := strings.TrimSpace(req.Action)
	if action == "" || len(action) > maxOrphanAuditActionLen {
		return nil, status.Errorf(codes.InvalidArgument, "action 非法（为空或超长）")
	}
	if _, ok := orphanAuditAllowedActions[action]; !ok {
		slog.Warn("孤儿审计上报被拒：action 不在白名单", "node", node.Name, "action", action)
		return nil, status.Errorf(codes.InvalidArgument, "action 不在允许集合内")
	}

	// target↔node 绑定（FR-456 N2）：targetId 既可能是实例 UUID（wrapper/docker 孤儿），也可能是
	// 工作目录路径（direct 孤儿）。可在库中解析为实例时强制归属校验（负面清单：解析到他节点的实例
	// 即拒）；解析不到（目录路径 / 实例已从库中消失）时不做绑定——孤儿审计恰恰常发生在实例已被
	// 清册之后，强绑定会把这些最需要留痕的事件丢掉。
	if !h.orphanTargetBelongsToNode(req.TargetId, node.ID) {
		slog.Warn("孤儿审计上报被拒：target 实例不属于该节点", "node", node.Name, "target", req.TargetId)
		return nil, status.Errorf(codes.PermissionDenied, "target 不属于该节点")
	}

	if h.orphanAudit == nil {
		// 未装配审计落库器：确认收到即可（测试/未接线部署）。
		return &workerpb.ReportOrphanAuditResponse{}, nil
	}
	// 自动处置无登录用户：以 0 表示系统（与 OrphanRuntimeTracker.recordDisposeAudit 同源约定）。
	// detail 追加 nodeUuid 便于按节点溯源（N3）——同一 UUID 可能在不同节点上出现，缺节点维度难归因。
	h.orphanAudit.RecordResultSafe(0, action, auditTargetTypeFor(action),
		truncateUTF8(req.TargetId, maxOrphanAuditTargetLen),
		wrapOrphanAuditDetail(req.NodeUuid, truncateUTF8(req.Detail, maxOrphanAuditDetailLen)),
		"", req.Success, req.Error)
	return &workerpb.ReportOrphanAuditResponse{}, nil
}

// orphanTargetBelongsToNode 报告 targetID 是否可归属于 nodeID。
//
// 仅当 targetID 能在库中解析为实例、且该实例不属于 nodeID 时返回 false（拒绝）。
// 解析不到（非实例 UUID 的工作目录 target、实例不存在）或查询出错时返回 true（放行）——
// 孤儿审计是「实例可能已从 CP 清册消失」场景的留痕通道，绑定只是**附加**的越权防护，
// 不得因解析不到而丢掉审计。
func (h *ControlPlaneHandler) orphanTargetBelongsToNode(targetID string, nodeID uint) bool {
	if targetID == "" {
		return true
	}
	var inst model.Instance
	err := h.db.Select("id", "node_id").Where("uuid = ?", targetID).First(&inst).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			slog.Debug("孤儿审计 target 归属校验查询失败，放行", "target", targetID, "error", err)
		}
		return true
	}
	return inst.NodeID == nodeID
}

// wrapOrphanAuditDetail 把 Worker 上报的 detail 载荷包成带 nodeUuid 的 JSON 对象（N3）。
// 载荷本身是 Worker 生成的 JSON 对象；非 JSON 时退化为字符串字段，保证外层恒为合法 JSON。
func wrapOrphanAuditDetail(nodeUUID, rawPayload string) string {
	raw := strings.TrimSpace(rawPayload)
	if raw == "" {
		raw = "{}"
	}
	var payload any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		payload = raw // 非 JSON（不应发生）：退化为字符串，外层仍合法。
	}
	b, err := json.Marshal(map[string]any{"nodeUuid": nodeUUID, "payload": payload})
	if err != nil {
		return fmt.Sprintf(`{"nodeUuid":%q}`, nodeUUID)
	}
	return string(b)
}

// truncateUTF8 按 rune 边界把 s 截断到至多 max 字节（max<=0 视为不截断）。
// 逐 rune 累加避免在多字节字符中间切断（审计详情含中文，按字节截断会产生非法 UTF-8）。
func truncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := 0
	for i := range s {
		if i > max {
			break
		}
		cut = i
	}
	return s[:cut]
}
