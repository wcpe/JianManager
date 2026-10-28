package grpc

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ReportStartupProgress 接收 Worker 的启动阶段进度（FR-486 范围②，方案 A）。
//
// ## 为什么不复用 Register / Heartbeat
//
// 时间上都不成立：
//   - Register 发生在加载**之后**（Worker 侧 newIngest → 再注册），而瓶颈恰恰在加载期；
//   - Heartbeat 更晚（注册成功后才启动）。
//
// 所以要有一条**注册前**就能用的通道，这正是本 RPC 的存在理由。
//
// ## 鉴权：为什么接受两种身份
//
// Worker 在这个时点的身份状态是不确定的：
//   - 首次启动：只有 enrollment token；
//   - 已有身份（重启）：有 node_uuid + node_secret，但 enroll token 早在首次注册时作废。
//
// 因此两种都要接受。两者都没有则拒绝——否则任何进程都能伪造「某节点正在启动」，
// 让面板展示虚假进度（一种误导运维的注入面）。
//
// ## 语义刻意保守
//
// - **绝不构成就绪**：写过进度≠可服务，就绪判据仍只看注册与心跳；
// - **best-effort**：拒绝时返回 accepted=false 而非让 Worker 重试——启动路径不能被观测拖慢；
// - 不因进度上报而创建正式节点记录（那会伪造出「存在的节点」），只写独立的进度表。
func (h *ControlPlaneHandler) ReportStartupProgress(
	_ context.Context, req *workerpb.ReportStartupProgressRequest,
) (*workerpb.ReportStartupProgressResponse, error) {
	if req == nil {
		return &workerpb.ReportStartupProgressResponse{Accepted: false, Reason: "空请求"}, nil
	}
	// 诊断级日志：这条 RPC 只在启动期每节点调用几次，量极小；
	// 打 Info 是为了让「面板没数据」时能立刻分清「Worker 没上报」与「CP 没落库」。
	slog.Info("收到启动进度上报", "name", req.GetName(), "phase", req.GetPhase(),
		"hasEnroll", req.GetEnrollToken() != "", "hasIdentity", req.GetNodeUuid() != "")

	// 身份解析：优先 node_uuid+secret（重启），回退 enroll token（首装）。

	claimedUUID := strings.TrimSpace(req.GetNodeUuid())
	claimedSecret := strings.TrimSpace(req.GetNodeSecret())
	enrollToken := strings.TrimSpace(req.GetEnrollToken())

	var nodeUUID string
	switch {
	case claimedUUID != "" && claimedSecret != "":
		var node model.Node
		if err := h.db.Where("uuid = ?", claimedUUID).First(&node).Error; err != nil {
			return &workerpb.ReportStartupProgressResponse{Accepted: false, Reason: "节点身份不存在"}, nil
		}
		if node.Secret != claimedSecret {
			// 不区分「不存在」与「secret 错」，避免成为节点 UUID 的探测接口。
			return &workerpb.ReportStartupProgressResponse{Accepted: false, Reason: "节点身份校验失败"}, nil
		}
		nodeUUID = node.UUID
	case enrollToken != "":
		// enrollment token 只校验有效性、**不消费**：它还要留给随后的 Register 用。
		// 这里只做「能不能证明你被授权启动」的粗判。
		if h.enroll == nil || !h.enroll.ValidateOnly(enrollToken) {
			return &workerpb.ReportStartupProgressResponse{Accepted: false, Reason: "enrollment token 无效"}, nil
		}
		// 首装时 CP 还没有这个节点的 UUID：用请求里的名字作占位键，
		// 注册成功后由正式节点记录接管（见 node_startup_progresses 的 key 说明）。
		nodeUUID = ""
	default:
		return &workerpb.ReportStartupProgressResponse{Accepted: false, Reason: "需要节点身份或 enrollment token"}, nil
	}

	phase := strings.TrimSpace(req.GetPhase())
	if phase == "" {
		return &workerpb.ReportStartupProgressResponse{Accepted: false, Reason: "phase 不能为空"}, nil
	}

	// 终态阶段：启动已完成，进度不再有意义 → 删除现有行而不是写入。
	//
	// 为什么必须在这里处理：Worker 的 heartbeat 阶段上报发生在**注册成功之后**
	//（注册 → SetIdentity → 上报 register → 启动心跳 → 上报 heartbeat）。
	// 注册时虽然已经 MarkStartupProgressDone 清过一次，但紧随其后的 heartbeat 上报
	// 会把它重新写回来，导致面板永远显示「启动中 · heartbeat」——一个已上线的节点
	// 被永久标成正在启动，比没有进度更有害。
	//
	// 判据用「阶段名」而不是「phases_done == phases_total」：后者依赖 Worker 侧
	// 总数与 CP 侧认知一致，一旦两侧阶段集合演进不同步就会失效。
	if phase == startupPhaseHeartbeat {
		if nodeUUID != "" {
			MarkStartupProgressDone(h.db, nodeUUID)
		}
		return &workerpb.ReportStartupProgressResponse{Accepted: true}, nil
	}

	// 键的选择：有 UUID 用 UUID（重启场景可稳定覆盖同一行）；无 UUID 用 "name:<名字>"
	//（首装场景），注册成功后 CP 会把该行归并到真实 UUID（见 RekeyStartupProgress）。
	key := nodeUUID
	if key == "" {
		name := strings.TrimSpace(req.GetName())
		if name == "" {
			return &workerpb.ReportStartupProgressResponse{Accepted: false, Reason: "首装上报需携带节点名"}, nil
		}
		key = startupProgressKeyByName(name)
	}

	now := time.Now()
	row := model.NodeStartupProgress{
		Key:            key,
		NodeUUID:       nodeUUID,
		NodeName:       req.GetName(),
		Phase:          phase,
		PhaseElapsedMs: req.GetPhaseElapsedMs(),
		TotalElapsedMs: req.GetTotalElapsedMs(),
		PhasesDone:     req.GetPhasesDone(),
		PhasesTotal:    req.GetPhasesTotal(),
		UpdatedAt:      now,
	}
	// Upsert：同一键只保留最新进度（进度是「当前状态」，不是历史）。
	//
	// 用 OnConflict 显式声明「主键冲突则更新」：
	// 这里既不能用 Create（第二次上报就主键冲突报错），
	// 也不能用 Save（它按主键覆盖全字段，对未赋值的列会写零值）。
	// 手写列清单是有意的——只更新进度相关列，不碰其它列。
	if err := h.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"node_uuid", "node_name", "phase",
			"phase_elapsed_ms", "total_elapsed_ms",
			"phases_done", "phases_total", "updated_at",
		}),
	}).Create(&row).Error; err != nil {
		slog.Warn("记录启动进度失败", "key", key, "phase", phase, "error", err)
		return &workerpb.ReportStartupProgressResponse{Accepted: false, Reason: "落库失败"}, nil
	}

	return &workerpb.ReportStartupProgressResponse{Accepted: true}, nil
}

