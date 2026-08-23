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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/memguard"
	"github.com/wcpe/JianManager/proto/workerpb"
)

var (
	ErrInstanceNotFound   = errors.New("实例不存在")
	ErrInvalidTransition  = errors.New("无效的状态转换")
	ErrInstanceRunning    = errors.New("实例正在运行，需先停止")
	ErrInstanceStopped    = errors.New("实例已停止")
	ErrInstanceNotRunning = errors.New("实例未运行")
	ErrQuotaExceeded      = errors.New("组配额已满")
	// ErrStartCommandRequired 非 docker 实例缺启动命令（docker 可空，交镜像 entrypoint 自管启动，FR-078）。
	ErrStartCommandRequired = errors.New("非 docker 实例必须提供启动命令")
)

// PreflightError 启动预检未通过错误（FR-314）：携带面向用户的原因，供 HTTP 层映射 422 PREFLIGHT_FAILED。
type PreflightError struct {
	Reason string
}

func (e *PreflightError) Error() string { return e.Reason }

// validTransitions 合法的状态转换。
var validTransitions = map[model.InstanceStatus][]model.InstanceStatus{
	model.InstanceStatusStopped: {model.InstanceStatusStarting},
	// STARTING 允许直接 STOPPING：用户常需中止「卡在启动中」的实例（docker 拉镜像/MC 建世界慢启动尤甚）。
	// 缺此转换时「停止」被状态机拦下、不下发 Worker，容器继续跑、终端不停（修 #5）。
	model.InstanceStatusStarting: {model.InstanceStatusRunning, model.InstanceStatusStopping, model.InstanceStatusCrashed},
	model.InstanceStatusRunning:  {model.InstanceStatusStopping, model.InstanceStatusCrashed},
	model.InstanceStatusStopping: {model.InstanceStatusStopped, model.InstanceStatusCrashed},
	model.InstanceStatusCrashed:  {model.InstanceStatusStarting},
}

type instanceOperationLock struct {
	mu   sync.Mutex
	refs int
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

	// operationLocks 将同实例生命周期请求与其异步 Worker 委托串行到同一临界区，
	// 防止删除越过在途启动后，旧实例指针又在 Worker 重建注册并启动孤儿进程。
	operationLocksMu sync.Mutex
	operationLocks   map[uint]*instanceOperationLock
}

// NewInstanceService 创建实例服务。
func NewInstanceService(db *gorm.DB, groupSvc *GroupService, pool *cpgrpc.ClientPool) *InstanceService {
	ctx, cancel := context.WithCancel(context.Background())
	return &InstanceService{
		db:             db,
		groupSvc:       groupSvc,
		pool:           pool,
		bgCtx:          ctx,
		bgCancel:       cancel,
		operationLocks: make(map[uint]*instanceOperationLock),
	}
}

// acquireInstanceOperation 获取单实例生命周期锁；返回函数必须且只能调用一次。
// 引用计数让无等待者的锁及时移除，避免实例反复创建删除后长期积累锁对象。
func (s *InstanceService) acquireInstanceOperation(id uint) func() {
	s.operationLocksMu.Lock()
	lock := s.operationLocks[id]
	if lock == nil {
		lock = &instanceOperationLock{}
		s.operationLocks[id] = lock
	}
	lock.refs++
	s.operationLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.operationLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.operationLocks, id)
		}
		s.operationLocksMu.Unlock()
	}
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

func normalizeCPULimit(v float64) float64 {
	if v <= 0 {
		return 0
	}
	return v
}

func normalizeResourceLimitMB(v int64) int64 {
	if v <= 0 {
		return 0
	}
	return v
}

