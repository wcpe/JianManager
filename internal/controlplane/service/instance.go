package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/proto/workerpb"
)

var (
	ErrInstanceNotFound  = errors.New("实例不存在")
	ErrInvalidTransition = errors.New("无效的状态转换")
	ErrInstanceRunning   = errors.New("实例正在运行，需先停止")
	ErrInstanceStopped   = errors.New("实例已停止")
	ErrQuotaExceeded     = errors.New("组配额已满")
)

// validTransitions 合法的状态转换。
var validTransitions = map[model.InstanceStatus][]model.InstanceStatus{
	model.InstanceStatusStopped:  {model.InstanceStatusStarting},
	model.InstanceStatusStarting: {model.InstanceStatusRunning, model.InstanceStatusCrashed},
	model.InstanceStatusRunning:  {model.InstanceStatusStopping, model.InstanceStatusCrashed},
	model.InstanceStatusStopping: {model.InstanceStatusStopped, model.InstanceStatusCrashed},
	model.InstanceStatusCrashed:  {model.InstanceStatusStarting},
}

// InstanceService 实例管理服务。
type InstanceService struct {
	db       *gorm.DB
	groupSvc *GroupService
	pool     *cpgrpc.ClientPool
}

// NewInstanceService 创建实例服务。
func NewInstanceService(db *gorm.DB, groupSvc *GroupService, pool *cpgrpc.ClientPool) *InstanceService {
	return &InstanceService{db: db, groupSvc: groupSvc, pool: pool}
}

// CreateInstanceRequest 创建实例请求。
type CreateInstanceRequest struct {
	NodeID       uint              `json:"nodeId" binding:"required"`
	Name         string            `json:"name" binding:"required,min=1,max=128"`
	Type         model.InstanceType `json:"type" binding:"required"`
	ProcessType  model.ProcessType  `json:"processType" binding:"required"`
	StartCommand string            `json:"startCommand" binding:"required"`
	WorkDir      string            `json:"workDir"`
	AutoStart    bool              `json:"autoStart"`
	AutoRestart  bool              `json:"autoRestart"`
	GroupID      uint              `json:"groupId"`
}

// Create 创建实例。
func (s *InstanceService) Create(req CreateInstanceRequest) (*model.Instance, error) {
	instance := &model.Instance{
		NodeID:       req.NodeID,
		Name:         req.Name,
		Type:         req.Type,
		ProcessType:  req.ProcessType,
		StartCommand: req.StartCommand,
		WorkDir:      req.WorkDir,
		AutoStart:    req.AutoStart,
		AutoRestart:  req.AutoRestart,
		Status:       model.InstanceStatusStopped,
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 配额检查
		if req.GroupID > 0 {
			var quota model.GroupQuota
			if err := tx.Where("group_id = ?", req.GroupID).First(&quota).Error; err != nil {
				return fmt.Errorf("查询组配额失败: %w", err)
			}

			var currentCount int64
			tx.Model(&model.GroupInstance{}).Where("group_id = ?", req.GroupID).Count(&currentCount)
			if int(currentCount) >= quota.MaxInstances {
				return fmt.Errorf("%w: 当前 %d/%d", ErrQuotaExceeded, currentCount, quota.MaxInstances)
			}
		}

		if err := tx.Create(instance).Error; err != nil {
			return fmt.Errorf("创建实例失败: %w", err)
		}

		// 分配给用户组
		if req.GroupID > 0 {
			gi := &model.GroupInstance{
				GroupID:    req.GroupID,
				InstanceID: instance.ID,
			}
			if err := tx.Create(gi).Error; err != nil {
				return fmt.Errorf("分配实例到组失败: %w", err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// 同步注册实例到 Worker Node
	if err := s.registerOnWorker(instance); err != nil {
		slog.Warn("实例已创建但未注册到 Worker，启动时将重试", "instanceId", instance.UUID, "error", err)
	}

	return instance, nil
}

// List 返回实例列表，支持按节点、状态、组过滤。
func (s *InstanceService) List(nodeID *uint, status *model.InstanceStatus, groupID *uint) ([]model.Instance, error) {
	var instances []model.Instance
	q := s.db.Model(&model.Instance{})

	if nodeID != nil {
		q = q.Where("node_id = ?", *nodeID)
	}
	if status != nil {
		q = q.Where("status = ?", *status)
	}
	if groupID != nil {
		q = q.Joins("JOIN group_instances ON group_instances.instance_id = instances.id").
			Where("group_instances.group_id = ?", *groupID)
	}

	if err := q.Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询实例列表失败: %w", err)
	}
	return instances, nil
}

// GetByID 按 ID 获取实例。
func (s *InstanceService) GetByID(id uint) (*model.Instance, error) {
	var instance model.Instance
	if err := s.db.First(&instance, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}
	return &instance, nil
}

// Update 更新实例配置。
func (s *InstanceService) Update(id uint, name, startCommand *string, autoStart, autoRestart *bool) (*model.Instance, error) {
	instance, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}
	if name != nil {
		updates["name"] = *name
	}
	if startCommand != nil {
		updates["start_command"] = *startCommand
	}
	if autoStart != nil {
		updates["auto_start"] = *autoStart
	}
	if autoRestart != nil {
		updates["auto_restart"] = *autoRestart
	}

	if len(updates) > 0 {
		if err := s.db.Model(instance).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("更新实例失败: %w", err)
		}
	}

	return s.GetByID(id)
}

// Delete 删除实例（需先停止）。
func (s *InstanceService) Delete(id uint) error {
	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}
	// 只允许删除已停止或已崩溃的实例
	if instance.Status != model.InstanceStatusStopped && instance.Status != model.InstanceStatusCrashed {
		return ErrInstanceRunning
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		// 删除组关联
		tx.Where("instance_id = ?", id).Delete(&model.GroupInstance{})
		// 删除实例
		return tx.Delete(&model.Instance{}, id).Error
	})
}

