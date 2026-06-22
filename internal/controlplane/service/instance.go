package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
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
	// settings 提供平台设置生效值（graceful_stop.timeout），随启动下发使优雅停止超时真生效；
	// 为 nil 时不下发，Worker 回退本地 env/默认（FR-063）。
	settings SettingsReader

	// bgCtx/bgCancel 管理后台 Worker 委托 goroutine 的生命周期；bgWG 用于优雅关闭时 join，
	// bgMu 保护「取消」与「登记新委托」之间的竞态（避免 WaitGroup 的 Add-after-Wait）。
	// 委托是 fire-and-forget 的（见 Start/Stop/Restart/Kill），无 join 会在进程/测试退出后
	// 仍向已关闭的依赖写库。参见 Shutdown。
	bgCtx    context.Context
	bgCancel context.CancelFunc
	bgWG     sync.WaitGroup
	bgMu     sync.Mutex
}

// NewInstanceService 创建实例服务。
func NewInstanceService(db *gorm.DB, groupSvc *GroupService, pool *cpgrpc.ClientPool) *InstanceService {
	ctx, cancel := context.WithCancel(context.Background())
	return &InstanceService{db: db, groupSvc: groupSvc, pool: pool, bgCtx: ctx, bgCancel: cancel}
}

// SetSettingsReader 注入平台设置读取器（FR-063）。在 main 装配阶段调用，避免构造期循环依赖。
func (s *InstanceService) SetSettingsReader(r SettingsReader) {
	s.settings = r
}

// gracefulStopTimeoutSeconds 取优雅停止超时（秒）的生效值（平台设置 graceful_stop.timeout）。
// 设置以 Go duration 文本存储（如 "30s"）；解析失败或未注入设置时返回 0，由 Worker 回退默认。
// 语义：仅随「启动」下发，故设置变更对其后新启动的实例生效，已运行实例保留启动时的值。
func (s *InstanceService) gracefulStopTimeoutSeconds() int32 {
	if s.settings == nil {
		return 0
	}
	d, err := time.ParseDuration(s.settings.EffectiveValue(SettingKeyGracefulStopTimeout))
	if err != nil || d <= 0 {
		return 0
	}
	return int32(d.Seconds())
}

// CreateInstanceRequest 创建实例请求。
type CreateInstanceRequest struct {
	NodeID           uint               `json:"nodeId" binding:"required"`
	Name             string             `json:"name" binding:"required,min=1,max=128"`
	Type             model.InstanceType `json:"type" binding:"required"`
	Role             model.InstanceRole `json:"role"`
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
	ServerPort       int                `json:"serverPort"`
	RCONPort         int                `json:"rconPort"`
	QueryPort        int                `json:"queryPort"`
	ProbePort        int                `json:"probePort"`
	RCONPassword     string             `json:"-"`
}

