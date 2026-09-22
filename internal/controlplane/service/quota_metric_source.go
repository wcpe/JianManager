package service

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// MetricQuotaSource 是 QuotaEnforcer 的运行期用量来源（FR-467 §2.1）。
//
// CPU / RSS 消费既有心跳采集（FR-170 ProcessMetricSnapshot：Worker 随心跳上报的
// 进程树 TOPN 样本），不新开采集通道；磁盘走按需的 GetInstanceResourceSnapshot
// （FR-399 通道，FR-467 起响应新增 WorkDirBytes）。
type MetricQuotaSource struct {
	db      *gorm.DB
	metrics *MetricService
	pool    *cpgrpc.ClientPool
}

// NewMetricQuotaSource 创建配额用量来源。
func NewMetricQuotaSource(db *gorm.DB, metrics *MetricService, pool *cpgrpc.ClientPool) *MetricQuotaSource {
	return &MetricQuotaSource{db: db, metrics: metrics, pool: pool}
}

// quotaDiskTimeout 按需磁盘快照的超时（与 Worker 侧统计预算同量级）。
const quotaDiskTimeout = 15 * time.Second

// errNilResourceSnapshot 按需资源快照 RPC 返回空响应（既不报错也没有数据）。
//
// 与「RPC 报错」分开：前者是协议层面的异常（Worker 实现问题），后者是通信/超时。
// 两者都必须让调用方看得见，不能与「实例未运行」混为一谈（R12）。
var errNilResourceSnapshot = errors.New("Worker 返回空的资源快照响应")

// quotaDiskContext 派生按需磁盘统计的 deadline（m-1）。
//
// 原先直接 `context.WithTimeout(context.Background(), quotaDiskTimeout)`，**不继承调用方 ctx**：
// 而 Worker 侧 instance_resource_snapshot.go 的注释把「入站 ctx 有 deadline」当作前提。
// 若某调用方传入更紧的 ctx（如 HTTP 请求 ctx），该前提被打破——请求已取消，RPC 仍会跑满 15s。
//
// 修复：deadline 派生自调用方 ctx（取消/超时可传递），调用点传 context.Background() 时
// 行为与原先完全一致。ctx 为 nil 时退回 Background，不给调用方制造 nil 接口陷阱。
func quotaDiskContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, quotaDiskTimeout)
}

// LatestProcessSample 取实例最近一拍的进程树 CPU/RSS。
//
// 「同拍」判定用最大 sampled_at：心跳上报的多个进程样本共享同一拍，按最大时间聚合
// 才能得到「这一刻整棵树」的用量，而不是把不同时刻的样本加在一起。
func (s *MetricQuotaSource) LatestProcessSample(instanceUUID string, since time.Time) (*quotaSample, error) {
	if instanceUUID == "" {
		return nil, gorm.ErrRecordNotFound
	}
	var latest time.Time
	if err := s.db.Model(&model.ProcessMetricSnapshot{}).
		Where("instance_uuid = ? AND sampled_at >= ?", instanceUUID, since.UTC()).
		Select("MAX(sampled_at)").Scan(&latest).Error; err != nil {
		return nil, err
	}
	if latest.IsZero() {
		// 窗口内无样本：返回零值样本（不报错），使「无数据」不参与连续计数。
		return &quotaSample{}, nil
	}
	var agg struct {
		CPU float64
		RSS uint64
	}
	if err := s.db.Model(&model.ProcessMetricSnapshot{}).
		Where("instance_uuid = ? AND sampled_at = ?", instanceUUID, latest).
		Select("COALESCE(SUM(cpu_percent), 0) as cpu, COALESCE(SUM(rss_bytes), 0) as rss").
		Scan(&agg).Error; err != nil {
		return nil, err
	}
	return &quotaSample{CPUPercent: agg.CPU, RSSBytes: int64(agg.RSS)}, nil
}