// Start 启动实例（委托给 Worker Node）。
func (s *InstanceService) Start(id uint) error {
	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}

	// 状态转换
	if err := s.transition(id, model.InstanceStatusStarting, "启动"); err != nil {
		return err
	}

	// 委托给 Worker Node
	go s.delegateToWorker(instance, "start")

	return nil
}

// Stop 停止实例（委托给 Worker Node）。
func (s *InstanceService) Stop(id uint) error {
	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}

	if err := s.transition(id, model.InstanceStatusStopping, "停止"); err != nil {
		return err
	}

	go s.delegateToWorker(instance, "stop")

	return nil
}

// Restart 重启实例。
func (s *InstanceService) Restart(id uint) error {
	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}

	if err := s.transition(id, model.InstanceStatusStopping, "重启-停止"); err != nil {
		return err
	}

	go s.delegateToWorker(instance, "restart")

	return nil
}

// Kill 强制终止实例。
func (s *InstanceService) Kill(id uint) error {
	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}

	if err := s.transition(id, model.InstanceStatusStopped, "强制终止"); err != nil {
		return err
	}

	go s.delegateToWorker(instance, "kill")

	return nil
}

// registerOnWorker 将实例注册到 Worker Node 的进程管理器。
func (s *InstanceService) registerOnWorker(instance *model.Instance) error {
	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		return fmt.Errorf("查找节点失败: %w", err)
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return fmt.Errorf("节点 %s 未连接", node.UUID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: instance.UUID,
		Name:         instance.Name,
		StartCommand: instance.StartCommand,
		WorkDir:      instance.WorkDir,
		AutoRestart:  instance.AutoRestart,
	})
	if err != nil {
		return fmt.Errorf("Worker CreateInstance 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker CreateInstance 失败: %s", resp.Error)
	}
	return nil
}

