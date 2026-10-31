package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

var (
	ErrNodeNotFound = errors.New("节点不存在")
	// ErrNodeInMaintenance 目标节点处于维护模式，拒绝新实例调度。参见。
	ErrNodeInMaintenance = errors.New("节点处于维护模式，已拒绝新实例调度")
	// ErrNodeNotArchived 目标节点未下线（非软删归档），拒绝彻底清理。参见。
	ErrNodeNotArchived = errors.New("节点未下线，请先下线再清理")
)

// NodeService 节点管理服务。
type NodeService struct {
	db *gorm.DB
	// instanceSvc 用于排空（drain）时停止节点上的运行实例。
	// 同包内通过 SetInstanceService 注入，规避构造期循环依赖。
	instanceSvc *InstanceService
	pool        *cpgrpc.ClientPool
	// tunnelStatus 反向隧道状态来源；nil 表示未启用，响应恒 false。
	tunnelStatus TunnelStatus
}

// NewNodeService 创建节点服务。
func NewNodeService(db *gorm.DB) *NodeService {
	return &NodeService{db: db}
}

// SetInstanceService 注入实例服务，供排空（drain）复用实例停止逻辑。
// 与 NewNodeService 分离是因为 InstanceService 也依赖其它服务，
// 在 main 装配阶段二者均就绪后再回填，避免构造顺序耦合。
func (s *NodeService) SetInstanceService(instanceSvc *InstanceService) {
	s.instanceSvc = instanceSvc
}

// SetClientPool 注入 Worker 连接池，供节点实时指标按需拉取。
func (s *NodeService) SetClientPool(pool *cpgrpc.ClientPool) {
	s.pool = pool
}

// TunnelStatus 报告节点反向隧道连接状态（见 ADR-066）；由 TunnelRegistry 实现。
type TunnelStatus interface {
	Connected(nodeUUID string) bool
}

// SetTunnelStatus 注入隧道状态来源；main 装配时调用一次。未注入时响应恒为 false。
func (s *NodeService) SetTunnelStatus(ts TunnelStatus) {
	s.tunnelStatus = ts
}

// fillTunnelState 填充节点的运行态隧道字段（响应期计算，不落库）。
func (s *NodeService) fillTunnelState(node *model.Node) {
	if s.tunnelStatus != nil {
		node.TunnelConnected = s.tunnelStatus.Connected(node.UUID)
	}
}