// CreateInstanceRequest 创建实例请求。
type CreateInstanceRequest struct {
	NodeID           uint               `json:"nodeId" binding:"required"`
	Name             string             `json:"name" binding:"required,min=1,max=128"`
	Type             model.InstanceType `json:"type" binding:"required"`
	Role             model.InstanceRole `json:"role"`
	ProcessType      model.ProcessType  `json:"processType" binding:"required"`
	StartCommand     string             `json:"startCommand"`
	JDKID            uint               `json:"jdkId"`
	JavaMajorVersion int                `json:"javaMajorVersion"`
	LaunchSpec       string             `json:"launchSpec"`
	WorkDir          string             `json:"workDir"`
	EnvVars          map[string]string  `json:"envVars"`
	// Image 是 docker 模式的容器镜像引用；仅 processType=docker 时使用（FR-078，ADR-019）。
	Image string `json:"image"`
	// CPULimit/MemLimitMB/DiskLimitMB 是 docker 模式资源限额（FR-079，ADR-019）；仅 processType=docker 使用，0=不限制。
	CPULimit    float64 `json:"cpuLimit"`
	MemLimitMB  int64   `json:"memLimitMb"`
	DiskLimitMB int64   `json:"diskLimitMb"`
	AutoStart   bool    `json:"autoStart"`
	AutoRestart bool    `json:"autoRestart"`
	GroupID     uint    `json:"groupId"`
	ServerPort  int     `json:"serverPort"`
	QueryPort   int     `json:"queryPort"`
	ProbePort   int     `json:"probePort"`

	// importWorkDir / importInPlace 仅供包内导入流程（FR-302，见 ADR-069）预置工作目录：
	// 就地=原目录绝对路径、搬迁=已搬迁的系统分配相对路径，绕过下方系统分配。
	// 未导出故 JSON 绑定不可注入——外部 API 依旧无法手填任意工作目录（ADR-007 原则不破）。
	importWorkDir string
	importInPlace bool
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

	// 非 docker 实例必须有启动命令；docker 实例可空——交镜像 entrypoint 自管启动（FR-078，ADR-019）。
	if strings.TrimSpace(req.StartCommand) == "" && req.ProcessType != model.ProcessTypeDocker {
		return nil, ErrStartCommandRequired
	}

	req.CPULimit = normalizeCPULimit(req.CPULimit)
	req.MemLimitMB = normalizeResourceLimitMB(req.MemLimitMB)
	req.DiskLimitMB = normalizeResourceLimitMB(req.DiskLimitMB)

	// 工作目录系统分配（ADR-007/ADR-010）：MC 实例始终系统分配（忽略用户手填）；
	// 其它类型（generic）调用方未传则同样系统分配（FR-234 创建向导隐藏工作目录、统一自动生成），
	// 显式传入则保留（API 调用方仍可指定）。一律落数据根 var/servers 下相对路径保证便携。
	// 导入流程（FR-302）经包内未导出字段预置工作目录，是系统分配的唯一例外（ADR-069）。
	workDir := req.WorkDir
	switch {
	case req.importWorkDir != "":
		workDir = req.importWorkDir
	case req.Type == model.InstanceTypeMinecraftJava || workDir == "":
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
		WorkDirInPlace:   req.importInPlace,
		Image:            req.Image,
		CPULimit:         req.CPULimit,
		MemLimitMB:       req.MemLimitMB,
		DiskLimitMB:      req.DiskLimitMB,
		AutoStart:        req.AutoStart,
		AutoRestart:      req.AutoRestart,
		ServerPort:       req.ServerPort,
		QueryPort:        req.QueryPort,
		ProbePort:        req.ProbePort,
		Status:           model.InstanceStatusStopped,
	}
	if len(req.EnvVars) > 0 {
		raw, err := json.Marshal(req.EnvVars)
		if err != nil {
			return nil, fmt.Errorf("序列化实例环境变量失败: %w", err)
		}
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

// ── FR-247 实例规模化：服务端分页搜索 + 维度聚合 ─────────────────────────────

const (
	// defaultInstancePageSize 分页默认每页条数；maxInstancePageSize 上限（约束响应体大小）。
	defaultInstancePageSize = 50
	maxInstancePageSize     = 200
)

// instanceSortColumns 把排序参数白名单映射到列名（防 SQL 注入；非法值回退 name）。
var instanceSortColumns = map[string]string{
	"name":      "instances.name",
	"status":    "instances.status",
	"createdAt": "instances.created_at",
	"nodeId":    "instances.node_id",
}

// InstanceSearchParams 分页搜索参数（FR-247）。嵌 InstanceFilter 复用多维筛选，
// 另加自由文本 Query（名称子串）、Sort/Order、Page/PageSize。
type InstanceSearchParams struct {
	InstanceFilter
	Query    string
	Sort     string
	Order    string
	Page     int
	PageSize int
}

// Normalize 归一化分页/排序边界：Page≥1、PageSize∈[1,max]（默认 50）、Sort 白名单、Order∈{asc,desc}。
// 幂等，handler 与 service 各调一次均安全。
func (p *InstanceSearchParams) Normalize() {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize <= 0 {
		p.PageSize = defaultInstancePageSize
	}
	if p.PageSize > maxInstancePageSize {
		p.PageSize = maxInstancePageSize
	}
	if _, ok := instanceSortColumns[p.Sort]; !ok {
		p.Sort = "name"
	}
	if strings.ToLower(p.Order) == "desc" {
		p.Order = "desc"
	} else {
		p.Order = "asc"
	}
}

// NodeCount 单节点实例计数（FR-247 聚合）。
type NodeCount struct {
	NodeID uint  `json:"nodeId" gorm:"column:node_id"`
	Count  int64 `json:"count" gorm:"column:cnt"`
}

// InstanceAggregate 实例维度计数（FR-247）：供前端筛选 chip / 分组头不拉全集即得计数。
// ByStatus/ByRole 含全部枚举键（零补 0）；ByNode 仅含出现的节点（按 nodeId 升序）。
type InstanceAggregate struct {
	Total    int64            `json:"total"`
	ByStatus map[string]int64 `json:"byStatus"`
	ByNode   []NodeCount      `json:"byNode"`
	ByRole   map[string]int64 `json:"byRole"`
}

// applySearchFilters 把可下推维度附加到查询；env/tag 用引号定界 LIKE 精确下推到 SQL
// （分页路径不能再 Go 后置过滤，否则破坏 page/total 一致性），并加名称子串 q。
// 不含 GroupID / 权限作用域（由 scopedBase 按管理员/非管理员分别 JOIN）。
func applySearchFilters(q *gorm.DB, p InstanceSearchParams) *gorm.DB {
	if p.NodeID != nil {
		q = q.Where("instances.node_id = ?", *p.NodeID)
	}
	if p.Status != nil {
		q = q.Where("instances.status = ?", *p.Status)
	}
	if p.Role != nil {
		q = q.Where("instances.role = ?", *p.Role)
	}
	if p.NetworkID != nil {
		q = q.Joins("JOIN network_members ON network_members.instance_id = instances.id").
			Where("network_members.network_id = ?", *p.NetworkID)
	}
	// env/tag 引号定界 LIKE：tags 存 JSON 数组（如 ["env:prod","x"]），匹配带引号的整元素精度足够。
	if env := strings.TrimSpace(p.Env); env != "" {
		q = q.Where("instances.tags LIKE ?", "%\""+model.EnvTagPrefix+env+"\"%")
	}
	if tag := strings.TrimSpace(p.Tag); tag != "" {
		q = q.Where("instances.tags LIKE ?", "%\""+tag+"\"%")
	}
	if query := strings.TrimSpace(p.Query); query != "" {
		q = q.Where("instances.name LIKE ?", "%"+query+"%")
	}
	return q
}

// scopedBase 构造带筛选 + 可选显式组过滤 + 可选权限作用域的基查询（每次返回新查询，调用方可独立终结）。
// scope==nil 表示平台管理员（不限组）；scope 非空限定到这些可访问组（非管理员）。
// GroupInstance.InstanceID 唯一，JOIN 不产重复行，故 Count 计数准确。
func (s *InstanceService) scopedBase(scope []uint, p InstanceSearchParams) *gorm.DB {
	q := applySearchFilters(s.db.Model(&model.Instance{}), p)
	if p.GroupID != nil {
		q = q.Joins("JOIN group_instances gi_filter ON gi_filter.instance_id = instances.id").
			Where("gi_filter.group_id = ?", *p.GroupID)
	}
	if scope != nil {
		q = q.Joins("JOIN group_instances gi_scope ON gi_scope.instance_id = instances.id").
			Where("gi_scope.group_id IN ?", scope)
	}
	return q
}

// SearchInstances 分页搜索实例（FR-247）。返回当前页实例 + 筛选下全量 total。
// scope==nil=平台管理员；非空=限定可访问组；非空且为空集=无可访问组，直接空结果。
func (s *InstanceService) SearchInstances(scope []uint, p InstanceSearchParams) ([]model.Instance, int64, error) {
	p.Normalize()
	if scope != nil && len(scope) == 0 {
		return []model.Instance{}, 0, nil
	}

	var total int64
	if err := s.scopedBase(scope, p).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计实例数失败: %w", err)
	}

	col := instanceSortColumns[p.Sort]
	var items []model.Instance
	if err := s.scopedBase(scope, p).
		Order(col + " " + p.Order + ", instances.id asc").
		Limit(p.PageSize).Offset((p.Page - 1) * p.PageSize).
		Find(&items).Error; err != nil {
		return nil, 0, fmt.Errorf("查询实例列表失败: %w", err)
	}
	return items, total, nil
}

// AggregateInstances 在同筛选 + 作用域下按状态/节点/角色分组计数（FR-247）。
func (s *InstanceService) AggregateInstances(scope []uint, p InstanceSearchParams) (InstanceAggregate, error) {
	agg := InstanceAggregate{ByStatus: map[string]int64{}, ByRole: map[string]int64{}}
	// 预置全枚举键为 0，前端 chip 不缺键。
	for _, st := range []model.InstanceStatus{
		model.InstanceStatusStopped, model.InstanceStatusStarting, model.InstanceStatusRunning,
		model.InstanceStatusStopping, model.InstanceStatusCrashed,
	} {
		agg.ByStatus[string(st)] = 0
	}
	for _, r := range []model.InstanceRole{model.InstanceRoleBackend, model.InstanceRoleProxy, model.InstanceRoleUniversal} {
		agg.ByRole[string(r)] = 0
	}
	if scope != nil && len(scope) == 0 {
		return agg, nil
	}

	type countRow struct {
		Grp string `gorm:"column:grp"`
		Cnt int64  `gorm:"column:cnt"`
	}

	if err := s.scopedBase(scope, p).Count(&agg.Total).Error; err != nil {
		return agg, fmt.Errorf("统计实例总数失败: %w", err)
	}

	var statusRows []countRow
	if err := s.scopedBase(scope, p).Select("instances.status as grp, count(*) as cnt").
		Group("instances.status").Scan(&statusRows).Error; err != nil {
		return agg, fmt.Errorf("按状态聚合失败: %w", err)
	}
	for _, r := range statusRows {
		agg.ByStatus[r.Grp] = r.Cnt
	}

	var roleRows []countRow
	if err := s.scopedBase(scope, p).Select("instances.role as grp, count(*) as cnt").
		Group("instances.role").Scan(&roleRows).Error; err != nil {
		return agg, fmt.Errorf("按角色聚合失败: %w", err)
	}
	for _, r := range roleRows {
		agg.ByRole[r.Grp] = r.Cnt
	}

	if err := s.scopedBase(scope, p).Select("instances.node_id as node_id, count(*) as cnt").
		Group("instances.node_id").Order("instances.node_id asc").Scan(&agg.ByNode).Error; err != nil {
		return agg, fmt.Errorf("按节点聚合失败: %w", err)
	}

	return agg, nil
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
	// CPULimit/MemLimitMB/DiskLimitMB 是 docker 模式资源限额（FR-079）；nil=不变，0=清除限制。
	CPULimit    *float64
	MemLimitMB  *int64
	DiskLimitMB *int64
}

// Update 更新实例配置。各字段为 nil 时表示不变。
func (s *InstanceService) Update(id uint, f UpdateInstanceFields) (*model.Instance, error) {
	releaseOperation := s.acquireInstanceOperation(id)
	defer releaseOperation()

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
		raw, err := json.Marshal(*f.EnvVars)
		if err != nil {
			return nil, fmt.Errorf("序列化实例环境变量失败: %w", err)
		}
		updates["env_vars"] = string(raw)
	}
	if f.Tags != nil {
		// 规范化后持久化为 JSON；空集合落 "null"，ParseTags 读回为空，等价清空标签。
		raw, err := json.Marshal(model.NormalizeTags(*f.Tags))
		if err != nil {
			return nil, fmt.Errorf("序列化实例标签失败: %w", err)
		}
		updates["tags"] = string(raw)
	}
	// docker 资源限额（FR-079）：传指针即写入（含 0=清除限制）。变更对下一次启动生效（启动时随 spec 定型）。
	if f.CPULimit != nil {
		updates["cpu_limit"] = normalizeCPULimit(*f.CPULimit)
	}
	if f.MemLimitMB != nil {
		updates["mem_limit_mb"] = normalizeResourceLimitMB(*f.MemLimitMB)
	}
	if f.DiskLimitMB != nil {
		updates["disk_limit_mb"] = normalizeResourceLimitMB(*f.DiskLimitMB)
	}

	if len(updates) > 0 {
		if err := s.db.Model(instance).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("更新实例失败: %w", err)
		}
	}

	updated, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	launchSpecChanged := f.StartCommand != nil || f.AutoRestart != nil || f.JDKID != nil || f.EnvVars != nil
	if launchSpecChanged && s.pool != nil {
		// 保存只更新 Worker 的下次启动规格，不触发任何生命周期动作；同步失败由后续 Start/Restart 重注册兜底。
		if err := s.registerOnWorkerLocked(updated); err != nil {
			slog.Warn("实例启动规格已保存但同步在线 Worker 失败", "instanceId", updated.UUID, "error", err)
		}
	}
	return updated, nil
}