// Create 创建实例。
func (s *InstanceService) Create(req CreateInstanceRequest) (*model.Instance, error) {
	// 调度拦截（FR-048）：维护模式（cordon）节点拒绝接纳新实例。
	// 直接查目标节点的维护标记，避免 InstanceService 反向依赖 NodeService。
	// 节点不存在时不在此处硬失败（沿用既有创建行为，注册/启动阶段另有校验），
	// 仅当节点存在且处于维护模式时拒绝。
	var target model.Node
	if err := s.db.First(&target, req.NodeID).Error; err == nil {
		if target.Maintenance {
			return nil, ErrNodeInMaintenance
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("查询目标节点失败: %w", err)
	}

	req.StartCommand = sanitizeStartCommand(req.StartCommand)

	// MC 结构化启动（ADR-008）：提供 launchSpec 时由其派生 java 启动命令，
	// 取代自由文本 start_command；启动时由 Worker 注入绑定 JDK 的 JAVA_HOME/PATH。
	if spec, err := parseLaunchSpec(req.LaunchSpec); err != nil {
		return nil, err
	} else if spec != nil {
		derived, derr := deriveStartCommand(spec)
		if derr != nil {
			return nil, derr
		}
		req.StartCommand = derived
	}

	// 工作目录系统分配（ADR-007/ADR-010）：MC 实例不接受用户手填绝对路径，
	// 由系统在数据根 var/servers 下按 slug+shortid 分配，按相对路径登记保证便携。
	// 其它类型（generic）保留用户传入的 WorkDir。
	workDir := req.WorkDir
	if req.Type == model.InstanceTypeMinecraftJava {
		workDir = allocWorkDirRel(req.Name)
	}

	// 角色化（ADR-007）：未指定或非法时落 universal（grandfather 既有创建路径）。
	role := req.Role
	if !model.ValidInstanceRole(role) {
		role = model.InstanceRoleUniversal
	}

	instance := &model.Instance{
		NodeID:           req.NodeID,
		Name:             req.Name,
		Type:             req.Type,
		Role:             role,
		ProcessType:      req.ProcessType,
		StartCommand:     req.StartCommand,
		JDKID:            req.JDKID,
		JavaMajorVersion: req.JavaMajorVersion,
		LaunchSpec:       req.LaunchSpec,
		WorkDir:          workDir,
		AutoStart:        req.AutoStart,
		AutoRestart:      req.AutoRestart,
		ServerPort:       req.ServerPort,
		RCONPort:         req.RCONPort,
		QueryPort:        req.QueryPort,
		ProbePort:        req.ProbePort,
		RCONPassword:     req.RCONPassword,
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

// InstanceFilter 聚合实例列表的多维筛选条件（FR-047）。
// 各字段为零值（nil / 空串）时表示该维度不参与过滤；多维之间为 AND 组合。
// 群组(NetworkID)、节点/状态/角色/组用 DB 侧过滤；环境(Env)/标签(Tag)因 Tags 以
// JSON 字符串存储，DB 侧用 LIKE 粗筛，最终由应用层精确校验（避免子串误命中）。
type InstanceFilter struct {
	NodeID    *uint
	Status    *model.InstanceStatus
	GroupID   *uint
	Role      *model.InstanceRole
	NetworkID *uint
	Env       string
	Tag       string
}

// applyDBFilters 把可下推到 DB 的筛选条件附加到查询上。
// 表名前缀统一用 instances.，兼容携带 JOIN 的查询。
func applyDBFilters(q *gorm.DB, f InstanceFilter) *gorm.DB {
	if f.NodeID != nil {
		q = q.Where("instances.node_id = ?", *f.NodeID)
	}
	if f.Status != nil {
		q = q.Where("instances.status = ?", *f.Status)
	}
	if f.Role != nil {
		q = q.Where("instances.role = ?", *f.Role)
	}
	if f.NetworkID != nil {
		// 群组是 M:N 软标签（ADR-007）：经 network_members 关联过滤。
		q = q.Joins("JOIN network_members ON network_members.instance_id = instances.id").
			Where("network_members.network_id = ?", *f.NetworkID)
	}
	// 环境/标签：DB 侧用 LIKE 缩小候选集，精确判定交给应用层（filterByTags）。
	if env := strings.TrimSpace(f.Env); env != "" {
		q = q.Where("instances.tags LIKE ?", "%"+model.EnvTagPrefix+env+"%")
	}
	if tag := strings.TrimSpace(f.Tag); tag != "" {
		q = q.Where("instances.tags LIKE ?", "%"+tag+"%")
	}
	return q
}

// filterByTags 对 DB 粗筛后的实例做环境/标签精确过滤。
// DB LIKE 仅缩小范围，可能误命中子串（如标签 `production` 命中 env 过滤 `prod`），
// 故按解析后的标签集合精确判定。Env/Tag 均空时原样返回。
func filterByTags(instances []model.Instance, env, tag string) []model.Instance {
	if strings.TrimSpace(env) == "" && strings.TrimSpace(tag) == "" {
		return instances
	}
	out := make([]model.Instance, 0, len(instances))
	for _, inst := range instances {
		tags := model.ParseTags(inst.Tags)
		if model.MatchEnv(tags, env) && model.MatchTag(tags, tag) {
			out = append(out, inst)
		}
	}
	return out
}

// List 返回实例列表，支持按节点/状态/组/角色/群组/环境/标签多维组合过滤（FR-047）。
func (s *InstanceService) List(f InstanceFilter) ([]model.Instance, error) {
	var instances []model.Instance
	q := applyDBFilters(s.db.Model(&model.Instance{}), f)
	if f.GroupID != nil {
		q = q.Joins("JOIN group_instances ON group_instances.instance_id = instances.id").
			Where("group_instances.group_id = ?", *f.GroupID)
	}

	if err := q.Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询实例列表失败: %w", err)
	}
	return filterByTags(instances, f.Env, f.Tag), nil
}

// ListByGroups 返回指定组集合内的实例列表，用于非平台管理员的权限过滤。
// 在权限组约束之上叠加 InstanceFilter 的多维筛选（FR-047）。
func (s *InstanceService) ListByGroups(groupIDs []uint, f InstanceFilter) ([]model.Instance, error) {
	if len(groupIDs) == 0 {
		return []model.Instance{}, nil
	}
	var instances []model.Instance
	q := s.db.Model(&model.Instance{}).
		Joins("JOIN group_instances ON group_instances.instance_id = instances.id").
		Where("group_instances.group_id IN ?", groupIDs)
	q = applyDBFilters(q, f)

	if err := q.Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询实例列表失败: %w", err)
	}
	return filterByTags(instances, f.Env, f.Tag), nil
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

// UpdateInstanceFields 实例可更新字段（nil 表示不变）。
// tags 用于环境/标签多维分组（FR-047）：写入前规范化（去空/去重/保序），
// 环境维度复用 `env:` 前缀标签，不单独建字段。
type UpdateInstanceFields struct {
	Name         *string
	StartCommand *string
	AutoStart    *bool
	AutoRestart  *bool
	JDKID        *uint
	EnvVars      *map[string]string
	Tags         *[]string
}

// Update 更新实例配置。各字段为 nil 时表示不变。
func (s *InstanceService) Update(id uint, f UpdateInstanceFields) (*model.Instance, error) {
	instance, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}

	updates := map[string]interface{}{}
	if f.Name != nil {
		updates["name"] = *f.Name
	}
	if f.StartCommand != nil {
		sanitized := sanitizeStartCommand(*f.StartCommand)
		updates["start_command"] = sanitized
	}
	if f.AutoStart != nil {
		updates["auto_start"] = *f.AutoStart
	}
	if f.AutoRestart != nil {
		updates["auto_restart"] = *f.AutoRestart
	}
	if f.JDKID != nil {
		updates["jdk_id"] = *f.JDKID
	}
	if f.EnvVars != nil {
		raw, _ := json.Marshal(*f.EnvVars)
		updates["env_vars"] = string(raw)
	}
	if f.Tags != nil {
		// 规范化后持久化为 JSON；空集合落 "null"，ParseTags 读回为空，等价清空标签。
		raw, _ := json.Marshal(model.NormalizeTags(*f.Tags))
		updates["tags"] = string(raw)
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
		// 级联删除群组服关系（ADR-007）：作为代理或后端的注册记录、群组成员关系。
		tx.Where("proxy_id = ? OR backend_id = ?", id, id).Delete(&model.ServerRegistration{})
		tx.Where("instance_id = ?", id).Delete(&model.NetworkMember{})
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
	s.spawnDelegate(instance, "start")

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

	s.spawnDelegate(instance, "stop")

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

	s.spawnDelegate(instance, "restart")

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

	s.spawnDelegate(instance, "kill")

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

	// 解析实例绑定的 JDK 安装路径下发给 Worker：Worker 启动时据此注入 JAVA_HOME 并把
	// <jdk>/bin 接入 PATH（ADR-008 / FR-033），结构化启动命令里的 `java` 即指向它。
	jdkPath, err := s.resolveJDKPath(instance)
	if err != nil {
		return err
	}

	resp, err := client.Worker.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid:               instance.UUID,
		Name:                       instance.Name,
		ProcessType:                string(instance.ProcessType),
		StartCommand:               instance.StartCommand,
		StopCommand:                gracefulStopCommand(instance.Role),
		WorkDir:                    instance.WorkDir,
		EnvVars:                    envVars,
		AutoRestart:                instance.AutoRestart,
		JdkPath:                    jdkPath,
		ProbePort:                  int32(instance.ProbePort),
		GracefulStopTimeoutSeconds: s.gracefulStopTimeoutSeconds(),
	})
	if err != nil {
		return fmt.Errorf("Worker CreateInstance 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker CreateInstance 失败: %s", resp.Error)
	}
	return nil
}