// RegisterRequest 节点注册请求。
type RegisterRequest struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	GRPCPort    int    `json:"grpcPort"`
	WSPort      int    `json:"wsPort"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPUCores    int    `json:"cpuCores"`
	MemoryMB    int64  `json:"memoryMb"`
	DiskTotalMB int64  `json:"diskTotalMb"`
}

// RegisterResult 节点注册结果。
type RegisterResult struct {
	NodeUUID   string `json:"nodeUuid"`
	NodeSecret string `json:"nodeSecret"`
}

// Register 节点首次注册。已注册节点通过 secret 重新连接。
func (s *NodeService) Register(req RegisterRequest) (*RegisterResult, error) {
	secret, err := generateSecret()
	if err != nil {
		return nil, fmt.Errorf("生成节点密钥失败: %w", err)
	}

	node := &model.Node{
		Name:          req.Name,
		Host:          req.Host,
		GRPCPort:      req.GRPCPort,
		WSPort:        req.WSPort,
		Secret:        secret,
		Status:        model.NodeStatusOnline,
		OS:            req.OS,
		Arch:          req.Arch,
		CPUCores:      req.CPUCores,
		MemoryMB:      req.MemoryMB,
		DiskTotalMB:   req.DiskTotalMB,
		LastHeartbeat: ptrTime(time.Now()),
	}

	if err := s.db.Create(node).Error; err != nil {
		return nil, fmt.Errorf("注册节点失败: %w", err)
	}

	return &RegisterResult{
		NodeUUID:   node.UUID,
		NodeSecret: secret,
	}, nil
}

// HeartbeatData 心跳上报数据。
type HeartbeatData struct {
	CPUUsage     float64 `json:"cpuUsage"`
	MemoryUsage  float64 `json:"memoryUsage"`
	DiskUsage    float64 `json:"diskUsage"`
	MemoryUsedMB int64   `json:"memoryUsedMb"`
	DiskUsedMB   int64   `json:"diskUsedMb"`
}

// Heartbeat 处理节点心跳。
func (s *NodeService) Heartbeat(nodeUUID string, data HeartbeatData) error {
	now := time.Now()
	result := s.db.Model(&model.Node{}).Where("uuid = ?", nodeUUID).Updates(map[string]interface{}{
		"status":         model.NodeStatusOnline,
		"last_heartbeat": &now,
	})
	if result.RowsAffected == 0 {
		return ErrNodeNotFound
	}
	return result.Error
}

// List 返回所有节点。
func (s *NodeService) List() ([]model.Node, error) {
	var nodes []model.Node
	if err := s.db.Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询节点列表失败: %w", err)
	}
	for i := range nodes {
		s.fillTunnelState(&nodes[i])
	}
	return nodes, nil
}

// GetByID 按 ID 获取节点。
func (s *NodeService) GetByID(id uint) (*model.Node, error) {
	var node model.Node
	if err := s.db.First(&node, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	s.fillTunnelState(&node)
	return &node, nil
}

// NodeMetricsData 节点实时指标快照。
type NodeMetricsData struct {
	CPUUsage      float32 `json:"cpuUsage"`
	MemoryUsage   float32 `json:"memoryUsage"`
	DiskUsage     float32 `json:"diskUsage"`
	MemoryUsedMB  int64   `json:"memoryUsedMb"`
	MemoryTotalMB int64   `json:"memoryTotalMb"`
	DiskUsedMB    int64   `json:"diskUsedMb"`
	DiskTotalMB   int64   `json:"diskTotalMb"`
}

// GetMetrics 返回节点实时指标；Worker 已连接时主动拉取，未连接时回退最新心跳快照。
func (s *NodeService) GetMetrics(id uint) (*NodeMetricsData, error) {
	node, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	if s.pool != nil {
		if client, ok := s.pool.Get(node.UUID); ok && client.Worker != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := client.Worker.GetNodeMetrics(ctx, &workerpb.GetNodeMetricsRequest{})
			if err != nil {
				return nil, fmt.Errorf("获取节点指标失败: %w", err)
			}
			return nodeMetricsFromWorker(resp), nil
		}
	}
	return nodeMetricsFromSnapshot(node), nil
}

func nodeMetricsFromSnapshot(node *model.Node) *NodeMetricsData {
	return &NodeMetricsData{
		CPUUsage:      node.CPUUsage,
		MemoryUsage:   node.MemoryUsage,
		DiskUsage:     node.DiskUsage,
		MemoryUsedMB:  node.MemoryUsedMB,
		MemoryTotalMB: node.MemoryMB,
		DiskUsedMB:    node.DiskUsedMB,
		DiskTotalMB:   node.DiskTotalMB,
	}
}

func nodeMetricsFromWorker(resp *workerpb.GetNodeMetricsResponse) *NodeMetricsData {
	if resp == nil {
		return &NodeMetricsData{}
	}
	return &NodeMetricsData{
		CPUUsage:      resp.CpuUsage,
		MemoryUsage:   resp.MemoryUsage,
		DiskUsage:     resp.DiskUsage,
		MemoryUsedMB:  resp.MemoryUsedMb,
		MemoryTotalMB: resp.MemoryTotalMb,
		DiskUsedMB:    resp.DiskUsedMb,
		DiskTotalMB:   resp.DiskTotalMb,
	}
}

// GetByUUID 按 UUID 获取节点。
func (s *NodeService) GetByUUID(uuid string) (*model.Node, error) {
	var node model.Node
	if err := s.db.Where("uuid = ?", uuid).First(&node).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	return &node, nil
}

// SetMaintenance 置/解节点维护模式（cordon）。
// 维护模式只影响新实例调度（见 ScheduleAllowed），不触碰已运行实例，
// 也不改变节点在线/离线状态。参见。
func (s *NodeService) SetMaintenance(id uint, enabled bool) (*model.Node, error) {
	node, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	if err := s.db.Model(node).Update("maintenance", enabled).Error; err != nil {
		return nil, fmt.Errorf("更新维护模式失败: %w", err)
	}
	node.Maintenance = enabled
	return node, nil
}

// DrainResult 排空结果，汇总本次停止的实例数与失败明细。
type DrainResult struct {
	StoppedCount int      `json:"stoppedCount"`
	Stopped      []uint   `json:"stopped"`
	Failed       []uint   `json:"failed"`
	Errors       []string `json:"errors,omitempty"`
}

// Drain 排空节点：停止其上所有运行中（含启动中）的实例。
// 复用 InstanceService.Stop（经 gRPC 委托 Worker 优雅停止），不做实例迁移（迁移为后续可选）。
// 排空不强制要求节点已处于维护模式，但调用方通常先 cordon 再 drain 以防停止过程中又有新实例落入。
// 参见。
func (s *NodeService) Drain(id uint) (*DrainResult, error) {
	if _, err := s.GetByID(id); err != nil {
		return nil, err
	}
	if s.instanceSvc == nil {
		return nil, fmt.Errorf("排空不可用：实例服务未注入")
	}

	// 仅停止处于 RUNNING 的实例：状态机只允许 RUNNING→STOPPING，
	// STARTING 为瞬态（即将进入 RUNNING 或 CRASHED），不在此处强停以免无效转换。
	// 已停止/崩溃的无需处理。
	var instances []model.Instance
	if err := s.db.Where("node_id = ? AND status = ?", id, model.InstanceStatusRunning).
		Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询节点实例失败: %w", err)
	}

	result := &DrainResult{Stopped: []uint{}, Failed: []uint{}}
	for _, inst := range instances {
		if err := s.instanceSvc.Stop(inst.ID); err != nil {
			result.Failed = append(result.Failed, inst.ID)
			result.Errors = append(result.Errors, fmt.Sprintf("实例 %d: %v", inst.ID, err))
			continue
		}
		result.Stopped = append(result.Stopped, inst.ID)
	}
	result.StoppedCount = len(result.Stopped)
	return result, nil
}

// NodeInstanceBrief 节点删除守卫随拒绝返回的实例摘要，供前端展示阻断清单。
type NodeInstanceBrief struct {
	ID     uint                 `json:"id"`
	Name   string               `json:"name"`
	Status model.InstanceStatus `json:"status"`
}

// NodeHasInstancesError 节点名下仍有实例、拒绝删除。
// 用类型化错误携带实例清单，handler 据此组装 409 响应体。
type NodeHasInstancesError struct {
	Instances []NodeInstanceBrief
}

// Error 实现 error 接口。
func (e *NodeHasInstancesError) Error() string {
	return fmt.Sprintf("节点名下仍有 %d 个实例，请先删除或迁移实例后再下线", len(e.Instances))
}

// NodeDeleteResult 节点删除结果：force 级联时报告删除的实例记录数。
type NodeDeleteResult struct {
	InstancesPurged int `json:"instancesPurged"`
}

// Delete 主动下线节点：解除注册并保留记录（软删除），复连需重新注册。
// 安全约束：
//   - 节点在线时一律拒绝（force 亦无效）——活节点必须先排空并断开 Worker，force 仅为
//     离线节点的孤儿记录兜底；
//   - 名下仍有实例且未 force 时拒绝并返回实例清单（*NodeHasInstancesError），避免删节点
//     留下孤儿实例（其终端/操作全 404「节点不存在」）；
//   - 离线节点 force=true 显式级联：同事务软删名下实例记录及其组/群组服/群组成员关联行
//     （口径与 InstanceService.Delete 的记录级联一致）。节点离线无法委托 Worker 清理，
//     远端实例文件一律保留，由调用方明示用户。
//
// 软删除（gorm.DeletedAt）保留历史审计与实例归属；Worker 复连时 Register 重新建档获得新 UUID/secret。
func (s *NodeService) Delete(id uint, force bool) (*NodeDeleteResult, error) {
	node, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	if node.Status == model.NodeStatusOnline {
		return nil, fmt.Errorf("不能删除在线节点")
	}

	var instances []model.Instance
	if err := s.db.Where("node_id = ?", id).Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询节点实例失败: %w", err)
	}
	if len(instances) > 0 && !force {
		briefs := make([]NodeInstanceBrief, 0, len(instances))
		for _, inst := range instances {
			briefs = append(briefs, NodeInstanceBrief{ID: inst.ID, Name: inst.Name, Status: inst.Status})
		}
		return nil, &NodeHasInstancesError{Instances: briefs}
	}

	result := &NodeDeleteResult{}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if len(instances) > 0 {
			ids := make([]uint, 0, len(instances))
			for _, inst := range instances {
				ids = append(ids, inst.ID)
			}
			if err := tx.Where("instance_id IN ?", ids).Delete(&model.GroupInstance{}).Error; err != nil {
				return err
			}
			if err := tx.Where("proxy_id IN ? OR backend_id IN ?", ids, ids).Delete(&model.ServerRegistration{}).Error; err != nil {
				return err
			}
			if err := tx.Where("instance_id IN ?", ids).Delete(&model.NetworkMember{}).Error; err != nil {
				return err
			}
			if err := tx.Where("node_id = ?", id).Delete(&model.Instance{}).Error; err != nil {
				return err
			}
			result.InstancesPurged = len(ids)
		}
		return tx.Delete(&model.Node{}, id).Error
	})
	if err != nil {
		return nil, fmt.Errorf("删除节点失败: %w", err)
	}
	return result, nil
}

// ArchivedNode 归档节点对外视图：公开字段 + deletedAt，不含 secret。
type ArchivedNode struct {
	ID            uint             `json:"id"`
	UUID          string           `json:"uuid"`
	Name          string           `json:"name"`
	Host          string           `json:"host"`
	GRPCPort      int              `json:"grpcPort"`
	WSPort        int              `json:"wsPort"`
	Status        model.NodeStatus `json:"status"`
	OS            string           `json:"os"`
	Arch          string           `json:"arch"`
	CPUCores      int              `json:"cpuCores"`
	MemoryMB      int64            `json:"memoryMb"`
	DiskTotalMB   int64            `json:"diskTotalMb"`
	Maintenance   bool             `json:"maintenance"`
	LastHeartbeat *time.Time       `json:"lastHeartbeat"`
	CreatedAt     time.Time        `json:"createdAt"`
	UpdatedAt     time.Time        `json:"updatedAt"`
	DeletedAt     time.Time        `json:"deletedAt"`
}

// toArchivedNode 将软删 Node 映射为归档 DTO。
func toArchivedNode(n *model.Node) ArchivedNode {
	out := ArchivedNode{
		ID: n.ID, UUID: n.UUID, Name: n.Name, Host: n.Host,
		GRPCPort: n.GRPCPort, WSPort: n.WSPort, Status: n.Status,
		OS: n.OS, Arch: n.Arch, CPUCores: n.CPUCores, MemoryMB: n.MemoryMB,
		DiskTotalMB: n.DiskTotalMB, Maintenance: n.Maintenance,
		LastHeartbeat: n.LastHeartbeat, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt,
	}
	if n.DeletedAt.Valid {
		out.DeletedAt = n.DeletedAt.Time
	}
	return out
}

// ListArchived 返回已软删下线节点，按 deleted_at 倒序；空列表非 nil。
func (s *NodeService) ListArchived() ([]ArchivedNode, error) {
	var nodes []model.Node
	if err := s.db.Unscoped().Where("deleted_at IS NOT NULL").
		Order("deleted_at DESC").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询归档节点失败: %w", err)
	}
	out := make([]ArchivedNode, 0, len(nodes))
	for i := range nodes {
		out = append(out, toArchivedNode(&nodes[i]))
	}
	return out, nil
}

// GetName 返回归档节点名称，供确认逻辑与活跃节点统一处理。
func (n ArchivedNode) GetName() string { return n.Name }

// GetArchived 按 ID 取单个归档节点；非归档或不存在 → ErrNodeNotFound。
func (s *NodeService) GetArchived(id uint) (*ArchivedNode, error) {
	var node model.Node
	if err := s.db.Unscoped().Where("id = ? AND deleted_at IS NOT NULL", id).
		First(&node).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询归档节点失败: %w", err)
	}
	a := toArchivedNode(&node)
	return &a, nil
}

// Purge 彻底清理归档节点：硬删库记录；有实例须 force 级联硬删实例平台记录，
// 不清理远端文件。活跃（未软删）节点返回 ErrNodeNotArchived。
func (s *NodeService) Purge(id uint, force bool) (*NodeDeleteResult, error) {
	var node model.Node
	if err := s.db.Unscoped().Where("id = ?", id).First(&node).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	if !node.DeletedAt.Valid {
		return nil, ErrNodeNotArchived
	}

	// 含已软删实例（下线 force 后仍挂 node_id）。
	var instances []model.Instance
	if err := s.db.Unscoped().Where("node_id = ?", id).Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询节点实例失败: %w", err)
	}
	if len(instances) > 0 && !force {
		briefs := make([]NodeInstanceBrief, 0, len(instances))
		for _, inst := range instances {
			briefs = append(briefs, NodeInstanceBrief{ID: inst.ID, Name: inst.Name, Status: inst.Status})
		}
		return nil, &NodeHasInstancesError{Instances: briefs}
	}

	result := &NodeDeleteResult{}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if len(instances) > 0 {
			ids := make([]uint, 0, len(instances))
			for _, inst := range instances {
				ids = append(ids, inst.ID)
			}
			if err := tx.Where("instance_id IN ?", ids).Delete(&model.GroupInstance{}).Error; err != nil {
				return err
			}
			if err := tx.Where("proxy_id IN ? OR backend_id IN ?", ids, ids).Delete(&model.ServerRegistration{}).Error; err != nil {
				return err
			}
			if err := tx.Where("instance_id IN ?", ids).Delete(&model.NetworkMember{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Where("node_id = ?", id).Delete(&model.Instance{}).Error; err != nil {
				return err
			}
			result.InstancesPurged = len(ids)
		}
		return tx.Unscoped().Delete(&model.Node{}, id).Error
	})
	if err != nil {
		return nil, fmt.Errorf("清理归档节点失败: %w", err)
	}
	return result, nil
}

// ScheduleAllowed 判断节点当前是否允许接纳新实例调度。
// 维护模式（cordon）下拒绝；返回的 error 为 ErrNodeInMaintenance 或 ErrNodeNotFound。
// 实例创建/分配选节点前必须经此校验。参见。
func (s *NodeService) ScheduleAllowed(id uint) error {
	node, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if node.Maintenance {
		return ErrNodeInMaintenance
	}
	return nil
}

// CheckOfflineNodes 检测离线节点（超过 90s 无心跳）。
func (s *NodeService) CheckOfflineNodes() {
	threshold := time.Now().Add(-90 * time.Second)
	s.db.Model(&model.Node{}).
		Where("status = ? AND last_heartbeat < ?", model.NodeStatusOnline, &threshold).
		Update("status", model.NodeStatusOffline)
}

func generateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

// StartupProgressView 是「正在启动的节点」的读模型（范围②）。
//
// 为什么不复用 model.Node：启动中的 Worker **还没有注册**，CP 里没有它的节点记录
// （首装时连 UUID 都没有）。若硬塞进 nodes 表，节点列表会凭空出现「存在但从未上线」的节点。
type StartupProgressView struct {
	// Key 进度行主键：node_uuid 或 "name:<名字>"。
	Key string `json:"key"`
	// NodeUUID 已注册节点的 UUID；首装未注册时为空。
	NodeUUID string `json:"nodeUuid,omitempty"`
	// NodeName Worker 自称的节点名。
	NodeName string `json:"nodeName"`
	// Phase 当前阶段标识（config / dataroot / ... / heartbeat）。
	Phase string `json:"phase"`
	// PhaseElapsedMs 该阶段已耗时。
	PhaseElapsedMs int64 `json:"phaseElapsedMs"`
	// TotalElapsedMs 自进程启动起的总耗时。
	TotalElapsedMs int64 `json:"totalElapsedMs"`
	// PhasesDone / PhasesTotal 供前端渲染 n/m。
	PhasesDone  int32 `json:"phasesDone"`
	PhasesTotal int32 `json:"phasesTotal"`
	// UpdatedAt 最近上报时刻。
	UpdatedAt time.Time `json:"updatedAt"`
	// Stale true 表示超时未更新，Worker 可能已崩溃或被杀。
	//
	// 必须由后端算并下发：前端拿 updatedAt 自行比较会依赖客户端时钟，
	// 而运维机器的时钟偏差恰恰是常见现象——那会让「启动中断」判定随机失真。
	Stale bool `json:"stale"`
}

// ListStartupProgress 返回当前所有「正在启动」的节点进度。
//
// 过滤规则：只返回**未陈旧**的进度行。陈旧行不展示——它们代表已经死掉的启动过程，
// 面板上显示「启动中」会误导运维以为还在加载（实际情况往往需要去查进程）。
// 陈旧行本身不在这里删除：清理交给注册路径与后续巡检，读路径保持只读。
func (s *NodeService) ListStartupProgress() ([]StartupProgressView, error) {
	var rows []model.NodeStartupProgress
	if err := s.db.Order("updated_at DESC").Find(&rows).Error; err != nil {
		// 老库可能还没建这张表（迁移未跑）——按「没有正在启动的节点」处理，
		// 不让一个可选可观测性功能把节点列表整体打挂。
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return []StartupProgressView{}, nil
		}
		return nil, fmt.Errorf("查询启动进度失败: %w", err)
	}
	now := time.Now()
	out := make([]StartupProgressView, 0, len(rows))
	for i := range rows {
		r := rows[i]
		stale := r.IsStale(now)
		if stale {
			// 陈旧行不进入「正在启动」列表；由 CleanupStaleStartupProgress 清理。
			continue
		}
		out = append(out, StartupProgressView{
			Key:            r.Key,
			NodeUUID:       r.NodeUUID,
			NodeName:       r.NodeName,
			Phase:          r.Phase,
			PhaseElapsedMs: r.PhaseElapsedMs,
			TotalElapsedMs: r.TotalElapsedMs,
			PhasesDone:     r.PhasesDone,
			PhasesTotal:    r.PhasesTotal,
			UpdatedAt:      r.UpdatedAt,
			Stale:          false,
		})
	}
	return out, nil
}

// CleanupStaleStartupProgress 删除陈旧进度行，返回删除条数。
//
// 由定期巡检调用。之所以要清理而不是永久保留：陈旧行是「启动中途死掉」的残留，
// 永远留着只会让表慢慢变大，且下次同节点启动时会与新进度混在一起。
func (s *NodeService) CleanupStaleStartupProgress() (int64, error) {
	cutoff := time.Now().Add(-5 * time.Minute)
	res := s.db.Where("updated_at < ?", cutoff).Delete(&model.NodeStartupProgress{})
	if res.Error != nil {
		return 0, fmt.Errorf("清理陈旧启动进度失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}