// deleteStopSettleInterval / deleteStopSettleMargin 是删除编排的停止收敛节奏（FR-310）。
// StopInstance 对 daemon 策略是「发起停止」即返回（wrapper 端优雅关服、超时强杀进程树兜底），
// 进程真正退出并释放文件锁可能滞后整个优雅停止窗口，Windows 上 Worker 的 RemoveAll 在此
// 窗口内会撞锁失败——删除编排在窗口内按 interval 重试清理，而非把「删除失败请重试」抛给用户；
// margin 是优雅停止超时之外的额外等待余量。测试经这两个变量缩短节奏。
var (
	deleteStopSettleInterval = 2 * time.Second
	deleteStopSettleMargin   = 15 * time.Second
)

// Delete 删除实例。运行中/启动中/停止中的实例先同步停止（FR-310：Worker 侧优雅停，
// 超时强杀进程树），停成才继续删；停不成中止删除并返回可操作错误——此前 CP 删记录而
// Worker 运行态守卫拒杀进程，DB 与现实脱钩，java 进程沦为无主孤儿继续占端口。
// 随后经 gRPC 让 Worker 清理实例数据（工作目录 + 派生索引 + 注册表条目），成功后再删记录，
// 兑现删除确认文案「所有数据将被删除」；否则系统分配的 hash 后缀目录（ADR-007）不复用，
// 反复建删会无限堆积孤儿目录。清理失败则中止删除（记录保留可重试），不静默孤儿化。
func (s *InstanceService) Delete(id uint) error {
	return s.deleteInternal(id, 0)
}

// DeleteForExpectedNode Agent 专用删除：锁内重验 node 归属，变化则拒绝（FR-396）。
func (s *InstanceService) DeleteForExpectedNode(id, expectedNodeID uint) error {
	if expectedNodeID == 0 {
		return fmt.Errorf("expectedNodeID 无效")
	}
	return s.deleteInternal(id, expectedNodeID)
}

func (s *InstanceService) deleteInternal(id, expectedNodeID uint) error {
	releaseOperation := s.acquireInstanceOperation(id)
	defer releaseOperation()

	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if expectedNodeID != 0 && instance.NodeID != expectedNodeID {
		return ErrAgentForbidden
	}
	stoppedForDelete := false
	switch instance.Status {
	case model.InstanceStatusRunning, model.InstanceStatusStarting, model.InstanceStatusStopping:
		if err := s.stopForDelete(instance); err != nil {
			return err
		}
		stoppedForDelete = true
	}

	if err := s.removeWorkerDataSettled(instance, stoppedForDelete); err != nil {
		return fmt.Errorf("清理实例数据失败: %w", err)
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		// 删除组关联
		tx.Where("instance_id = ?", id).Delete(&model.GroupInstance{})
		// 级联删除群组服关系（ADR-007）：作为代理或后端的注册记录、群组成员关系。
		tx.Where("proxy_id = ? OR backend_id = ?", id, id).Delete(&model.ServerRegistration{})
		tx.Where("instance_id = ?", id).Delete(&model.NetworkMember{})
		// 级联清理崩溃快照（FR-313）。
		tx.Where("instance_id = ?", id).Delete(&model.InstanceCrashSnapshot{})
		// 删除实例
		return tx.Delete(&model.Instance{}, id).Error
	})
}