// gracefulStopCommand 按实例角色派生优雅停止命令（daemon 模式写入进程 stdin）。
// 代理（BungeeCord/Waterfall/Velocity）控制台用 `end`，不认 MC 的 `stop`；若误发 `stop`
// 代理不退出，会一直挂到超时强杀，期间旧进程仍占监听端口，重启时端口冲突崩溃（FR-035）。
// 后端/通用实例沿用 MC 的 `stop`。
func gracefulStopCommand(role model.InstanceRole) string {
	if role == model.InstanceRoleProxy {
		return "end"
	}
	return "stop"
}

// EnsureRegistered 确保实例已在其 Worker 注册（幂等：已存在视为成功）。
// 供克隆等需要源/目标实例在册的流程复用（STOPPED 实例在 Worker 重启后可能不在管理器中）。
func (s *InstanceService) EnsureRegistered(inst *model.Instance) error {
	err := s.registerOnWorker(inst)
	if err != nil && strings.Contains(err.Error(), "已存在") {
		return nil
	}
	return err
}

// resolveJDKPath 解析实例绑定的 JDK 在节点上的安装路径，下发给 Worker 作 JAVA_HOME。
// 优先按 JDKID 精确匹配；未绑定但指定了 Java 大版本时，回退到本节点该大版本的 JDK；
// 都没有则返回空字符串（generic/universal 实例无需注入 JDK）。
func (s *InstanceService) resolveJDKPath(instance *model.Instance) (string, error) {
	if instance.JDKID > 0 {
		var jdk model.NodeJDK
		if err := s.db.First(&jdk, instance.JDKID).Error; err != nil {
			return "", fmt.Errorf("绑定的 JDK(id=%d) 不存在: %w", instance.JDKID, err)
		}
		return jdk.Path, nil
	}
	if instance.JavaMajorVersion > 0 {
		var jdk model.NodeJDK
		err := s.db.Where("node_id = ? AND major_version = ?", instance.NodeID, instance.JavaMajorVersion).First(&jdk).Error
		if err == nil {
			return jdk.Path, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("查询 JDK 失败: %w", err)
		}
	}
	return "", nil
}

