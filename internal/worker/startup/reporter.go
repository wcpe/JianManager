package startup

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// Reporter 把启动阶段进度上报给 CP（范围②，方案 A）。
//
// ## 设计约束（每条都是为了「观测不拖累启动」）
//
//   - **best-effort**：任何失败只记 debug 日志、不重试、不返回错误给调用方；
//   - **短超时**：单次上报 2s 封顶。启动期 CP 可能还没起来（或正在重启），
//     若用默认超时，一个连不上的 CP 会让每个阶段都卡几秒——观测反倒把启动拖慢；
//   - **懒连接**：首次上报时才建连接；没有 CP 地址或建连失败则整体静默禁用；
//   - **不消费 enroll token**：CP 侧只读校验（ValidateOnly），token 留给正式注册。
//
// ## 为什么单独建连接而不复用注册用的连接
//
// 注册发生在加载**之后**，那时才建立主连接；而本上报要在注册前工作，
// 只能自建一条。用 `insecure` 与注册侧一致（隧道/直拨由 CP 地址决定，Worker 不额外校验）。
type Reporter struct {
	addr string
	// enrollToken 首装凭据；有本地身份时改用 uuid+secret（两者互斥）。
	enrollToken string
	nodeUUID    string
	nodeSecret  string
	nodeName    string

	conn   *grpc.ClientConn
	client workerpb.WorkerServiceClient
	// disabled 一旦建连失败即置位：后续阶段不再尝试，避免每段都付一次建连超时。
	disabled bool
}

// ReporterConfig 上报配置。
//
// 全部字段都可缺省：addr 为空即整个上报被禁用（不产生任何连接尝试）。
type ReporterConfig struct {
	// ControlPlaneAddr CP 的 gRPC 地址（与 worker.yml control_plane 同值）。
	ControlPlaneAddr string
	// EnrollToken 首装 enrollment token（可空）。
	EnrollToken string
	// NodeUUID / NodeSecret 已有身份（重启场景，可空）。
	NodeUUID   string
	NodeSecret string
	// NodeName 节点名，用于首装场景让 CP 知道「谁在启动」。
	NodeName string
}

// NewReporter 创建上报器。addr 为空时返回 nil（调用方据此跳过上报）。
func NewReporter(cfg ReporterConfig) *Reporter {
	if cfg.ControlPlaneAddr == "" {
		return nil
	}
	return &Reporter{
		addr:        cfg.ControlPlaneAddr,
		enrollToken: cfg.EnrollToken,
		nodeUUID:    cfg.NodeUUID,
		nodeSecret:  cfg.NodeSecret,
		nodeName:    cfg.NodeName,
	}
}

// reportTimeout 单次上报上限。启动期宁可丢一条进度，也不能卡住启动。
const reportTimeout = 2 * time.Second

// Report 上报一次进度。fn 为 nil 时直接返回。
//
// 参数 semantics：
//   - phase 当前阶段；
//   - phaseCost 该阶段已耗时（0 表示刚开始）；
//   - total 自进程启动起的总耗时；
//   - done / totalPhases 供面板渲染 n/m。
//
// 所有错误都被吞掉：调用方在启动主链路上，不该为观测写任何 if err。
func (r *Reporter) Report(phase Phase, phaseCost, total time.Duration, done, totalPhases int) {
	if r == nil || r.disabled {
		return
	}
	client, err := r.ensureClient()
	if err != nil {
		// 建连失败即永久禁用：后续阶段不必再各自等一次超时。
		r.disabled = true
		slog.Info("启动进度上报已禁用（无法连接 CP）", "addr", r.addr, "error", err)
		return
	}

	slog.Info("启动进度上报尝试", "phase", phase.String(), "addr", r.addr,
		"credKind", credKind(r), "enrollLen", len(r.enrollToken))

	ctx, cancel := context.WithTimeout(context.Background(), reportTimeout)
	defer cancel()

	resp, err := client.ReportStartupProgress(ctx, &workerpb.ReportStartupProgressRequest{
		Name:           r.nodeName,
		Phase:          phase.String(),
		PhaseElapsedMs: phaseCost.Milliseconds(),
		TotalElapsedMs: total.Milliseconds(),
		PhasesDone:     int32(done),
		PhasesTotal:    int32(totalPhases),
		EnrollToken:    r.enrollToken,
		NodeUuid:       r.nodeUUID,
		NodeSecret:     r.nodeSecret,
	})
	if err != nil {
		// 老 CP 无此 RPC（Unimplemented）也会走到这里：一次性禁用，不刷日志。
		r.disabled = true
		slog.Info("启动进度上报失败（将不再尝试）", "phase", phase.String(), "error", err)
		return
	}
	if !resp.GetAccepted() {
		r.disabled = true
		slog.Info("启动进度被 CP 拒绝（将不再尝试）", "phase", phase.String(), "reason", resp.GetReason())
	}
}

// ensureClient 懒建连接。
func (r *Reporter) ensureClient() (workerpb.WorkerServiceClient, error) {
	if r.client != nil {
		return r.client, nil
	}
	conn, err := grpc.NewClient(r.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	r.conn = conn
	r.client = workerpb.NewWorkerServiceClient(conn)
	return r.client, nil
}

// SetIdentity 在注册成功后切换到真实节点身份。
//
// 为什么必须切：enrollment token 是一次性凭据，Register 成功时已被 CP 消费。
// 之后若仍拿它上报，CP 的只读校验必然失败（token 已 used），表现为
// 「注册之后的所有进度上报都被拒」——而那正是 register / heartbeat 两个阶段。
//
// 切换后同时**清除 disabled**：注册成功意味着 CP 现在可达且这层鉴权有效，
// 之前因 token 失效导致的禁用不应继续生效。
func (r *Reporter) SetIdentity(nodeUUID, nodeSecret string) {
	if r == nil {
		return
	}
	r.nodeUUID = nodeUUID
	r.nodeSecret = nodeSecret
	r.enrollToken = "" // 一次性凭据已消费，不再携带（避免误导 CP 侧判定）
	r.disabled = false
}

// Close 释放连接。未建立过连接时安全。
func (r *Reporter) Close() {
	if r == nil || r.conn == nil {
		return
	}
	_ = r.conn.Close()
	r.conn = nil
	r.client = nil
}

// credKind 报告当前凭据种类（仅诊断用，不泄露内容）。
func credKind(r *Reporter) string {
	switch {
	case r.enrollToken != "":
		return "enroll"
	case r.nodeUUID != "" && r.nodeSecret != "":
		return "identity"
	default:
		return "none"
	}
}
