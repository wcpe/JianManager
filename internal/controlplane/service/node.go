package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

var (
	ErrNodeNotFound = errors.New("节点不存在")
	// ErrNodeInMaintenance 目标节点处于维护模式，拒绝新实例调度。参见 FR-048。
	ErrNodeInMaintenance = errors.New("节点处于维护模式，已拒绝新实例调度")
)

// NodeService 节点管理服务。
type NodeService struct {
	db *gorm.DB
	// instanceSvc 用于排空（drain）时停止节点上的运行实例。
	// 同包内通过 SetInstanceService 注入，规避构造期循环依赖。
	instanceSvc *InstanceService
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
		Name:        req.Name,
		Host:        req.Host,
		GRPCPort:    req.GRPCPort,
		WSPort:      req.WSPort,
		Secret:      secret,
		Status:      model.NodeStatusOnline,
		OS:          req.OS,
		Arch:        req.Arch,
		CPUCores:    req.CPUCores,
		MemoryMB:    req.MemoryMB,
		DiskTotalMB: req.DiskTotalMB,
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
	CPUUsage    float64 `json:"cpuUsage"`
	MemoryUsage float64 `json:"memoryUsage"`
	DiskUsage   float64 `json:"diskUsage"`
	MemoryUsedMB int64  `json:"memoryUsedMb"`
	DiskUsedMB  int64   `json:"diskUsedMb"`
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
	return &node, nil
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
// 也不改变节点在线/离线状态。参见 FR-048。
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
// 参见 FR-048。
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

// Delete 主动下线节点：解除注册并保留记录（软删除），复连需重新注册。
// 安全约束：节点在线时拒绝下线，应先排空并断开 Worker，避免下线一个仍在跑实例的活节点。
// 软删除（gorm.DeletedAt）保留历史审计与实例归属；Worker 复连时 Register 重新建档获得新 UUID/secret。
// 参见 FR-048。
func (s *NodeService) Delete(id uint) error {
	node, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if node.Status == model.NodeStatusOnline {
		return fmt.Errorf("不能删除在线节点")
	}
	return s.db.Delete(&model.Node{}, id).Error
}

// ScheduleAllowed 判断节点当前是否允许接纳新实例调度。
// 维护模式（cordon）下拒绝；返回的 error 为 ErrNodeInMaintenance 或 ErrNodeNotFound。
// 实例创建/分配选节点前必须经此校验。参见 FR-048。
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