// spawnDelegate 在后台异步委托实例动作给 Worker，并登记到 bgWG 以便优雅关闭时 join。
// Shutdown 之后（bgCtx 取消）不再发起新委托，避免向已关闭的依赖（如测试退出后关闭的 DB）写库。
func (s *InstanceService) spawnDelegate(instance *model.Instance, action string) {
	s.bgMu.Lock()
	if s.bgCtx.Err() != nil {
		s.bgMu.Unlock()
		return
	}
	s.bgWG.Add(1)
	s.bgMu.Unlock()

	go func() {
		defer s.bgWG.Done()
		s.delegateToWorker(instance, action)
	}()
}

// Shutdown 停止接受新的后台 Worker 委托并等待在途委托完成。
// 用途：① 进程优雅关闭时确保异步状态回写收尾、不泄漏 goroutine；
// ② 无 Worker 连接的测试中于装配后立即调用以禁用异步委托——否则委托因节点不可达
// 把状态异步覆盖为 CRASHED，并可能在用例结束关闭 DB 后仍写库，引入竞态。
func (s *InstanceService) Shutdown() {
	s.bgMu.Lock()
	s.bgCancel()
	s.bgMu.Unlock()
	s.bgWG.Wait()
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
	// 以下为 ServerProbe 富指标（FR-010）；探针不可用时为零值，ProbeAvailable=false。
	MSPTMillis     float32       `json:"msptMillis"`
	Threads        int32         `json:"threads"`
	CPUPercent     float64       `json:"cpuPercent"`
	HeapMaxMB      int64         `json:"heapMaxMb"`
	UptimeSeconds  float64       `json:"uptimeSeconds"`
	Worlds         []WorldMetric `json:"worlds"`
	ProbeAvailable bool          `json:"probeAvailable"`
}

// WorldMetric 单个世界的负载（来自 ServerProbe），供前端 FR-010 监控页展示。
type WorldMetric struct {
	Name         string `json:"name"`
	LoadedChunks int64  `json:"loadedChunks"`
	Entities     int64  `json:"entities"`
	TileEntities int64  `json:"tileEntities"`
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
		ProbePort:    int32(instance.ProbePort),
		RconPort:     int32(instance.RCONPort),
		RconPassword: instance.RCONPassword,
	})
	if err != nil {
		return nil, fmt.Errorf("获取指标失败: %w", err)
	}

	data := &MetricsData{
		TPS:            resp.Tps,
		OnlinePlayers:  resp.OnlinePlayers,
		MemoryMB:       resp.MemoryMb,
		MSPTMillis:     resp.MsptMillis,
		Threads:        resp.Threads,
		CPUPercent:     resp.CpuPercent,
		HeapMaxMB:      resp.HeapMaxMb,
		UptimeSeconds:  resp.UptimeSeconds,
		ProbeAvailable: resp.ProbeAvailable,
	}
	for _, w := range resp.Worlds {
		data.Worlds = append(data.Worlds, WorldMetric{
			Name:         w.Name,
			LoadedChunks: w.LoadedChunks,
			Entities:     w.Entities,
			TileEntities: w.TileEntities,
		})
	}
	return data, nil
}