// stopForDelete 删除前的同步停止编排（FR-310）。与 Stop 的异步委托不同：删除必须确证
// 进程处置已发起且 Worker 受理，才能继续删目录/删记录，故同步调 StopInstance 并等待结果。
func (s *InstanceService) stopForDelete(instance *model.Instance) error {
	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w：实例所属节点记录不存在，无法停止并清理 Worker 数据；已中止删除，实例记录保留", ErrInstanceRunning)
		}
		return fmt.Errorf("查找节点失败: %w", err)
	}
	if node.Status != model.NodeStatusOnline {
		return fmt.Errorf("%w：节点 %s 已离线，无法停止运行中的实例；请待节点恢复后重试删除", ErrInstanceRunning, node.Name)
	}
	if s.pool == nil {
		return fmt.Errorf("%w：节点 %s 未连接，无法停止运行中的实例；请待节点恢复后重试删除", ErrInstanceRunning, node.Name)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return fmt.Errorf("%w：节点 %s 未连接，无法停止运行中的实例；请待节点恢复后重试删除", ErrInstanceRunning, node.Name)
	}
	if instance.Status != model.InstanceStatusStopping {
		if err := s.transition(instance.ID, model.InstanceStatusStopping, "停止"); err != nil {
			return fmt.Errorf("%w：%v", ErrInstanceRunning, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := client.Worker.StopInstance(ctx, &workerpb.InstanceActionRequest{InstanceUuid: instance.UUID})
	if err != nil {
		return fmt.Errorf("%w：停止运行中的实例失败: %v", ErrInstanceRunning, err)
	}
	if resp != nil && !resp.Success {
		if strings.Contains(resp.Error, "未运行") {
			s.updateStatusFromTo(instance.ID, model.InstanceStatusStopping, model.InstanceStatusStopped)
			return nil
		}
		return fmt.Errorf("%w：停止运行中的实例失败: %s", ErrInstanceRunning, resp.Error)
	}
	s.updateStatusFromTo(instance.ID, model.InstanceStatusStopping, model.InstanceStatusStopped)
	return nil
}

// removeWorkerDataSettled 删除前的 Worker 数据清理（FR-310）。与 Stop 的异步委托不同：删除必须确证
// Worker 已受理清理，才能继续删目录/删记录；刚停过的实例允许在停止收敛窗口内重试清理。
func (s *InstanceService) removeWorkerDataSettled(instance *model.Instance, stoppedForDelete bool) error {
	deadline := time.Now().Add(deleteStopSettleMargin)
	for {
		err := s.removeWorkerDataOnce(instance)
		if err == nil || !stoppedForDelete || time.Now().After(deadline) {
			return err
		}
		time.Sleep(deleteStopSettleInterval)
	}
}

func (s *InstanceService) removeWorkerDataOnce(instance *model.Instance) error {
	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w：实例所属节点记录不存在，无法停止并清理 Worker 数据；已中止删除，实例记录保留", ErrNodeOffline)
		}
		return fmt.Errorf("查找节点失败: %w", err)
	}
	if node.Status != model.NodeStatusOnline {
		return fmt.Errorf("%w：节点 %s 已离线，无法停止并清理 Worker 数据；已中止删除，实例记录保留",
			ErrNodeOffline, node.Name)
	}
	if s.pool == nil {
		return fmt.Errorf("%w：节点 %s 未连接，无法清理 Worker 数据；已中止删除，实例记录保留",
			ErrNodeOffline, node.Name)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {

		return fmt.Errorf("%w：节点 %s 未连接，无法清理 Worker 数据；已中止删除，实例记录保留",
			ErrNodeOffline, node.Name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// 就地导入实例（FR-302，见 ADR-069）：显式指示 Worker 跳过目录删除，
	// 原目录归用户自管；Worker 托管区守卫是第二道保险。
	resp, err := client.Worker.RemoveInstance(ctx, &workerpb.RemoveInstanceRequest{
		InstanceUuid: instance.UUID,
		WorkDir:      instance.WorkDir,
		SkipWorkDir:  instance.WorkDirInPlace,
	})
	if err != nil {
		return fmt.Errorf("gRPC RemoveInstance 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker 清理失败: %s", resp.Error)
	}
	if resp.WorkDirSkipped {
		// 托管区外的历史手填目录：Worker 拒绝越界删除但放行记录删除，仅告警留痕。
		slog.Warn("删除实例：Worker 跳过工作目录清理", "instanceId", instance.UUID, "reason", resp.SkipReason)
	}
	return nil
}

// Start 启动实例（委托给 Worker Node）。
func (s *InstanceService) Start(id uint) error {
	return s.startInternal(id, 0)
}

// StartForExpectedNode Agent 专用启动：锁内重验 node 归属，变化则拒绝派发（FR-395）。
func (s *InstanceService) StartForExpectedNode(id, expectedNodeID uint) error {
	if expectedNodeID == 0 {
		return fmt.Errorf("expectedNodeID 无效")
	}
	return s.startInternal(id, expectedNodeID)
}

func (s *InstanceService) startInternal(id, expectedNodeID uint) error {
	releaseOperation := s.acquireInstanceOperation(id)
	delegateOwnsOperation := false
	defer func() {
		if !delegateOwnsOperation {
			releaseOperation()
		}
	}()

	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if expectedNodeID != 0 && instance.NodeID != expectedNodeID {
		return ErrAgentForbidden
	}

	// 损毁实例不可直接启动（FR-342）：搭建任一阶段失败进 DAMAGED，须先「重建」复用参数重跑搭建，
	// 成功→STOPPED 再启动。返回 *PreflightError（HTTP 422）带明确原因，前端启动按钮亦禁用。
	if instance.Status == model.InstanceStatusDamaged {
		return &PreflightError{Reason: "实例已损毁（搭建未完成），请先重建再启动"}
	}

	// 在途长操作闸（FR-319/FR-323）：搭建/导入/克隆异步化后实例秒回 STOPPED 可点启动，
	// 但核心可能还在下载、目录还在搬迁/拷贝——此时启动会得到半截工作目录。
	// 若有未终态的 provision/import/clone 任务关联本实例即拒。
	if err := longOpInFlightGate(s.db, id); err != nil {
		return err
	}

	// 内存水位预警闸（FR-317 CP 侧）：按节点最近心跳预判，不足直接拒绝不下发 RPC。
	// Worker 侧启动前还有实时闸兜底（心跳数据最多滞后一个心跳周期）。
	if err := s.memoryGate(instance); err != nil {
		return err
	}

	// 启动前同步预检（FR-314）：转 STARTING 前同步问 Worker 校验 JDK/jar/工作目录，
	// 失败即带具体原因返回、状态不进 STARTING，终结「配置错误也要启动中→崩溃兜一圈」。
	if err := s.preflightStart(instance); err != nil {
		return err
	}

	// 状态转换
	if err := s.transition(id, model.InstanceStatusStarting, "启动"); err != nil {
		return err
	}

	// 委托给 Worker Node；生命周期锁移交给后台委托，RPC 与状态回写结束后释放。
	delegateOwnsOperation = s.spawnDelegate(instance, "start", expectedNodeID, releaseOperation)

	return nil
}

// longOpInFlightGate 拦截「工作目录尚未就绪就点启动」（FR-319 二轮②，FR-323 补漏扩展）：
// 搭建（provision，核心下载中）/导入（import，migrate 搬迁中）/克隆（clone，目录拷贝中）
// 任一未终态（pending/running）任务关联本实例即拒启，文案按 kind 区分并引导看任务中心。
// 包级函数：单实例 Start 与批量 start/restart（FR-331 补漏）共用同一道闸。
func longOpInFlightGate(db *gorm.DB, instanceID uint) error {
	var kinds []string
	err := db.Model(&model.Task{}).
		Where("instance_id = ? AND kind IN ? AND state IN ?",
			instanceID,
			[]string{model.TaskKindProvision, model.TaskKindImport, model.TaskKindClone},
			[]model.TaskState{model.TaskStatePending, model.TaskStateRunning}).
		Order("id").
		Pluck("kind", &kinds).Error
	if err != nil || len(kinds) == 0 {
		return nil // 查询异常不阻断正常启动
	}
	switch kinds[0] {
	case model.TaskKindImport:
		return fmt.Errorf("实例正在导入中（目录搬迁未完成），请等待任务中心的导入任务完成后再启动")
	case model.TaskKindClone:
		return fmt.Errorf("实例正在克隆中（工作目录复制未完成），请等待任务中心的克隆任务完成后再启动")
	default:
		return fmt.Errorf("实例正在搭建中（核心下载未完成），请等待任务中心的搭建任务完成后再启动")
	}
}

// memoryGate 启动前按节点心跳数据预判内存水位（FR-317）。
// 心跳过旧（>90s，节点观测不可信）或字段缺失时放行——实时判定交给 Worker 闸；
// 这里只拦「数据可信且明显塞不下」的启动，避免白走一趟 RPC 和状态翻转。
func (s *InstanceService) memoryGate(instance *model.Instance) error {
	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		return nil // 节点记录异常交给后续委托路径报错，不在闸上拦
	}
	if node.MemoryMB <= 0 || node.MemoryUsedMB <= 0 ||
		node.LastHeartbeat == nil || time.Since(*node.LastHeartbeat) > 90*time.Second {
		return nil
	}
	availMB := node.MemoryMB - node.MemoryUsedMB
	required := memguard.EstimateStartMB(instance.StartCommand, instance.MemLimitMB)
	reserve := memguard.DefaultReserveMB(node.MemoryMB)
	if err := memguard.Check(availMB, required, reserve); err != nil {
		return fmt.Errorf("节点 %s %w", node.Name, err)
	}
	return nil
}

// Stop 停止实例（委托给 Worker Node）。
func (s *InstanceService) Stop(id uint) error {
	return s.stopInternal(id, 0)
}

// StopForExpectedNode Agent 专用停止：锁内重验 node 归属（FR-395）。
func (s *InstanceService) StopForExpectedNode(id, expectedNodeID uint) error {
	if expectedNodeID == 0 {
		return fmt.Errorf("expectedNodeID 无效")
	}
	return s.stopInternal(id, expectedNodeID)
}

func (s *InstanceService) stopInternal(id, expectedNodeID uint) error {
	releaseOperation := s.acquireInstanceOperation(id)
	delegateOwnsOperation := false
	defer func() {
		if !delegateOwnsOperation {
			releaseOperation()
		}
	}()

	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if expectedNodeID != 0 && instance.NodeID != expectedNodeID {
		return ErrAgentForbidden
	}

	if err := s.transition(id, model.InstanceStatusStopping, "停止"); err != nil {
		return err
	}

	delegateOwnsOperation = s.spawnDelegate(instance, "stop", expectedNodeID, releaseOperation)
	return nil
}

// Restart 重启实例。
func (s *InstanceService) Restart(id uint) error {
	return s.restartInternal(id, 0)
}

// RestartForExpectedNode Agent 专用重启：锁内重验 node 归属（FR-395）。
func (s *InstanceService) RestartForExpectedNode(id, expectedNodeID uint) error {
	if expectedNodeID == 0 {
		return fmt.Errorf("expectedNodeID 无效")
	}
	return s.restartInternal(id, expectedNodeID)
}

func (s *InstanceService) restartInternal(id, expectedNodeID uint) error {
	releaseOperation := s.acquireInstanceOperation(id)
	delegateOwnsOperation := false
	defer func() {
		if !delegateOwnsOperation {
			releaseOperation()
		}
	}()

	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if expectedNodeID != 0 && instance.NodeID != expectedNodeID {
		return ErrAgentForbidden
	}

	// 重启最终会重新启动实例，必须与 Start 共用同一道长操作闸；所有复用 Restart 的入口自动覆盖。
	if err := longOpInFlightGate(s.db, id); err != nil {
		return err
	}

	// 重启 = 停止 + 启动，启动侧守卫必须同样生效（真机：代理后端注册被清空后点「重启」
	// 绕过启动预检，BungeeCord 读到空 servers 立崩、JVM 非 daemon 线程残留假活成僵尸）。
	// DAMAGED 守卫与启动预检（节点连通 / jar / JDK / 代理后端）与 Start 同栈；
	// 内存水位闸刻意不进重启——旧进程随即释放同额内存，按新增启动计算会误拒。
	if instance.Status == model.InstanceStatusDamaged {
		return &PreflightError{Reason: "实例已损毁（搭建未完成），请先重建再启动"}
	}
	if err := s.preflightStart(instance); err != nil {
		return err
	}

	if err := s.transition(id, model.InstanceStatusStopping, "重启-停止"); err != nil {
		return err
	}

	delegateOwnsOperation = s.spawnDelegate(instance, "restart", expectedNodeID, releaseOperation)
	return nil
}

// Kill 强制终止实例。
func (s *InstanceService) Kill(id uint) error {
	return s.killInternal(id, 0)
}

// KillForExpectedNode Agent 专用强杀：锁内重验 node 归属，变化则拒绝派发（FR-396）。
func (s *InstanceService) KillForExpectedNode(id, expectedNodeID uint) error {
	if expectedNodeID == 0 {
		return fmt.Errorf("expectedNodeID 无效")
	}
	return s.killInternal(id, expectedNodeID)
}

func (s *InstanceService) killInternal(id, expectedNodeID uint) error {
	releaseOperation := s.acquireInstanceOperation(id)
	delegateOwnsOperation := false
	defer func() {
		if !delegateOwnsOperation {
			releaseOperation()
		}
	}()

	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if expectedNodeID != 0 && instance.NodeID != expectedNodeID {
		return ErrAgentForbidden
	}

	// 强制终止是逃生通道：从任意状态（含 RUNNING / STARTING）直接置 STOPPED，绕过状态机校验。
	// 修 BUG——强杀原走 transition，RUNNING→STOPPED 不在 validTransitions（须经 STOPPING）被「无效的状态转换」
	// 拦下，导致卡在 RUNNING/STARTING 的实例无法强停。validTransitions 仅约束正常生命周期，强杀不受其限。
	if err := s.UpdateStatus(id, model.InstanceStatusStopped); err != nil {
		return err
	}

	delegateOwnsOperation = s.spawnDelegate(instance, "kill", expectedNodeID, releaseOperation)
	return nil
}

// SendCommand 向运行中的实例下发一行控制台命令（复用既有 SendCommand RPC，FR-005）。
// 仅 RUNNING 实例可下发（其它状态进程 stdin 不存在，返回 ErrInstanceNotRunning）；命令不改变实例状态。
// 与 Start/Stop 的异步委托不同：命令需即时反馈成功/失败，故走同步委托（与 GetMetrics 一致），
// 不经 spawnDelegate，因此测试中 Shutdown 禁用异步委托后仍可观测结果。
func (s *InstanceService) SendCommand(id uint, command string) error {
	return s.sendCommandInternal(id, command, 0)
}

// SendCommandForExpectedNode Agent 专用命令：重验 node 归属后同步下发（FR-396）。
func (s *InstanceService) SendCommandForExpectedNode(id uint, command string, expectedNodeID uint) error {
	if expectedNodeID == 0 {
		return fmt.Errorf("expectedNodeID 无效")
	}
	return s.sendCommandInternal(id, command, expectedNodeID)
}

func (s *InstanceService) sendCommandInternal(id uint, command string, expectedNodeID uint) error {
	instance, err := s.GetByID(id)
	if err != nil {
		return err
	}
	if expectedNodeID != 0 && instance.NodeID != expectedNodeID {
		return ErrAgentForbidden
	}
	if instance.Status != model.InstanceStatusRunning {
		return fmt.Errorf("%w：当前状态 %s", ErrInstanceNotRunning, instance.Status)
	}

	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		return fmt.Errorf("查找节点失败: %w", err)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return fmt.Errorf("节点 %s 未连接", node.UUID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Worker.SendCommand(ctx, &workerpb.SendCommandRequest{
		InstanceUuid: instance.UUID,
		Command:      command,
	})
	if err != nil {
		return fmt.Errorf("gRPC SendCommand 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker SendCommand 失败: %s", resp.Error)
	}
	return nil
}

// registerOnWorker 在单实例生命周期锁内重读当前记录后注册，拒绝删除后的旧实例指针重新登记 Worker。
func (s *InstanceService) registerOnWorker(instance *model.Instance) error {
	releaseOperation := s.acquireInstanceOperation(instance.ID)
	defer releaseOperation()

	current, err := s.GetByID(instance.ID)
	if err != nil || current.UUID != instance.UUID {
		return ErrInstanceNotFound
	}
	return s.registerOnWorkerLocked(current)
}

// registerOnWorkerLocked 注册当前实例；调用方必须持有对应实例生命周期锁。
func (s *InstanceService) registerOnWorkerLocked(instance *model.Instance) error {
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

	spec, err := s.buildCreateInstanceRequest(instance)
	if err != nil {
		return err
	}

	resp, err := client.Worker.CreateInstance(ctx, spec)
	if err != nil {
		return fmt.Errorf("Worker CreateInstance 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker CreateInstance 失败: %s", resp.Error)
	}
	return nil
}

// buildCreateInstanceRequest 把一条实例模型翻译成下发给 Worker 的实例规格。
// 解出 EnvVars JSON、解析绑定 JDK 安装路径（注入 JAVA_HOME/PATH，ADR-008）、派生优雅停止命令、
// 计算 docker 端口映射。registerOnWorker（单实例补注册）与 ResyncNode（重连整节点重推，见 ADR-050）
// 共用此翻译，确保两条路径下发给 Worker 的规格完全一致。
func (s *InstanceService) buildCreateInstanceRequest(instance *model.Instance) (*workerpb.CreateInstanceRequest, error) {
	// 把存储为 JSON 字符串的 EnvVars 解出来，原样下发给 Worker 注入到进程环境。
	var envVars map[string]string
	if strings.TrimSpace(instance.EnvVars) != "" {
		if err := json.Unmarshal([]byte(instance.EnvVars), &envVars); err != nil {
			return nil, fmt.Errorf("解析实例环境变量失败: %w", err)
		}
	}

	// 解析实例绑定的 JDK 安装路径下发给 Worker：Worker 启动时据此注入 JAVA_HOME 并把
	// <jdk>/bin 接入 PATH（ADR-008 / FR-033），结构化启动命令里的 `java` 即指向它。
	jdkPath, err := s.resolveJDKPath(instance)
	if err != nil {
		return nil, err
	}

	return &workerpb.CreateInstanceRequest{
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
		Image:                      instance.Image,
		PortMappings:               dockerPortMappings(instance),
		CpuLimit:                   instance.CPULimit,
		MemLimitMb:                 instance.MemLimitMB,
		DiskLimitMb:                instance.DiskLimitMB,
	}, nil
}

// ResyncNode 在 Worker 重连/重注册成功后，把该节点全部实例规格一次性重推给 Worker（见 ADR-050）。
// 修 bug #2：Worker 重启后只经 PID 文件恢复 RUNNING daemon 实例，STOPPED 实例在 Worker 内存
// 注册表丢失，致文件/配置/归档 op 报「实例不存在」。CP 是真源，重连后重推让 Worker 重新认识停机实例
// （Worker 据此按 STOPPED 填表、不启动进程；已恢复的 RUNNING 实例在 Worker 侧按 UUID 命中跳过）。
//
// 由 onWorkerConnect 回调触发（Register 成功或心跳重连后），异步执行、失败仅告警不阻断
// （与 registerOnWorker 失败的容错一致；个别实例启动路径仍有 registerOnWorker 兜底）。
func (s *InstanceService) ResyncNode(nodeUUID string) {
	var node model.Node
	if err := s.db.Where("uuid = ?", nodeUUID).First(&node).Error; err != nil {
		slog.Warn("重连重推：查节点失败", "nodeUUID", nodeUUID, "error", err)
		return
	}

	var instances []model.Instance
	if err := s.db.Where("node_id = ?", node.ID).Order("id").Find(&instances).Error; err != nil {
		slog.Warn("重连重推：查实例列表失败", "nodeUUID", nodeUUID, "error", err)
		return
	}
	if len(instances) == 0 {
		return
	}

	// 与单实例生命周期动作、删除共用同一组锁。锁内重读可剔除等待期间已删除的记录；
	// 若重推先获得锁，删除会等待 RPC 完成后再移除 Worker 注册，两个顺序都不会留下旧快照重建。
	releaseOperations := make([]func(), 0, len(instances))
	instanceIDs := make([]uint, 0, len(instances))
	for i := range instances {
		releaseOperations = append(releaseOperations, s.acquireInstanceOperation(instances[i].ID))
		instanceIDs = append(instanceIDs, instances[i].ID)
	}
	defer func() {
		for i := len(releaseOperations) - 1; i >= 0; i-- {
			releaseOperations[i]()
		}
	}()
	if err := s.db.Where("id IN ? AND node_id = ?", instanceIDs, node.ID).Order("id").Find(&instances).Error; err != nil {
		slog.Warn("重连重推：锁内刷新实例列表失败", "nodeUUID", nodeUUID, "error", err)
		return
	}
	if len(instances) == 0 {
		return
	}

	client, ok := s.pool.Get(nodeUUID)
	if !ok {
		slog.Warn("重连重推：节点未连接", "nodeUUID", nodeUUID)
		return
	}

	specs := make([]*workerpb.CreateInstanceRequest, 0, len(instances))
	for i := range instances {
		spec, err := s.buildCreateInstanceRequest(&instances[i])
		if err != nil {
			// 单条实例规格构造失败（如绑定 JDK 已删）不应拖垮整批重推：跳过该条，其余照常重推。
			slog.Warn("重连重推：构造实例规格失败，跳过", "instanceId", instances[i].UUID, "error", err)
			continue
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Worker.ResyncInstances(ctx, &workerpb.ResyncInstancesRequest{Instances: specs})
	if err != nil {
		slog.Warn("重连重推实例规格失败", "nodeUUID", nodeUUID, "count", len(specs), "error", err)
		return
	}
	slog.Info("已向 Worker 重推实例规格", "nodeUUID", nodeUUID, "pushed", len(specs), "registered", resp.Registered, "skipped", resp.Skipped)
}

// mcContainerServerPort 是 MC 服务端在容器内的约定监听端口（多数 MC 镜像固定 25565）。
// docker 模式把端口池分配的宿主端口映射到此容器内端口（ADR-019）。
const mcContainerServerPort = 25565

// dockerPortMappings 为 docker 模式实例派生容器↔宿主端口映射（FR-078，ADR-019）。
// 非 docker 模式返回 nil（direct/daemon 直接监听宿主端口，无需映射）。
// MC 实例：宿主 ServerPort 映射到容器内 25565（tcp + query 同号 udp）；
// 其它类型：缺乏容器内端口约定，宿主端口与容器内端口同号直通（host=container）。
func dockerPortMappings(instance *model.Instance) []*workerpb.PortMapping {
	if instance.ProcessType != model.ProcessTypeDocker {
		return nil
	}
	var out []*workerpb.PortMapping
	if instance.Type == model.InstanceTypeMinecraftJava {
		if instance.ServerPort > 0 {
			out = append(out,
				&workerpb.PortMapping{ContainerPort: mcContainerServerPort, HostPort: int32(instance.ServerPort), Protocol: "tcp"},
				&workerpb.PortMapping{ContainerPort: mcContainerServerPort, HostPort: int32(instance.ServerPort), Protocol: "udp"},
			)
		}
		return out
	}
	// 通用容器：宿主端口与容器内端口同号直通。
	if instance.ServerPort > 0 {
		out = append(out, &workerpb.PortMapping{ContainerPort: int32(instance.ServerPort), HostPort: int32(instance.ServerPort), Protocol: "tcp"})
	}
	return out
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
// releaseOperation 由后台委托在 RPC 与状态回写结束后调用，使删除与后续生命周期请求等待在途动作。
// expectedNodeID>0 时派发前再次重读实例归属；变化则拒绝 Worker RPC（FR-395）。
// Shutdown 之后（bgCtx 取消）不再发起新委托，返回 false 让调用方自行释放生命周期锁。
func (s *InstanceService) spawnDelegate(instance *model.Instance, action string, expectedNodeID uint, releaseOperation func()) bool {
	s.bgMu.Lock()
	if s.bgCtx.Err() != nil {
		s.bgMu.Unlock()
		return false
	}
	s.bgWG.Add(1)
	s.bgMu.Unlock()

	go func() {
		defer s.bgWG.Done()
		defer releaseOperation()
		if expectedNodeID != 0 {
			current, err := s.GetByID(instance.ID)
			if err != nil || current.NodeID != expectedNodeID {
				slog.Warn("实例归属变化，取消 Agent 生命周期派发",
					"instanceId", instance.UUID, "expectedNodeId", expectedNodeID)
				return
			}
			instance = current
		}
		s.delegateToWorker(instance, action)
	}()
	return true
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
		s.updateStatusReasonAsync(instance.ID, model.InstanceStatusCrashed, "查找节点失败: "+err.Error())
		return
	}

	// 获取 gRPC 客户端
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		slog.Error("节点未连接", "nodeUUID", node.UUID)
		s.updateStatusReasonAsync(instance.ID, model.InstanceStatusCrashed, "节点未连接")
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
		if regErr := s.registerOnWorkerLocked(instance); regErr != nil {
			slog.Debug("实例已在 Worker 注册或注册失败", "instanceId", instance.UUID, "error", regErr)
		}
		resp, err = client.Worker.StartInstance(ctx, req)
	case "stop":
		resp, err = client.Worker.StopInstance(ctx, req)
	case "restart":
		// Restart 不经过 Start 的预检链路，须先幂等重注册；同步失败时禁止继续用 Worker 缓存旧规格重启。
		if regErr := s.registerOnWorkerLocked(instance); regErr != nil {
			reason := "重启前同步最新启动规格失败，已取消重启: " + regErr.Error()
			slog.Error("重启前同步最新启动规格失败，已取消重启", "instanceId", instance.UUID, "error", regErr)
			s.updateStatusReasonAsync(instance.ID, model.InstanceStatusCrashed, reason)
			return
		}
		resp, err = client.Worker.RestartInstance(ctx, req)
	case "kill":
		resp, err = client.Worker.KillInstance(ctx, req)
	}

	if err != nil {
		slog.Error("Worker 操作失败", "action", action, "instanceId", instance.UUID, "error", err)
		s.updateStatusReasonAsync(instance.ID, model.InstanceStatusCrashed, "Worker 操作失败: "+err.Error())
		return
	}

	if resp != nil && !resp.Success {
		slog.Error("Worker 操作未成功", "action", action, "instanceId", instance.UUID, "error", resp.Error)
		s.updateStatusReasonAsync(instance.ID, model.InstanceStatusCrashed, resp.Error)
		return
	}

	// 操作成功，更新状态
	if action == "start" {
		// 仅当仍处 STARTING 时才置 RUNNING：启动委托回来前用户可能已点停止（STARTING→STOPPING），
		// 此时迟到的 start 成功不得把已在停止中的实例「复活」成 RUNNING（修 #5 启动中停止竞态）。
		s.updateStatusFromTo(instance.ID, model.InstanceStatusStarting, model.InstanceStatusRunning)
		slog.Info("Worker 操作成功", "action", action, "instanceId", instance.UUID)
		return
	}

	var targetStatus model.InstanceStatus
	switch action {
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

// updateStatusFromTo 仅当实例当前状态为 from 时才置为 to（条件更新，单条 SQL 原子）。
// 用于异步委托回写时避免覆盖期间发生的并发状态变更（如启动成功覆盖已发起的停止）。
func (s *InstanceService) updateStatusFromTo(id uint, from, to model.InstanceStatus) {
	if err := s.db.Model(&model.Instance{}).Where("id = ? AND status = ?", id, from).
		Update("status", to).Error; err != nil {
		slog.Error("条件更新实例状态失败", "instanceId", id, "from", from, "to", to, "error", err)
	}
}

// updateStatusReasonAsync 异步置状态 + 原因：委托失败时写入崩溃原因（供前端显示具体错误，不再只见「崩溃」无因）。
func (s *InstanceService) updateStatusReasonAsync(id uint, status model.InstanceStatus, reason string) {
	if err := s.db.Model(&model.Instance{}).Where("id = ?", id).
		Updates(map[string]any{"status": status, "status_reason": reason}).Error; err != nil {
		slog.Error("更新实例状态/原因失败", "instanceId", id, "status", status, "error", err)
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

	// 正常状态推进清空崩溃原因（StatusReason 仅在异步委托失败时写入；每次启动/停止重置）。
	if err := s.db.Model(instance).Updates(map[string]any{"status": target, "status_reason": ""}).Error; err != nil {
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

	// 指标纯探针（FR-067 退役 RCON）：仅下发 probe_port，未部署探针即 N/A。
	resp, err := client.Worker.GetInstanceMetrics(ctx, &workerpb.GetInstanceMetricsRequest{
		InstanceUuid: instance.UUID,
		ProbePort:    int32(instance.ProbePort),
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

// InstanceEnvData 实例环境（FR-344 环境变量页签）：configured=自定义启动 env（可编辑源），
// runtime=运行时进程实际环境（含继承，只读）；runtimeAvailable=false 表示未运行/平台受限。
type InstanceEnvData struct {
	Configured       map[string]string `json:"configured"`
	Runtime          map[string]string `json:"runtime"`
	RuntimeAvailable bool              `json:"runtimeAvailable"`
	Note             string            `json:"note"`
}

// GetInstanceEnv 返回实例自定义启动环境变量 + 运行时进程实际环境（FR-344）。
// configured 恒返回（解自 instance.EnvVars）；runtime 尽力而为——节点未连/未运行/平台受限时
// runtimeAvailable=false + note，不阻塞 configured 返回（上区编辑不受运行态影响）。
type ManagedProcessInfo struct {
	PID               int32   `json:"pid"`
	ParentPID         int32   `json:"parentPid"`
	Name              string  `json:"name"`
	IsRoot            bool    `json:"isRoot"`
	CPUPercent        float64 `json:"cpuPercent"`
	RSSBytes          int64   `json:"rssBytes"`
	ReadBytesPerSec   uint64  `json:"readBytesPerSec"`
	WriteBytesPerSec  uint64  `json:"writeBytesPerSec"`
	User              string  `json:"user"`
	CommandSummary    string  `json:"commandSummary"`
	UptimeSeconds     float64 `json:"uptimeSeconds"`
	ThreadCount       int32   `json:"threadCount"`
	SampledAt         string  `json:"sampledAt"`
	UnavailableReason string  `json:"unavailableReason"`
}

type ManagedProcessInstanceSummary struct {
	ID       uint   `json:"id"`
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	NodeID   uint   `json:"nodeId"`
	NodeUUID string `json:"nodeUuid"`
	NodeName string `json:"nodeName"`
}

type ManagedProcessDiagnostic struct {
	Code       string `json:"code"`
	Severity   string `json:"severity"`
	Title      string `json:"title"`
	Evidence   string `json:"evidence"`
	Suggestion string `json:"suggestion"`
}

type ManagedProcessHistory struct {
	WindowSeconds       int     `json:"windowSeconds"`
	SampleCount         int     `json:"sampleCount"`
	LatestSampledAt     string  `json:"latestSampledAt"`
	RSSDeltaBytes       int64   `json:"rssDeltaBytes"`
	AvgCPUPercent       float64 `json:"avgCpuPercent"`
	AvgWriteBytesPerSec float64 `json:"avgWriteBytesPerSec"`
}

type ManagedProcessDetail struct {
	Instance    ManagedProcessInstanceSummary `json:"instance"`
	RootPID     int64                         `json:"rootPid"`
	Target      ManagedProcessInfo            `json:"target"`
	Ancestors   []ManagedProcessInfo          `json:"ancestors"`
	Children    []ManagedProcessInfo          `json:"children"`
	Diagnostics []ManagedProcessDiagnostic    `json:"diagnostics"`
	History     ManagedProcessHistory         `json:"history"`
}

type ManagedProcessActionResult struct {
	Success      bool    `json:"success"`
	Action       string  `json:"action"`
	PID          int32   `json:"pid"`
	AffectedPids []int32 `json:"affectedPids"`
	Message      string  `json:"message"`
}

type ManagedProcessError struct {
	Code    string
	Message string
}

func (e *ManagedProcessError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Code
}

func (s *InstanceService) InspectManagedProcess(id uint, pid int32) (*ManagedProcessDetail, error) {
	if pid <= 0 {
		return nil, &ManagedProcessError{Code: "INVALID_PID", Message: "pid 必须为正整数"}
	}
	instance, node, client, err := s.managedProcessClient(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Worker.InspectManagedProcess(ctx, &workerpb.ManagedProcessInspectRequest{InstanceUuid: instance.UUID, Pid: pid})
	if err != nil {
		return nil, fmt.Errorf("探查受管进程失败: %w", err)
	}
	if !resp.Success {
		return nil, &ManagedProcessError{Code: resp.Code, Message: resp.Message}
	}
	history, rows, err := s.managedProcessHistory(instance.UUID, pid)
	if err != nil {
		return nil, fmt.Errorf("查询进程历史样本失败: %w", err)
	}
	diagnostics := managedProcessDiagnostics(history, rows)
	return managedProcessDetailFromProto(resp, instance, node, history, diagnostics), nil
}

func (s *InstanceService) TerminateManagedProcess(id uint, pid int32, mode string) (*ManagedProcessActionResult, error) {
	mode = strings.TrimSpace(mode)
	if pid <= 0 {
		return nil, &ManagedProcessError{Code: "INVALID_PID", Message: "pid 必须为正整数"}
	}
	if mode != "terminate" && mode != "kill_tree" {
		return nil, &ManagedProcessError{Code: "INVALID_REQUEST", Message: "不支持的进程处置模式"}
	}
	instance, _, client, err := s.managedProcessClient(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Worker.TerminateManagedProcess(ctx, &workerpb.ManagedProcessActionRequest{InstanceUuid: instance.UUID, Pid: pid, Mode: mode})
	if err != nil {
		return nil, fmt.Errorf("处置受管进程失败: %w", err)
	}
	if !resp.Success {
		return nil, &ManagedProcessError{Code: resp.Code, Message: resp.Message}
	}
	return &ManagedProcessActionResult{Success: true, Action: resp.Mode, PID: resp.Pid, AffectedPids: resp.AffectedPids, Message: resp.Message}, nil
}

func (s *InstanceService) managedProcessClient(id uint) (*model.Instance, *model.Node, *cpgrpc.Client, error) {
	instance, err := s.GetByID(id)
	if err != nil {
		return nil, nil, nil, err
	}
	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		return nil, nil, nil, fmt.Errorf("查找节点失败: %w", err)
	}
	if s.pool == nil {
		return nil, nil, nil, fmt.Errorf("%w: %s", ErrNodeOffline, node.UUID)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, nil, nil, fmt.Errorf("%w: %s", ErrNodeOffline, node.UUID)
	}
	return instance, &node, client, nil
}

const managedProcessHistoryWindow = 30 * time.Minute

func (s *InstanceService) managedProcessHistory(instanceUUID string, pid int32) (ManagedProcessHistory, []model.ProcessMetricSnapshot, error) {
	var rows []model.ProcessMetricSnapshot
	// 采样入库统一存 UTC（metric.go 写入 sampledAt.UTC()），查询窗口须用 UTC 时间对齐，
	// 否则本地时区（如 +08:00）比较会把窗口起点推后 8 小时、永远查不到样本（真机验收 FR-407 抓到）。
	from := time.Now().UTC().Add(-managedProcessHistoryWindow)
	err := s.db.Where("instance_uuid = ? AND pid = ? AND sampled_at >= ?", instanceUUID, pid, from).
		Order("sampled_at ASC").Find(&rows).Error
	if err != nil {
		return ManagedProcessHistory{}, nil, err
	}
	return summarizeManagedProcessHistory(rows), rows, nil
}

func summarizeManagedProcessHistory(rows []model.ProcessMetricSnapshot) ManagedProcessHistory {
	history := ManagedProcessHistory{WindowSeconds: int(managedProcessHistoryWindow.Seconds()), SampleCount: len(rows)}
	if len(rows) == 0 {
		return history
	}
	first, last := rows[0], rows[len(rows)-1]
	history.LatestSampledAt = last.SampledAt.UTC().Format(time.RFC3339)
	history.RSSDeltaBytes = int64(last.RSSBytes) - int64(first.RSSBytes)
	for _, row := range rows {
		history.AvgCPUPercent += row.CPUPercent
		history.AvgWriteBytesPerSec += float64(row.WriteBytesPerSec)
	}
	history.AvgCPUPercent /= float64(len(rows))
	history.AvgWriteBytesPerSec /= float64(len(rows))
	return history
}

func managedProcessDiagnostics(history ManagedProcessHistory, rows []model.ProcessMetricSnapshot) []ManagedProcessDiagnostic {
	if history.SampleCount < 3 {
		return []ManagedProcessDiagnostic{managedProcessInsufficientSamples(history)}
	}
	diagnostics := make([]ManagedProcessDiagnostic, 0, 4)
	if latestSampleStale(rows[len(rows)-1].SampledAt) {
		diagnostics = append(diagnostics, ManagedProcessDiagnostic{Code: "stale_samples", Severity: "warning", Title: "采样陈旧", Evidence: "最近进程样本超过 90 秒未更新。", Suggestion: "请先确认 Worker 心跳和节点连通性，再判断进程趋势。"})
	}
	if history.AvgCPUPercent >= 80 {
		diagnostics = append(diagnostics, ManagedProcessDiagnostic{Code: "high_cpu", Severity: "warning", Title: "CPU 持续高占用", Evidence: fmt.Sprintf("最近 %d 个样本平均 CPU %.1f%%。", history.SampleCount, history.AvgCPUPercent), Suggestion: "优先检查插件、脚本或任务循环；必要时先通过实例控制台做业务内降载。"})
	}
	if history.AvgWriteBytesPerSec >= 10*1024*1024 {
		diagnostics = append(diagnostics, ManagedProcessDiagnostic{Code: "high_write_io", Severity: "warning", Title: "IO 写入偏高", Evidence: fmt.Sprintf("最近 %d 个样本平均写入 %.1f MiB/s。", history.SampleCount, history.AvgWriteBytesPerSec/1024/1024), Suggestion: "请检查日志刷屏、存档频繁写入或备份任务。"})
	}
	return append(diagnostics, rssGrowthDiagnostic(rows)...)
}

func managedProcessInsufficientSamples(history ManagedProcessHistory) ManagedProcessDiagnostic {
	return ManagedProcessDiagnostic{Code: "insufficient_samples", Severity: "info", Title: "样本不足", Evidence: fmt.Sprintf("最近 %d 秒仅有 %d 个样本，不能判断趋势。", history.WindowSeconds, history.SampleCount), Suggestion: "等待至少 3 个采样周期后再查看诊断结论。"}
}

func latestSampleStale(sampledAt time.Time) bool {
	return time.Since(sampledAt) > 90*time.Second
}

func rssGrowthDiagnostic(rows []model.ProcessMetricSnapshot) []ManagedProcessDiagnostic {
	mid := len(rows) / 2
	firstMax, secondMin := maxRSS(rows[:mid]), minRSS(rows[mid:])
	if secondMin <= firstMax {
		return nil
	}
	delta := int64(secondMin - firstMax)
	if firstMax == 0 {
		return nil
	}
	if delta < 256*1024*1024 && float64(delta)/float64(firstMax) < 0.2 {
		return nil
	}
	return []ManagedProcessDiagnostic{{Code: "rss_growth", Severity: "warning", Title: "RSS 持续增长", Evidence: fmt.Sprintf("后半窗口最小 RSS 比前半窗口最大 RSS 高 %d MiB。", delta/1024/1024), Suggestion: "RSS 是操作系统观察值，请结合 JVM/插件指标确认是否存在内存泄漏。"}}
}

func maxRSS(rows []model.ProcessMetricSnapshot) uint64 {
	var out uint64
	for _, row := range rows {
		if row.RSSBytes > out {
			out = row.RSSBytes
		}
	}
	return out
}

func minRSS(rows []model.ProcessMetricSnapshot) uint64 {
	if len(rows) == 0 {
		return 0
	}
	out := rows[0].RSSBytes
	for _, row := range rows[1:] {
		if row.RSSBytes < out {
			out = row.RSSBytes
		}
	}
	return out
}

func managedProcessDetailFromProto(resp *workerpb.ManagedProcessInspectResponse, instance *model.Instance, node *model.Node, history ManagedProcessHistory, diagnostics []ManagedProcessDiagnostic) *ManagedProcessDetail {
	out := &ManagedProcessDetail{Instance: managedProcessInstanceSummary(instance, node), RootPID: resp.RootPid, Target: managedProcessInfoFromProto(resp.Target), History: history, Diagnostics: diagnostics}
	for _, ancestor := range resp.Ancestors {
		out.Ancestors = append(out.Ancestors, managedProcessInfoFromProto(ancestor))
	}
	for _, child := range resp.Children {
		out.Children = append(out.Children, managedProcessInfoFromProto(child))
	}
	return out
}

func managedProcessInstanceSummary(instance *model.Instance, node *model.Node) ManagedProcessInstanceSummary {
	return ManagedProcessInstanceSummary{ID: instance.ID, UUID: instance.UUID, Name: instance.Name, NodeID: node.ID, NodeUUID: node.UUID, NodeName: node.Name}
}

func managedProcessInfoFromProto(info *workerpb.ManagedProcessInfo) ManagedProcessInfo {
	if info == nil {
		return ManagedProcessInfo{}
	}
	return ManagedProcessInfo{
		PID:               info.Pid,
		ParentPID:         info.Ppid,
		Name:              info.Name,
		IsRoot:            info.IsRoot,
		CPUPercent:        info.CpuPercent,
		RSSBytes:          info.RssBytes,
		ReadBytesPerSec:   info.ReadBytesPerSec,
		WriteBytesPerSec:  info.WriteBytesPerSec,
		User:              info.User,
		CommandSummary:    info.CommandSummary,
		UptimeSeconds:     info.UptimeSeconds,
		ThreadCount:       info.ThreadCount,
		SampledAt:         formatUnixMillis(info.SampledAtUnixMs),
		UnavailableReason: info.UnavailableReason,
	}
}

func formatUnixMillis(value int64) string {
	if value <= 0 {
		return ""
	}
	return time.UnixMilli(value).UTC().Format(time.RFC3339)
}

func (s *InstanceService) GetInstanceEnv(id uint) (*InstanceEnvData, error) {
	instance, err := s.GetByID(id)
	if err != nil {
		return nil, err
	}
	data := &InstanceEnvData{Configured: map[string]string{}}
	if strings.TrimSpace(instance.EnvVars) != "" {
		if err := json.Unmarshal([]byte(instance.EnvVars), &data.Configured); err != nil {
			data.Note = "解析已配置环境变量失败: " + err.Error()
			return data, nil
		}
	}
	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		data.Note = "查找节点失败"
		return data, nil
	}
	if node.Status != model.NodeStatusOnline {
		data.Note = "节点离线，无法读取运行时环境"
		return data, nil
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		data.Note = "节点未连接，无法读取运行时环境"
		return data, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Worker.GetInstanceEnv(ctx, &workerpb.GetInstanceEnvRequest{InstanceUuid: instance.UUID})
	if err != nil {
		data.Note = "读取运行时环境失败: " + err.Error()
		return data, nil
	}
	data.Runtime = resp.Env
	data.RuntimeAvailable = resp.Available
	if resp.Note != "" {
		data.Note = resp.Note
	}
	return data, nil
}

// preflightStart 启动前同步预检（FR-314）：查节点连通 → 确保实例已注册 → 调 Worker 预检 RPC。
// 节点未连接返回 ErrNodeOffline（HTTP 409）；预检未过返回 *PreflightError（HTTP 422）并写 statusReason；
// 老 Worker 无该 RPC（Unimplemented）则跳过预检、保持现有异步启动行为不变（向后兼容）。
func (s *InstanceService) preflightStart(instance *model.Instance) error {
	// 代理启动前置校验（FIX，真机复现 BungeeCord「No servers defined」崩溃）：
	// 无启用后端注册的代理直接启动，BungeeCord/Velocity 会在读到空 servers 段时抛
	// IllegalArgumentException: No servers defined 崩溃。此为纯 CP 侧 DB 校验，先于节点连通/Worker RPC，
	// 给出可直接修复的明确原因（HTTP 422），把「崩一圈才知道」提前为「启动前拦截」。
	if instance.Role == model.InstanceRoleProxy {
		var enabled int64
		if err := s.db.Model(&model.ServerRegistration{}).
			Where("proxy_id = ? AND enabled = ?", instance.ID, true).
			Count(&enabled).Error; err != nil {
			slog.Warn("代理启动预检：统计启用后端注册失败，跳过该项校验", "instanceId", instance.UUID, "error", err)
		} else if enabled == 0 {
			reason := "代理未注册任何启用的后端服务器：直接启动会因「No servers defined」崩溃。请先为该代理添加并启用至少一个后端子服，再启动。"
			s.updateStatusReasonOnly(instance.ID, reason)
			return &PreflightError{Reason: reason}
		}
	}

	var node model.Node
	if err := s.db.First(&node, instance.NodeID).Error; err != nil {
		return ErrNodeOffline
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ErrNodeOffline
	}

	if regErr := s.registerOnWorkerLocked(instance); regErr != nil {
		slog.Debug("预检前实例注册（已注册或失败均不阻断）", "instanceId", instance.UUID, "error", regErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.PreflightStartInstance(ctx, &workerpb.InstanceActionRequest{
		InstanceUuid: instance.UUID,
	})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			slog.Debug("Worker 未实现启动预检，跳过", "instanceId", instance.UUID)
			return nil
		}
		reason := "启动预检不可达: " + err.Error()
		s.updateStatusReasonOnly(instance.ID, reason)
		return &PreflightError{Reason: reason}
	}

	if resp.Success {
		return nil
	}

	reason := resp.Error
	if reason == "" {
		reason = "启动预检未通过"
	}
	s.updateStatusReasonOnly(instance.ID, reason)
	return &PreflightError{Reason: reason}
}

// updateStatusReasonOnly 仅更新 status_reason，不动 status（FR-314 预检失败：状态保持、不进 STARTING）。
func (s *InstanceService) updateStatusReasonOnly(id uint, reason string) {
	if err := s.db.Model(&model.Instance{}).Where("id = ?", id).
		Update("status_reason", reason).Error; err != nil {
		slog.Error("更新实例状态原因失败", "instanceId", id, "error", err)
	}
}