// InstanceResourceUsage 取实例**全进程树**的 RSS / CPU 与工作目录占用（FR-467 M-2 修复）。
//
// 为什么不复用心跳里的 LatestProcessSample 作为内存判据：心跳的进程样本被裁为
// 「每实例最多 10 个进程、按 CPU 排序」（worker/heartbeat/process_metrics.go）。
// 用它做内存维度有两个方向的系统性偏差：
//   - 漏判：MC 的常驻内存主要在 JVM 主进程，但一旦主进程 CPU 低而若干子进程 CPU 高，
//     10 条截断会把主进程（RSS 最大者）挤出样本，SUM 出来的 RSS 远低于真实占用；
//   - 误停：反过来在 CPU 全落在主进程时又恰好准确，行为在两种形态间摇摆。
//
// GetInstanceResourceSnapshot（FR-399 通道）在 Worker 侧遍历**完整**进程树后求和，
// 不受 TOPN 截断影响，是本维度唯一可靠的来源。
//
// 一次 RPC 同时取回 RSS/CPU/磁盘（s-5）：原先每实例要发两次按需 RPC（进程 + 磁盘），
// 64 服规模下巡检周期被串行 RPC 拖长；合并为一次后 RPC 数减半。
// 返回值中 Available=false 表示 Worker 侧不可用（实例未运行/节点离线），调用方必须
// 显式区分「不可用」与「占用为 0」，不得把未采集当作未超限。
//
// R12：**「无数据」与「查询失败」必须可区分**。原先四种失败（DB 查节点失败、
// 节点未连接、RPC 失败、resp==nil）一律 `return nil, false, nil`，error 恒为 nil，
// 调用方无法分辨「节点离线」与「Worker 报不可用」——节点离线期间配额巡检静默漏检，
// 无任何告警。现在：查节点失败与 RPC 失败**带上 error 返回**，让调用方按
// 「不可采样」计数告警；仅「实例本身没有可采数据」（未运行、节点无连接）保持
// `(nil, false, nil)`——那属于预期内的正常状态，不应刷错误日志。
func (s *MetricQuotaSource) InstanceResourceUsage(ctx context.Context, instanceID uint, instanceUUID string) (*quotaSample, bool, error) {
	if s.pool == nil || instanceUUID == "" {
		return nil, false, nil
	}
	var node model.Node
	if err := s.db.Model(&model.Instance{}).
		Select("nodes.uuid").Joins("JOIN nodes ON nodes.id = instances.node_id").
		Where("instances.id = ?", instanceID).Scan(&node.UUID).Error; err != nil {
		return nil, false, err
	}
	if node.UUID == "" {
		// 实例不存在或其节点记录已删：无数据可采（不是查询失败）。
		return nil, false, nil
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		// 节点未连接（离线）：预期内的不可采样状态，由调用方的「不可用」计数兜住。
		return nil, false, nil
	}
	// m-1：deadline 派生自传入 ctx，使调用方的取消/超时可传递（调用点传 Background
	// 即等价于改造前行为）。见 quotaDiskContext。
	rpcCtx, cancel := quotaDiskContext(ctx)
	defer cancel()
	resp, err := client.Worker.GetInstanceResourceSnapshot(rpcCtx, &workerpb.GetInstanceResourceSnapshotRequest{
		InstanceUuid: instanceUUID,
	})
	if err != nil {
		return nil, false, err
	}
	if resp == nil {
		return nil, false, errNilResourceSnapshot
	}
	sample := &quotaSample{}
	if resp.RssAvailable {
		sample.RSSBytes = resp.ProcessRssBytes
	}
	if resp.CpuAvailable {
		sample.CPUPercent = resp.CpuPercent
	}
	if resp.WorkDirAvailable {
		sample.DiskBytes = resp.WorkDirBytes
	}
	return sample, resp.RssAvailable || resp.CpuAvailable || resp.WorkDirAvailable, nil
}

// InstanceWorkDirBytes 只取实例工作目录占用（FR-467 磁盘维度）。
//
// 与 InstanceResourceUsage 并存的原因：配额视图（Status）只需要磁盘一项，
// 不必为它多读一次进程树；巡检主路径则用合并版 InstanceResourceUsage 省一次 RPC。
// 实例未运行时返回 0：进程不在，磁盘不再增长，无需强制。
//
// R12：同样区分「无数据」（返回 0, nil）与「查询/RPC 失败」（返回 error），
// 使读侧能把「磁盘占用为 0」与「拿不到磁盘占用」分开呈现。
func (s *MetricQuotaSource) InstanceWorkDirBytes(ctx context.Context, instanceID uint, instanceUUID string) (int64, error) {
	if s.pool == nil || instanceUUID == "" {
		return 0, nil
	}
	var node model.Node
	if err := s.db.Model(&model.Instance{}).
		Select("nodes.uuid").Joins("JOIN nodes ON nodes.id = instances.node_id").
		Where("instances.id = ?", instanceID).Scan(&node.UUID).Error; err != nil {
		return 0, err
	}
	if node.UUID == "" {
		return 0, nil
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return 0, nil
	}
	rpcCtx, cancel := quotaDiskContext(ctx)
	defer cancel()
	resp, err := client.Worker.GetInstanceResourceSnapshot(rpcCtx, &workerpb.GetInstanceResourceSnapshotRequest{
		InstanceUuid: instanceUUID,
	})
	if err != nil {
		return 0, err
	}
	if resp == nil || !resp.WorkDirAvailable {
		// 响应为空或未带磁盘维度：无数据可读（非失败），保持 0。
		return 0, nil
	}
	return resp.WorkDirBytes, nil
}