// startupPhaseHeartbeat 是启动链路的终态阶段（与 Worker 侧 startup.PhaseHeartbeat 对齐）。
const startupPhaseHeartbeat = "heartbeat"

// startupProgressKeyByName 首装场景的占位键。
//
// 为什么要前缀：与 UUID 键同表共存，若直接用名字，一个恰好叫某 UUID 的节点名
// 会与真节点行撞键。前缀让两类键在结构上不相交。
func startupProgressKeyByName(name string) string { return "name:" + name }

// RekeyStartupProgress 注册成功后把「按名字」的进度行归并到真实 UUID。
//
// 为什么需要：首装时 Worker 还没有 UUID（CP 才签发），进度只能按名字记；
// 注册拿到 UUID 后若不归并，面板会同时看到「按名字的旧行」和后续心跳驱动的真节点，
// 表现为「一个节点在启动中、另一个同名节点已在线」——比没有进度更误导。
//
// 实现为尽力而为：归并失败只告警，不影响注册结果。
func RekeyStartupProgress(db *gorm.DB, nodeName, nodeUUID string) {
	if db == nil || strings.TrimSpace(nodeName) == "" || strings.TrimSpace(nodeUUID) == "" {
		return
	}
	oldKey := startupProgressKeyByName(nodeName)
	res := db.Model(&model.NodeStartupProgress{}).
		Where("key = ?", oldKey).
		Updates(map[string]any{"key": nodeUUID, "node_uuid": nodeUUID})
	if res.Error != nil {
		slog.Warn("归并启动进度行失败", "name", nodeName, "uuid", nodeUUID, "error", res.Error)
	}
}

// MarkStartupProgressDone 注册完成即删除进度行（节点已可服务，进度不再有意义）。
//
// 保留会让面板持续显示一条陈旧的「启动中」——必须显式清除，不能等 TTL。
func MarkStartupProgressDone(db *gorm.DB, nodeUUID string) {
	if db == nil || strings.TrimSpace(nodeUUID) == "" {
		return
	}
	if err := db.Where("key = ? OR node_uuid = ?", nodeUUID, nodeUUID).
		Delete(&model.NodeStartupProgress{}).Error; err != nil {
		slog.Warn("清理启动进度行失败", "uuid", nodeUUID, "error", err)
	}
}
