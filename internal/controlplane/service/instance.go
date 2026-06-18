package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
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
	NodeID           uint               `json:"nodeId" binding:"required"`
	Name             string             `json:"name" binding:"required,min=1,max=128"`
	Type             model.InstanceType `json:"type" binding:"required"`
	ProcessType      model.ProcessType  `json:"processType" binding:"required"`
	StartCommand     string             `json:"startCommand" binding:"required"`
	JDKID            uint               `json:"jdkId"`
	JavaMajorVersion int                `json:"javaMajorVersion"`
	LaunchSpec       string             `json:"launchSpec"`
	WorkDir          string             `json:"workDir"`
	EnvVars          map[string]string  `json:"envVars"`
	AutoStart        bool               `json:"autoStart"`
	AutoRestart      bool               `json:"autoRestart"`
	GroupID          uint               `json:"groupId"`
}

// Create 创建实例。
func (s *InstanceService) Create(req CreateInstanceRequest) (*model.Instance, error) {
	req.StartCommand = sanitizeStartCommand(req.StartCommand)

	// 工作目录系统分配（ADR-007/ADR-010）：MC 实例不接受用户手填绝对路径，
	// 由系统在数据根 var/servers 下按 slug+shortid 分配，按相对路径登记保证便携。
	// 其它类型（generic）保留用户传入的 WorkDir。
	workDir := req.WorkDir
	if req.Type == model.InstanceTypeMinecraftJava {
		workDir = allocWorkDirRel(req.Name)
	}

	instance := &model.Instance{
		NodeID:           req.NodeID,
		Name:             req.Name,
		Type:             req.Type,
		ProcessType:      req.ProcessType,
		StartCommand:     req.StartCommand,
		JDKID:            req.JDKID,
		JavaMajorVersion: req.JavaMajorVersion,
		LaunchSpec:       req.LaunchSpec,
		WorkDir:          workDir,
		AutoStart:        req.AutoStart,
		AutoRestart:      req.AutoRestart,
		Status:           model.InstanceStatusStopped,
	}
	if len(req.EnvVars) > 0 {
		raw, _ := json.Marshal(req.EnvVars)
		instance.EnvVars = string(raw)
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// 配额检查：实例数 / Bot 数 / 存储空间
		if req.GroupID > 0 {
			var quota model.GroupQuota
			if err := tx.Where("group_id = ?", req.GroupID).First(&quota).Error; err != nil {
				return fmt.Errorf("查询组配额失败: %w", err)
			}

			// 实例数配额
			var currentCount int64
			tx.Model(&model.GroupInstance{}).Where("group_id = ?", req.GroupID).Count(&currentCount)
			if quota.MaxInstances > 0 && int(currentCount) >= quota.MaxInstances {
				return fmt.Errorf("%w: 实例数 %d/%d", ErrQuotaExceeded, currentCount, quota.MaxInstances)
			}

			// Bot 数配额：组内关联 Bot 已达上限时拒绝新建实例
			// 参见 FR-003 验收：配额检查覆盖 MaxBots。
			if quota.MaxBots > 0 {
				var botCount int64
				tx.Model(&model.Bot{}).
					Joins("JOIN group_instances ON group_instances.instance_id = bots.instance_id").
					Where("group_instances.group_id = ?", req.GroupID).
					Count(&botCount)
				if int(botCount) >= quota.MaxBots {
					return fmt.Errorf("%w: Bot 数 %d/%d", ErrQuotaExceeded, botCount, quota.MaxBots)
				}
			}

			// 存储配额：按组内备份总大小预估，超额拒绝创建。
			// 参见 FR-003 验收：配额检查覆盖 MaxStorageMB。
			// TODO(FR-003): 接入 Worker 工作目录大小上报后替换为更精确的累计。
			if quota.MaxStorageMB > 0 {
				var storageSum struct {
					Total float64
				}
				tx.Model(&model.Backup{}).
					Select("COALESCE(SUM(file_size_mb), 0) as total").
					Joins("JOIN group_instances ON group_instances.instance_id = backups.instance_id").
					Where("group_instances.group_id = ?", req.GroupID).
					Scan(&storageSum)
				if int(storageSum.Total) >= quota.MaxStorageMB {
					return fmt.Errorf("%w: 存储 %d/%d MB", ErrQuotaExceeded, int(storageSum.Total), quota.MaxStorageMB)
				}
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

// ListByGroups 返回指定组集合内的实例列表，用于非平台管理员的权限过滤。
func (s *InstanceService) ListByGroups(nodeID *uint, status *model.InstanceStatus, groupIDs []uint) ([]model.Instance, error) {
	if len(groupIDs) == 0 {
		return []model.Instance{}, nil
	}
	var instances []model.Instance
	q := s.db.Model(&model.Instance{}).
		Joins("JOIN group_instances ON group_instances.instance_id = instances.id").
		Where("group_instances.group_id IN ?", groupIDs)

	if nodeID != nil {
		q = q.Where("instances.node_id = ?", *nodeID)
	}
	if status != nil {
		q = q.Where("instances.status = ?", *status)
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
// jdkId == 0 时表示不变；envVars == nil 时表示不变。
func (s *InstanceService) Update(id uint, name, startCommand *string, autoStart, autoRestart *bool, jdkID *uint, envVars *map[string]string) (*model.Instance, error) {
	instance, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}
	if name != nil {
		updates["name"] = *name
	}
	if startCommand != nil {
		sanitized := sanitizeStartCommand(*startCommand)
		updates["start_command"] = sanitized
	}
	if autoStart != nil {
		updates["auto_start"] = *autoStart
	}
	if autoRestart != nil {
		updates["auto_restart"] = *autoRestart
	}
	if jdkID != nil {
		updates["jdk_id"] = *jdkID
	}
	if envVars != nil {
		raw, _ := json.Marshal(*envVars)
		updates["env_vars"] = string(raw)
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

	// 把存储为 JSON 字符串的 EnvVars 解出来，原样下发给 Worker 注入到进程环境。
	var envVars map[string]string
	if strings.TrimSpace(instance.EnvVars) != "" {
		_ = json.Unmarshal([]byte(instance.EnvVars), &envVars)
	}

	resp, err := client.Worker.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: instance.UUID,
		Name:         instance.Name,
		ProcessType:  string(instance.ProcessType),
		StartCommand: instance.StartCommand,
		WorkDir:      instance.WorkDir,
		EnvVars:      envVars,
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

// sanitizeStartCommand 去除启动命令外层多余的引号包裹。
// 用户从其他来源复制命令时可能带入单引号或双引号包裹，导致 cmd.exe 执行失败。
// 仅当整个命令被同种引号完整包裹时才去除，避免误删路径中的引号。
func sanitizeStartCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if len(cmd) < 2 {
		return cmd
	}
	// 仅当整个字符串被一对引号包裹时才去除
	first, last := cmd[0], cmd[len(cmd)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		inner := strings.TrimSpace(cmd[1 : len(cmd)-1])
		// 内容不包含同类引号 → 说明是多余的外层包裹
		if !strings.ContainsRune(inner, rune(first)) {
			return inner
		}
	}
	return cmd
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