// delegateToWorker 委托实例操作给 Worker Node。
func (s *InstanceService) delegateToWorker(instance *model.Instance, action string) {
	// 查找节点
	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		slog.Error("查找节点失败", "instanceId", instance.UUID, "error", err)
		s.updateStatusAsync(instance.ID, model.InstanceStatusCrashed)
		return
	}

	// 获取 gRPC 客户端
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		slog.Error("节点未连接", "nodeUUID", node.UUID)
		s.updateStatusAsync(instance.ID, model.InstanceStatusCrashed)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := &workerpb.InstanceActionRequest{
		InstanceUuid: instance.UUID,
	}

	var err error
	var resp *workerpb.InstanceActionResponse
	switch action {
	case "start":
		// 确保实例已注册到 Worker（Create 时可能 Worker 离线）
		if regErr := s.registerOnWorker(instance); regErr != nil {
			slog.Debug("实例已在 Worker 注册或注册失败", "instanceId", instance.UUID, "error", regErr)
		}
		resp, err = client.Worker.StartInstance(ctx, req)
	case "stop":
		resp, err = client.Worker.StopInstance(ctx, req)
	case "restart":
		resp, err = client.Worker.RestartInstance(ctx, req)
	case "kill":
		resp, err = client.Worker.KillInstance(ctx, req)
	}

	if err != nil {
		slog.Error("Worker 操作失败", "action", action, "instanceId", instance.UUID, "error", err)
		s.updateStatusAsync(instance.ID, model.InstanceStatusCrashed)
		return
	}

	if resp != nil && !resp.Success {
		slog.Error("Worker 操作未成功", "action", action, "instanceId", instance.UUID, "error", resp.Error)
		s.updateStatusAsync(instance.ID, model.InstanceStatusCrashed)
		return
	}

	// 操作成功，更新状态
	var targetStatus model.InstanceStatus
	switch action {
	case "start":
		targetStatus = model.InstanceStatusRunning
	case "stop", "kill":
		targetStatus = model.InstanceStatusStopped
	case "restart":
		targetStatus = model.InstanceStatusRunning
	}

	s.updateStatusAsync(instance.ID, targetStatus)
	slog.Info("Worker 操作成功", "action", action, "instanceId", instance.UUID)
}

func (s *InstanceService) updateStatusAsync(id uint, status model.InstanceStatus) {
	if err := s.UpdateStatus(id, status); err != nil {
		slog.Error("更新实例状态失败", "instanceId", id, "status", status, "error", err)
	}
}

// transition 执行状态转换。
func (s *InstanceService) transition(id uint, target model.InstanceStatus, action string) error {
	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}

	allowed := validTransitions[instance.Status]
	valid := false
	for _, s := range allowed {
		if s == target {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("%s: 当前状态 %s 无法转换到 %s: %w", action, instance.Status, target, ErrInvalidTransition)
	}

	if err := s.db.Model(instance).Update("status", target).Error; err != nil {
		return fmt.Errorf("%s失败: %w", action, err)
	}

	slog.Info("实例状态变更", "instanceId", instance.UUID, "from", instance.Status, "to", target, "action", action)
	return nil
}

// UpdateStatus 直接更新实例状态（供 Worker 回调使用）。
func (s *InstanceService) UpdateStatus(id uint, status model.InstanceStatus) error {
	return s.db.Model(&model.Instance{}).Where("id = ?", id).Update("status", status).Error
}

// MetricsData 实例指标数据。
type MetricsData struct {
	TPS           float32 `json:"tps"`
	OnlinePlayers int32   `json:"onlinePlayers"`
	MemoryMB      int64   `json:"memoryMb"`
}

// GetMetrics 通过 gRPC 从 Worker 获取实例指标。
func (s *InstanceService) GetMetrics(id uint) (*MetricsData, error) {
	instance, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}

	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		return nil, fmt.Errorf("查找节点失败: %w", err)
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, fmt.Errorf("节点 %s 未连接", node.UUID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.GetInstanceMetrics(ctx, &workerpb.GetInstanceMetricsRequest{
		InstanceUuid: instance.UUID,
	})
	if err != nil {
		return nil, fmt.Errorf("获取指标失败: %w", err)
	}

	return &MetricsData{
		TPS:           resp.Tps,
		OnlinePlayers: resp.OnlinePlayers,
		MemoryMB:      resp.MemoryMb,
	}, nil
}
