package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 配置分批灰度与交付编排（FR-531）。
//
// ## 这个文件解决什么
//
// FR-458 的 `ConfigBaselineService.Converge` 是**循环逐台**：一次性推完全部实例，
// 中间出问题只能等它跑完再复盘。FR-531 要的是「一批一批放量、每批之后停下来看一眼，
// 不对就熔断、就回滚」。
//
// ## 两个最容易做错的地方
//
//  1. **离线语义**（§8）。Beacon 原设计把 agent 离线算 `failed` 并计入熔断率，
//     结果「两台机器正常重启」就能把整个变更打停。JM 把它拆成 `deferred`：
//     离线率 < 10% 时标 deferred、**不进失败率分母**、继续推在线目标、上线后自动补推；
//     ≥ 10% 才熔断（半个集群失联才是真异常信号）。
//  2. **落盘 ≠ 生效**（FR-532 §16）。目标同时记 `DiskWritten` 与生效结论，
//     因为「磁盘是新配置、内存是旧配置」这个状态重启就会变，必须能被人看见。
//
// ## 状态机
//
// 不引状态机库：转移表在 model/config_delivery.go 的 `deliveryOrderTransitions`，
// 手写表 + 显式校验点。理由是转移一共十来条，库引入的运行时解释层反而让
// 「哪些转移合法」变成不可 grep 的东西，而这张表正是本单的核心契约。

// 交付相关错误。
var (
	// ErrDeliveryNotFound 交付单不存在。
	ErrDeliveryNotFound = errors.New("配置交付单不存在")
	// ErrDeliveryTargetNotFound 交付目标不存在。
	ErrDeliveryTargetNotFound = errors.New("配置交付目标不存在")
	// ErrDeliveryStateInvalid 当前状态不允许该动作。
	ErrDeliveryStateInvalid = errors.New("配置交付单当前状态不允许该操作")
	// ErrDeliveryEmptyTarget 选择器解析后没有任何目标实例。
	ErrDeliveryEmptyTarget = errors.New("选择器未匹配到任何实例")
	// ErrDeliveryInvalid 入参校验失败。
	ErrDeliveryInvalid = errors.New("配置交付单参数非法")
)

// 离线率熔断阈值（spec §8.2：≥ 10% 熔断）。
const deliveryOfflineBreakPercent = 10

// 交付单熔断原因码（写入 break_reason，供审计与 UI 展示）。
const (
	// BreakReasonFailureRate 失败率触阈。
	BreakReasonFailureRate = "FAILURE_RATE"
	// BreakReasonUnhealthyRate 不健康率触阈。
	BreakReasonUnhealthyRate = "UNHEALTHY_RATE"
	// BreakReasonOfflineRate 离线率触阈（≥10%，保留 Beacon 的安全语义）。
	BreakReasonOfflineRate = "OFFLINE_RATE"
)

// 目标侧原因码（落 target.reason_code，机器可读）。
const (
	// TargetCodeOffline 目标实例离线 → deferred。
	TargetCodeOffline = "INSTANCE_OFFLINE"
	// TargetCodeRetryExhausted deferred 补推次数耗尽。
	TargetCodeRetryExhausted = "DEFERRED_RETRY_EXHAUSTED"
	// TargetCodeCircuitBreak 熔断导致未下发。
	TargetCodeCircuitBreak = "CIRCUIT_BREAK"
	// TargetCodeCancelled 人工终止导致未下发。
	TargetCodeCancelled = "CANCELLED"
	// TargetCodeNoChange 配置与当前版本一致，无需下发。
	TargetCodeNoChange = "NO_CHANGE"
	// TargetCodeMergeFailed 合并有效配置失败（层链/schema 问题）。
	TargetCodeMergeFailed = "EFFECTIVE_MERGE_FAILED"
	// TargetCodeWriteFailed 落盘失败（Worker 侧原因码透传在 reason 里）。
	TargetCodeWriteFailed = "WRITE_FAILED"
	// TargetCodeActivateFailed 落盘成功但生效失败（§16 的关键区分）。
	TargetCodeActivateFailed = "DISK_OK_ACTIVATE_FAILED"
	// TargetCodeRollbackFailed 回滚失败。
	TargetCodeRollbackFailed = "ROLLBACK_FAILED"
)

// systemActorName 审计里的系统 actor 标识（熔断等自动动作）。
const systemActorName = "system"

// DeliverySelector 目标实例选择器（spec §6 的 TargetSelector）。
//
// 各维度之间是 **AND**：先按分组子树 ∪ 显式 ID 求候选，再用标签/节点收敛。
// 全空不是「全部实例」而是**报错**——灰度变更误选中全量是灾难，
// 与 FR-535 的「不给隐式全量」同一口径。
type DeliverySelector struct {
	GroupIDs           []uint   `json:"groupIds,omitempty"`
	IncludeDescendants bool     `json:"includeDescendants"`
	InstanceIDs        []uint   `json:"instanceIds,omitempty"`
	Tags               []string `json:"tags,omitempty"`
	NodeIDs            []uint   `json:"nodeIds,omitempty"`
}

// Empty 判断选择器是否未指定任何维度。
func (s DeliverySelector) Empty() bool {
	return len(s.GroupIDs) == 0 && len(s.InstanceIDs) == 0 &&
		len(s.Tags) == 0 && len(s.NodeIDs) == 0
}

// ConfigDeliveryInput 创建/编辑交付单的入参。
type ConfigDeliveryInput struct {
	ConfigFileID uint `json:"configFileId"`
	// FilePath 落盘路径（相对实例工作目录）；为空时取 ConfigFile.Name。
	FilePath               string                        `json:"filePath"`
	TargetLayerLevel       model.ConfigScopeLevel        `json:"targetLayerLevel"`
	TargetLayerScopeID     uint                          `json:"targetLayerScopeId"`
	Selector               DeliverySelector              `json:"selector"`
	BatchMode              model.ConfigDeliveryBatchMode `json:"batchMode"`
	BatchSizes             []int                         `json:"batchSizes"`
	ObserveWindowSec       int                           `json:"observeWindowSec"`
	FailureRateThreshold   int                           `json:"failureRateThreshold"`
	UnhealthyRateThreshold int                           `json:"unhealthyRateThreshold"`
	ActivationMethod       model.ConfigActivationMethod  `json:"activationMethod"`
	ActivateTimeoutSec     int                           `json:"activateTimeoutSec"`
	ReloadTimeoutSec       int                           `json:"reloadTimeoutSec"`
	// AutoRetryDeferred 指针区分「不传（默认 true）」与「显式 false」。
	AutoRetryDeferred  *bool `json:"autoRetryDeferred,omitempty"`
	DeferredRetryLimit int   `json:"deferredRetryLimit"`
}

// DeliveryTargetView 目标的对外视图（含诚实文案）。
type DeliveryTargetView struct {
	model.ConfigDeliveryTarget
	// InstanceStatus 实例当前状态（实时读库，便于判断「离线」是不是临时状态）。
	InstanceStatus string `json:"instanceStatus,omitempty"`
}

// DeliveryImpact 影响预览（spec §11 的 impact）。
type DeliveryImpact struct {
	OrderID      uint `json:"orderId"`
	Planned      int  `json:"planned"`
	Online       int  `json:"online"`
	Offline      int  `json:"offline"`
	Changed      int  `json:"changed"`
	Unchanged    int  `json:"unchanged"`
	Undetermined int  `json:"undetermined"`
	// OfflineRate 离线率 %（批开始时的熔断判定依据）。
	OfflineRate float64 `json:"offlineRate"`
	// WouldCircuitBreak 按当前离线率是否会直接熔断。
	WouldCircuitBreak bool `json:"wouldCircuitBreak"`
	// Targets 逐台明细（上限 500，避免超大选择器把响应撑爆）。
	Targets []DeliveryImpactTarget `json:"targets"`
	// Truncated 明细是否被截断。
	Truncated bool `json:"truncated"`
}

// DeliveryImpactTarget 影响预览里的单台明细。
type DeliveryImpactTarget struct {
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName"`
	Online       bool   `json:"online"`
	Changed      bool   `json:"changed"`
	CurrentHash  string `json:"currentHash,omitempty"`
	TargetHash   string `json:"targetHash,omitempty"`
	Error        string `json:"error,omitempty"`
}

// DeliveryObserve 观察窗数据（spec §11 的 observe）。
//
// **只看我们真的知道的**：实例状态与探针连通性。TPS/玩家数等业务指标属于 spec §18
// 待定项 1，未定口径前不臆造字段。
type DeliveryObserve struct {
	OrderID      uint                       `json:"orderId"`
	Status       model.ConfigDeliveryStatus `json:"status"`
	CurrentBatch int                        `json:"currentBatch"`
	TotalBatches int                        `json:"totalBatches"`
	Thresholds   DeliveryObserveThresholds  `json:"thresholds"`
	Measured     DeliveryObserveMeasured    `json:"measured"`
	Batches      []DeliveryObserveBatch     `json:"batches"`
}

// DeliveryObserveThresholds 观察口径的阈值。
type DeliveryObserveThresholds struct {
	FailureRateThreshold   int `json:"failureRateThreshold"`
	UnhealthyRateThreshold int `json:"unhealthyRateThreshold"`
	OfflineBreakPercent    int `json:"offlineBreakPercent"`
	ObserveWindowSec       int `json:"observeWindowSec"`
}

// DeliveryObserveMeasured 实测口径。
type DeliveryObserveMeasured struct {
	Planned  int `json:"planned"`
	Failed   int `json:"failed"`
	Deferred int `json:"deferred"`
	// FailureRate 失败率 %，**分母排除 deferred**（§9.1）。
	FailureRate float64 `json:"failureRate"`
	// Unhealthy 已生效目标中实例当前非 RUNNING 的数量。
	Unhealthy int `json:"unhealthy"`
	// UnhealthyRate 不健康率 %（分母为已生效目标数）。
	UnhealthyRate float64 `json:"unhealthyRate"`
}

// DeliveryObserveBatch 逐批状态（批是记账单位，不是实体）。
type DeliveryObserveBatch struct {
	BatchNo   int    `json:"batchNo"`
	State     string `json:"state"`
	Total     int    `json:"total"`
	Activated int    `json:"activated"`
	Failed    int    `json:"failed"`
	Deferred  int    `json:"deferred"`
	Skipped   int    `json:"skipped"`
}

// deliveryWriteOutcome 单目标落盘结果。
//
// 为什么不复用 ConfigService.Write：那个方法把 error_code 折进 error 文本、并且不返回
// backup_path，而回滚链路必须知道「写前快照在哪」。这里保持同一张版本表、同一套 RPC，
// 只是把 Worker 的完整回执留下来。
type deliveryWriteOutcome struct {
	DiskWritten   bool
	Changed       bool
	ErrorCode     string
	ErrorMessage  string
	BackupPath    string
	BackupCreated bool
	ActualHash    string
	NewVersionID  *uint
	PrevVersionID *uint
}

// ConfigDeliveryService 配置分批灰度与交付编排（FR-531）。
type ConfigDeliveryService struct {
	db         *gorm.DB
	pool       *cpgrpc.ClientPool
	files      *ConfigFileService
	audit      *AuditService
	activation *ConfigActivationService

	// writeFn 落盘实现；默认走 Worker gRPC WriteConfig（含 FR-530 的读回校验）。
	writeFn func(inst *model.Instance, filePath, content, message string) deliveryWriteOutcome
	// sleepFn 观察窗等待原语；测试注入为 no-op。
	sleepFn func(time.Duration)
	clock   func() time.Time

	// cancelMu/cancelRequested 运行期取消标记（Cancel 只置标记，由 run 循环自行收敛，
	// 避免在库操作中途硬中断留下半个批次的状态）。
	cancelMu        sync.Mutex
	cancelRequested map[uint]bool

	// orderLocks 是交付单级的执行锁：Run 的放量循环与探针连入触发的补推可能同时
	// 操作同一张单的同一批目标。两边都对目标行做「读状态 → 改状态」，没有这把锁就会出现
	// 同一台实例被下发两次、以及 deferred 补推把刚被熔断跳过的目标又拉起来。
	// 用 keyedMutex 而非全局锁：不同交付单之间不应互相阻塞。
	orderLocks *deliveryOrderLocks
}

// deliveryOrderLocks 是交付单级的互斥表（与 Worker 侧 pathWriteLocks 同构）。
type deliveryOrderLocks struct {
	mu    sync.Mutex
	locks map[uint]*deliveryOrderLockEntry
}

// deliveryOrderLockEntry 单张单的锁与其引用计数（归零时删除，避免 map 无限增长）。
type deliveryOrderLockEntry struct {
	mu   sync.Mutex
	refs int
}

// lock 获取某交付单的排他锁并返回解锁函数。
func (l *deliveryOrderLocks) lock(orderID uint) func() {
	l.mu.Lock()
	entry, ok := l.locks[orderID]
	if !ok {
		entry = &deliveryOrderLockEntry{}
		l.locks[orderID] = entry
	}
	entry.refs++
	l.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		l.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(l.locks, orderID)
		}
		l.mu.Unlock()
	}
}

// NewConfigDeliveryService 创建配置交付服务。
func NewConfigDeliveryService(db *gorm.DB, pool *cpgrpc.ClientPool, files *ConfigFileService, activation *ConfigActivationService, audit *AuditService) *ConfigDeliveryService {
	return &ConfigDeliveryService{
		db:              db,
		pool:            pool,
		files:           files,
		audit:           audit,
		activation:      activation,
		sleepFn:         time.Sleep,
		clock:           time.Now,
		cancelRequested: map[uint]bool{},
		orderLocks:      &deliveryOrderLocks{locks: map[uint]*deliveryOrderLockEntry{}},
	}
}

// SetWriteFuncForTest 注入落盘实现（仅测试；生产走 Worker gRPC）。
func (s *ConfigDeliveryService) SetWriteFuncForTest(fn func(inst *model.Instance, filePath, content, message string) deliveryWriteOutcome) {
	s.writeFn = fn
}

// SetSleepForTest 注入等待原语（仅测试）。
func (s *ConfigDeliveryService) SetSleepForTest(fn func(d time.Duration)) {
	if fn != nil {
		s.sleepFn = fn
	}
}

// Activation 返回生效编排服务（路由与测试共用）。
func (s *ConfigDeliveryService) Activation() *ConfigActivationService { return s.activation }

// SetAudit 注入审计服务。装配期调用——审计服务与配置服务的构造顺序互不依赖，
// 用 setter 而不是把它塞进构造函数签名，避免为一个可选依赖改动所有调用点。
func (s *ConfigDeliveryService) SetAudit(audit *AuditService) { s.audit = audit }

// ---------------------------------------------------------------------------
// 创建 / 编辑 / 查询
// ---------------------------------------------------------------------------

// Create 创建交付单（draft）并解析目标。
func (s *ConfigDeliveryService) Create(in ConfigDeliveryInput, authorID uint) (*model.ConfigDeliveryOrder, error) {
	if s.files == nil {
		return nil, errors.New("配置分层服务未装配")
	}
	file, err := s.files.mustGet(in.ConfigFileID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeDeliveryInput(in, file)
	if err != nil {
		return nil, err
	}
	insts, err := s.resolveSelector(normalized.Selector)
	if err != nil {
		return nil, err
	}
	if len(insts) == 0 {
		return nil, ErrDeliveryEmptyTarget
	}
	selectorJSON, err := json.Marshal(normalized.Selector)
	if err != nil {
		return nil, fmt.Errorf("选择器序列化失败: %w", err)
	}
	batchJSON, err := json.Marshal(normalized.BatchSizes)
	if err != nil {
		return nil, fmt.Errorf("分批配置序列化失败: %w", err)
	}
	order := &model.ConfigDeliveryOrder{
		ConfigFileID:           normalized.ConfigFileID,
		FilePath:               normalized.FilePath,
		TargetLayerLevel:       normalized.TargetLayerLevel,
		TargetLayerScopeID:     normalized.TargetLayerScopeID,
		SelectorJSON:           string(selectorJSON),
		BatchMode:              normalized.BatchMode,
		BatchSizesJSON:         string(batchJSON),
		ObserveWindowSec:       normalized.ObserveWindowSec,
		FailureRateThreshold:   normalized.FailureRateThreshold,
		UnhealthyRateThreshold: normalized.UnhealthyRateThreshold,
		ActivationMethod:       normalized.ActivationMethod,
		ActivateTimeoutSec:     normalized.ActivateTimeoutSec,
		ReloadTimeoutSec:       normalized.ReloadTimeoutSec,
		AutoRetryDeferred:      boolValue(normalized.AutoRetryDeferred, true),
		DeferredRetryLimit:     normalized.DeferredRetryLimit,
		Status:                 model.DeliveryStatusDraft,
		CreatedBy:              authorID,
	}
	// 冻结当前层 head：交付的是「这一版」，之后该层再被改写也不影响本单。
	headID, prevID, err := s.layerHeads(order.ConfigFileID, order.TargetLayerLevel, order.TargetLayerScopeID)
	if err != nil {
		return nil, err
	}
	order.TargetLayerVersionID = headID
	order.FromLayerVersionID = prevID
	order.PlannedCount = len(insts)
	order.TotalBatches = len(computeBatchSizes(len(insts), normalized.BatchMode, normalized.BatchSizes))

	if err := s.db.Create(order).Error; err != nil {
		return nil, fmt.Errorf("创建配置交付单失败: %w", err)
	}
	if err := s.replaceTargets(order, insts); err != nil {
		return nil, err
	}
	return order, nil
}

// Update 编辑交付单（仅 draft）。
func (s *ConfigDeliveryService) Update(id uint, in ConfigDeliveryInput, authorID uint) (*model.ConfigDeliveryOrder, error) {
	order, err := s.mustGet(id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.DeliveryStatusDraft {
		return nil, fmt.Errorf("%w：只有草稿可以编辑（当前 %s）", ErrDeliveryStateInvalid, order.Status)
	}
	if s.files == nil {
		return nil, errors.New("配置分层服务未装配")
	}
	file, err := s.files.mustGet(in.ConfigFileID)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeDeliveryInput(in, file)
	if err != nil {
		return nil, err
	}
	insts, err := s.resolveSelector(normalized.Selector)
	if err != nil {
		return nil, err
	}
	if len(insts) == 0 {
		return nil, ErrDeliveryEmptyTarget
	}
	selectorJSON, _ := json.Marshal(normalized.Selector)
	batchJSON, _ := json.Marshal(normalized.BatchSizes)
	headID, prevID, err := s.layerHeads(order.ConfigFileID, normalized.TargetLayerLevel, normalized.TargetLayerScopeID)
	if err != nil {
		return nil, err
	}
	order.ConfigFileID = normalized.ConfigFileID
	order.FilePath = normalized.FilePath
	order.TargetLayerLevel = normalized.TargetLayerLevel
	order.TargetLayerScopeID = normalized.TargetLayerScopeID
	order.TargetLayerVersionID = headID
	order.FromLayerVersionID = prevID
	order.SelectorJSON = string(selectorJSON)
	order.BatchMode = normalized.BatchMode
	order.BatchSizesJSON = string(batchJSON)
	order.ObserveWindowSec = normalized.ObserveWindowSec
	order.FailureRateThreshold = normalized.FailureRateThreshold
	order.UnhealthyRateThreshold = normalized.UnhealthyRateThreshold
	order.ActivationMethod = normalized.ActivationMethod
	order.ActivateTimeoutSec = normalized.ActivateTimeoutSec
	order.ReloadTimeoutSec = normalized.ReloadTimeoutSec
	order.AutoRetryDeferred = boolValue(normalized.AutoRetryDeferred, true)
	order.DeferredRetryLimit = normalized.DeferredRetryLimit
	order.PlannedCount = len(insts)
	order.TotalBatches = len(computeBatchSizes(len(insts), normalized.BatchMode, normalized.BatchSizes))
	if err := s.db.Save(order).Error; err != nil {
		return nil, fmt.Errorf("保存配置交付单失败: %w", err)
	}
	if err := s.replaceTargets(order, insts); err != nil {
		return nil, err
	}
	return order, nil
}

// Delete 删除交付单（仅 draft）。
func (s *ConfigDeliveryService) Delete(id uint) error {
	order, err := s.mustGet(id)
	if err != nil {
		return err
	}
	if order.Status != model.DeliveryStatusDraft {
		return fmt.Errorf("%w：只有草稿可以删除（当前 %s）", ErrDeliveryStateInvalid, order.Status)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("order_id = ?", id).Delete(&model.ConfigDeliveryTarget{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ConfigDeliveryOrder{}, id).Error
	})
}

// Get 读取交付单详情。
func (s *ConfigDeliveryService) Get(id uint) (*model.ConfigDeliveryOrder, error) {
	return s.mustGet(id)
}

// List 分页列出交付单。
func (s *ConfigDeliveryService) List(status, keyword string, page, pageSize int) ([]model.ConfigDeliveryOrder, int64, error) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	q := s.db.Model(&model.ConfigDeliveryOrder{})
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}
	if kw := strings.TrimSpace(keyword); kw != "" {
		q = q.Where("file_path LIKE ?", "%"+kw+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.ConfigDeliveryOrder
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// Targets 分页列出目标（含实例实时状态）。
func (s *ConfigDeliveryService) Targets(orderID uint, status string, batchNo int, page, pageSize int) ([]DeliveryTargetView, int64, error) {
	if _, err := s.mustGet(orderID); err != nil {
		return nil, 0, err
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 50
	}
	q := s.db.Model(&model.ConfigDeliveryTarget{}).Where("order_id = ?", orderID)
	if st := strings.TrimSpace(status); st != "" {
		q = q.Where("status = ?", st)
	}
	if batchNo > 0 {
		q = q.Where("batch_no = ?", batchNo)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.ConfigDeliveryTarget
	if err := q.Order("batch_no asc, instance_id asc").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	views := make([]DeliveryTargetView, 0, len(rows))
	statuses := s.instanceStatuses(targetInstanceIDs(rows))
	for _, r := range rows {
		views = append(views, DeliveryTargetView{ConfigDeliveryTarget: r, InstanceStatus: string(statuses[r.InstanceID])})
	}
	return views, total, nil
}

// Impact 影响预览：解析选择器并逐台比对目标内容与当前版本。
func (s *ConfigDeliveryService) Impact(orderID uint) (*DeliveryImpact, error) {
	order, err := s.mustGet(orderID)
	if err != nil {
		return nil, err
	}
	var selector DeliverySelector
	if err := json.Unmarshal([]byte(order.SelectorJSON), &selector); err != nil {
		return nil, fmt.Errorf("选择器解析失败: %w", err)
	}
	insts, err := s.resolveSelector(selector)
	if err != nil {
		return nil, err
	}
	impact := &DeliveryImpact{OrderID: order.ID, Planned: len(insts)}
	const maxDetail = 500
	for _, inst := range insts {
		online := inst.Status == model.InstanceStatusRunning
		if online {
			impact.Online++
		} else {
			impact.Offline++
		}
		if len(impact.Targets) >= maxDetail {
			impact.Truncated = true
			continue
		}
		item := DeliveryImpactTarget{InstanceID: inst.ID, InstanceName: inst.Name, Online: online}
		_, hash, cerr := s.files.EffectiveContent(order.ConfigFileID, inst.ID)
		if cerr != nil {
			item.Error = cerr.Error()
			impact.Undetermined++
		} else {
			cur, has, _ := s.currentConfigHash(inst.ID, order.FilePath)
			item.CurrentHash = cur
			item.TargetHash = hash
			item.Changed = !has || cur != hash
			if item.Changed {
				impact.Changed++
			} else {
				impact.Unchanged++
			}
		}
		impact.Targets = append(impact.Targets, item)
	}
	impact.OfflineRate = offlineRatePercent(impact.Offline, impact.Planned)
	impact.WouldCircuitBreak = shouldBreakOnOffline(impact.Offline, impact.Planned)
	return impact, nil
}

// Observe 返回观察窗数据。
func (s *ConfigDeliveryService) Observe(orderID uint) (*DeliveryObserve, error) {
	order, err := s.mustGet(orderID)
	if err != nil {
		return nil, err
	}
	var rows []model.ConfigDeliveryTarget
	if err := s.db.Where("order_id = ?", orderID).Order("batch_no asc, instance_id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	counts := newDeliveryCounters(order.PlannedCount)
	statuses := s.instanceStatuses(targetInstanceIDs(rows))
	for _, r := range rows {
		counts.observe(r.Status)
		if r.Status == model.DeliveryTargetActivated {
			if statuses[r.InstanceID] != model.InstanceStatusRunning {
				counts.unhealthy++
			}
		}
	}
	out := &DeliveryObserve{
		OrderID:      order.ID,
		Status:       order.Status,
		CurrentBatch: order.CurrentBatch,
		TotalBatches: order.TotalBatches,
		Thresholds: DeliveryObserveThresholds{
			FailureRateThreshold:   order.FailureRateThreshold,
			UnhealthyRateThreshold: order.UnhealthyRateThreshold,
			OfflineBreakPercent:    deliveryOfflineBreakPercent,
			ObserveWindowSec:       order.ObserveWindowSec,
		},
		Measured: DeliveryObserveMeasured{
			Planned:       order.PlannedCount,
			Failed:        counts.failed,
			Deferred:      counts.deferred,
			FailureRate:   failureRatePercent(counts.failed, order.PlannedCount, counts.deferred),
			Unhealthy:     counts.unhealthy,
			UnhealthyRate: unhealthyRatePercent(counts.unhealthy, counts.activated),
		},
	}
	batchStates := decodeBatchStates(order.BatchStatesJSON)
	byBatch := map[int]*DeliveryObserveBatch{}
	for _, r := range rows {
		b := byBatch[r.BatchNo]
		if b == nil {
			b = &DeliveryObserveBatch{BatchNo: r.BatchNo, State: batchStates[r.BatchNo]}
			if b.State == "" {
				b.State = "pending"
			}
			byBatch[r.BatchNo] = b
		}
		b.Total++
		switch r.Status {
		case model.DeliveryTargetActivated:
			b.Activated++
		case model.DeliveryTargetFailed:
			b.Failed++
		case model.DeliveryTargetDeferred:
			b.Deferred++
		case model.DeliveryTargetSkipped:
			b.Skipped++
		}
	}
	keys := make([]int, 0, len(byBatch))
	for k := range byBatch {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		out.Batches = append(out.Batches, *byBatch[k])
	}
	if out.Batches == nil {
		out.Batches = []DeliveryObserveBatch{}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 状态推进
// ---------------------------------------------------------------------------

// Submit 提交审批：draft → pending_approval。
func (s *ConfigDeliveryService) Submit(id uint) (*model.ConfigDeliveryOrder, error) {
	return s.transit(id, model.DeliveryStatusPendingApproval, func(o *model.ConfigDeliveryOrder) error {
		if o.PlannedCount == 0 {
			return ErrDeliveryEmptyTarget
		}
		return nil
	})
}

// Approve 批准：pending_approval → approved。
//
// 说明：正式的审批入口应走平台审批中心（FR-490），本方法同时作为那条链路被执行器调用
// 的落点，以及「未接入审批中心时的直接批准」路径——不提供它，状态机就永远走不到 rolling。
func (s *ConfigDeliveryService) Approve(id, approverID uint) (*model.ConfigDeliveryOrder, error) {
	order, err := s.transit(id, model.DeliveryStatusApproved, nil)
	if err != nil {
		return nil, err
	}
	s.recordAudit(approverID, "config_delivery.approve", order.ID,
		fmt.Sprintf(`{"orderId":%d,"approverId":%d}`, order.ID, approverID))
	return order, nil
}

// Start 异步启动放量（approved → rolling）。
func (s *ConfigDeliveryService) Start(id uint) error {
	order, err := s.mustGet(id)
	if err != nil {
		return err
	}
	if order.Status != model.DeliveryStatusApproved {
		return fmt.Errorf("%w：只有已批准的交付单可以开始（当前 %s）", ErrDeliveryStateInvalid, order.Status)
	}
	if err := s.transitTo(order, model.DeliveryStatusRolling); err != nil {
		return err
	}
	go func() {
		if err := s.Run(id); err != nil {
			slog.Warn("配置交付执行失败", "orderId", id, "error", err)
		}
	}()
	return nil
}

// Pause 人工暂停（rolling/observing → paused）。
func (s *ConfigDeliveryService) Pause(id uint, reason string) (*model.ConfigDeliveryOrder, error) {
	order, err := s.transit(id, model.DeliveryStatusPaused, func(o *model.ConfigDeliveryOrder) error {
		o.PauseKind = string(model.DeliveryPauseManual)
		o.PauseReason = reason
		return nil
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// DeliveryResumeMode 继续模式（spec §11：retry_failed / skip_failed）。
type DeliveryResumeMode string

const (
	// ResumeRetryFailed 把失败目标重置为 pending 后重跑。
	ResumeRetryFailed DeliveryResumeMode = "retry_failed"
	// ResumeSkipFailed 保持失败目标不动，只继续未下发的。
	ResumeSkipFailed DeliveryResumeMode = "skip_failed"
)

// Resume 继续放量（paused → rolling）。
func (s *ConfigDeliveryService) Resume(id uint, mode DeliveryResumeMode) (*model.ConfigDeliveryOrder, error) {
	if mode != ResumeRetryFailed && mode != ResumeSkipFailed {
		return nil, fmt.Errorf("%w：mode 只支持 retry_failed / skip_failed", ErrDeliveryInvalid)
	}
	order, err := s.mustGet(id)
	if err != nil {
		return nil, err
	}
	if order.Status != model.DeliveryStatusPaused {
		return nil, fmt.Errorf("%w：只有暂停中的交付单可以继续（当前 %s）", ErrDeliveryStateInvalid, order.Status)
	}
	if mode == ResumeRetryFailed {
		// 失败目标重新排队：deferred 不动（它等的是目标上线，不是人点继续）。
		if err := s.db.Model(&model.ConfigDeliveryTarget{}).
			Where("order_id = ? AND status = ?", id, model.DeliveryTargetFailed).
			Updates(map[string]any{
				"status":       model.DeliveryTargetPending,
				"reason":       "",
				"reason_code":  "",
				"finished_at":  nil,
				"started_at":   nil,
				"disk_written": false,
			}).Error; err != nil {
			return nil, fmt.Errorf("重置失败目标失败: %w", err)
		}
	}
	order.PauseKind = ""
	order.PauseReason = ""
	if err := s.transitTo(order, model.DeliveryStatusRolling); err != nil {
		return nil, err
	}
	go func() {
		if err := s.Run(id); err != nil {
			slog.Warn("配置交付续跑失败", "orderId", id, "error", err)
		}
	}()
	return s.mustGet(id)
}

// Cancel 终止：未下发的目标置 skipped。
func (s *ConfigDeliveryService) Cancel(id uint, reason string) (*model.ConfigDeliveryOrder, error) {
	order, err := s.mustGet(id)
	if err != nil {
		return nil, err
	}
	// 终止允许从多个未终态出发，统一收敛到 cancelled。
	switch order.Status {
	case model.DeliveryStatusCancelled:
		return order, nil
	case model.DeliveryStatusRolledBack:
		return nil, fmt.Errorf("%w：已回滚的交付单不能终止", ErrDeliveryStateInvalid)
	case model.DeliveryStatusCompleted, model.DeliveryStatusFailed:
		// 已完成/已失败的单允许终止？没必要——它们是终态，直接拒绝，避免语义发散。
		return nil, fmt.Errorf("%w：交付单已终态（%s）", ErrDeliveryStateInvalid, order.Status)
	}
	s.requestCancel(id)
	now := s.clock()
	if err := s.db.Model(&model.ConfigDeliveryTarget{}).
		Where("order_id = ? AND status IN ?", id, []model.ConfigDeliveryTargetStatus{
			model.DeliveryTargetPending, model.DeliveryTargetPushing,
		}).
		Updates(map[string]any{
			"status":      model.DeliveryTargetSkipped,
			"reason":      fallbackActivateMsg(reason, "交付单被终止"),
			"reason_code": TargetCodeCancelled,
			"finished_at": &now,
		}).Error; err != nil {
		return nil, fmt.Errorf("标记跳过目标失败: %w", err)
	}
	order.Status = model.DeliveryStatusCancelled
	order.FinishedAt = &now
	order.PauseReason = reason
	if err := s.db.Save(order).Error; err != nil {
		return nil, err
	}
	_ = s.refreshCounters(order)
	return s.mustGet(id)
}

// ---------------------------------------------------------------------------
// 执行
// ---------------------------------------------------------------------------

// Run 同步执行放量循环（供 Start / Resume / 测试调用）。
//
// 串行推进每一批：批内逐台下发（不并发）。理由不是性能，而是**中止语义**：
// 熔断要求「批内未下发的目标置 skipped」，并发下「已决定下发但还没发」的目标无法停下，
// 会出现「熔断了但还在往外推」的窗口。
func (s *ConfigDeliveryService) Run(orderID uint) error {
	unlock := s.orderLocks.lock(orderID)
	defer unlock()

	order, err := s.mustGet(orderID)
	if err != nil {
		return err
	}
	if order.Status != model.DeliveryStatusRolling {
		return fmt.Errorf("%w：只有放量中的交付单可以执行（当前 %s）", ErrDeliveryStateInvalid, order.Status)
	}
	if order.StartedAt == nil {
		now := s.clock()
		order.StartedAt = &now
		_ = s.db.Model(order).Update("started_at", now).Error
	}

	instances, err := s.loadTargetInstances(order.ID)
	if err != nil {
		return err
	}

	for batchNo := 1; batchNo <= deliveryMaxInt(order.TotalBatches, 1); batchNo++ {
		if s.isCancelRequested(order.ID) {
			break
		}
		pending, loadErr := s.pendingTargets(order.ID, batchNo)
		if loadErr != nil {
			return loadErr
		}
		if len(pending) == 0 {
			continue // 该批已无待下发目标（续跑/跳过场景）
		}
		order.CurrentBatch = batchNo
		_ = s.db.Model(order).Update("current_batch", batchNo).Error

		// ① 每批开始时算离线率（§8.2）：≥10% 熔断，<10% 标 deferred 继续。
		offlineCount, err := s.countOfflinePending(order, instances)
		if err != nil {
			return err
		}
		if shouldBreakOnOffline(offlineCount, order.PlannedCount) {
			return s.triggerBreak(order, batchNo, BreakReasonOfflineRate,
				deliveryOfflineBreakPercent, offlineRatePercent(offlineCount, order.PlannedCount),
				fmt.Sprintf("离线率 %.1f%% 达到熔断阈值 %d%%（%d/%d 台离线）",
					offlineRatePercent(offlineCount, order.PlannedCount), deliveryOfflineBreakPercent, offlineCount, order.PlannedCount),
				"离线目标占比过高，疑似集群级故障，停止放量")
		}
		if err := s.deferOfflineTargets(order, batchNo, instances); err != nil {
			return err
		}

		// ② 逐台下发 + 生效；每次进入终态即时评估熔断（§9.2）。
		broken, err := s.runBatch(order, batchNo, instances)
		if err != nil {
			return err
		}
		if broken {
			return nil
		}
		s.markBatchState(order, batchNo, "completed")

		// ③ 观察窗：等一段时间看健康是否回归（§9 / §18 待定 1）。
		if order.ObserveWindowSec > 0 {
			if err := s.setStatus(order, model.DeliveryStatusObserving); err != nil {
				return err
			}
			s.sleepFn(time.Duration(order.ObserveWindowSec) * time.Second)
			if s.isCancelRequested(order.ID) {
				break
			}
			if broke, err := s.evaluateUnhealthyRate(order, batchNo); err != nil {
				return err
			} else if broke {
				return nil
			}
			if err := s.setStatus(order, model.DeliveryStatusRolling); err != nil {
				return err
			}
		}
	}

	// 收尾：根据目标终态决定 completed / failed / cancelled。
	if err := s.refreshCounters(order); err != nil {
		return err
	}
	now := s.clock()
	if s.isCancelRequested(order.ID) {
		order.Status = model.DeliveryStatusCancelled
	} else if order.FailedCount > 0 {
		order.Status = model.DeliveryStatusFailed
	} else {
		order.Status = model.DeliveryStatusCompleted
	}
	order.FinishedAt = &now
	// deferred 不是失败：全部推完但仍有人没收到时，单子仍是 completed，
	// 运维从 deferred_count 一眼看出「不是配置坏了，是目标不在」（§8.3）。
	if err := s.db.Save(order).Error; err != nil {
		return err
	}
	s.clearCancel(order.ID)
	return nil
}

// runBatch 执行一批，返回是否已熔断中止。
func (s *ConfigDeliveryService) runBatch(order *model.ConfigDeliveryOrder, batchNo int, instances map[uint]*model.Instance) (bool, error) {
	targets, err := s.pendingTargets(order.ID, batchNo)
	if err != nil {
		return false, err
	}
	for i := range targets {
		target := &targets[i]
		if s.isCancelRequested(order.ID) {
			return false, nil
		}
		broken, err := s.pushTarget(order, batchNo, target, instances[target.InstanceID])
		if err != nil {
			return false, err
		}
		if broken {
			return true, nil
		}
	}
	return false, nil
}

// pushTarget 对单台目标执行「落盘 → 生效 → 记账 → 熔断评估」。
//
// 顺序刻意是「先记账再评估熔断」：熔断必须看到刚进入终态的这一台，
// 否则「第 N 台失败导致超阈」要等下一台才反应，中间已经又推出去若干台。
func (s *ConfigDeliveryService) pushTarget(order *model.ConfigDeliveryOrder, batchNo int, target *model.ConfigDeliveryTarget, inst *model.Instance) (bool, error) {
	now := s.clock()
	startedAt := now
	if err := s.db.Model(target).Updates(map[string]any{
		"status":     model.DeliveryTargetPushing,
		"started_at": &startedAt,
	}).Error; err != nil {
		return false, err
	}

	if inst == nil {
		// 实例已被删除：算失败（目标不在库里 ≠ 短暂离线；等它上线是等不到的）。
		return s.finishTargetFailed(order, batchNo, target, TargetCodeWriteFailed, "实例不存在或已被删除")
	}
	if inst.Status != model.InstanceStatusRunning {
		// 走到这里说明批开始时的离线检查之后状态又变了（或该目标属于后续批）。
		return s.markTargetDeferred(order, target, "实例当前离线，等待上线后自动补推")
	}

	content, _, err := s.files.EffectiveContent(order.ConfigFileID, inst.ID)
	if err != nil {
		return s.finishTargetFailed(order, batchNo, target, TargetCodeMergeFailed,
			"合并有效配置失败："+err.Error())
	}
	message := fmt.Sprintf("配置交付 #%d（%s）", order.ID, order.ActivationMethod)
	outcome := s.writeConfig(inst, order.FilePath, content, message)
	if !outcome.DiskWritten {
		reason := outcome.ErrorMessage
		if outcome.ErrorCode != "" {
			reason = outcome.ErrorCode + ": " + reason
		}
		return s.finishTargetFailed(order, batchNo, target, TargetCodeWriteFailed,
			fallbackActivateMsg(reason, "落盘失败"))
	}

	// 落盘成功：先记「磁盘已是新配置」，再谈生效——两者必须能分开读。
	updates := map[string]any{
		"disk_written":        true,
		"backup_path":         outcome.BackupPath,
		"backup_present":      outcome.BackupCreated,
		"changed_file_count":  0,
		"new_version_id":      outcome.NewVersionID,
		"previous_version_id": outcome.PrevVersionID,
		"status":              model.DeliveryTargetPushed,
	}
	if outcome.Changed {
		updates["changed_file_count"] = 1
	}
	if err := s.db.Model(target).Updates(updates).Error; err != nil {
		return false, err
	}
	target.DiskWritten = true
	target.BackupPath = outcome.BackupPath
	target.BackupPresent = outcome.BackupCreated
	target.NewVersionID = outcome.NewVersionID
	target.PreviousVersionID = outcome.PrevVersionID

	if !outcome.Changed {
		// 内容与当前版本一致：无需生效，也不能谎称「已生效」。
		finish := s.clock()
		if err := s.db.Model(target).Updates(map[string]any{
			"status":             model.DeliveryTargetActivated,
			"reason":             "配置与当前版本一致，无需下发",
			"reason_code":        TargetCodeNoChange,
			"activate_confirmed": false,
			"finished_at":        &finish,
		}).Error; err != nil {
			return false, err
		}
		return s.afterTargetTerminal(order, batchNo, target)
	}

	// 生效阶段。
	if err := s.db.Model(target).Update("status", model.DeliveryTargetActivating).Error; err != nil {
		return false, err
	}
	act := s.activation.Activate(inst, order, order.FilePath)
	return s.applyActivationOutcome(order, batchNo, target, act)
}

// applyActivationOutcome 把生效结论如实落到目标行（FR-532 §16 的四象限）。
func (s *ConfigDeliveryService) applyActivationOutcome(order *model.ConfigDeliveryOrder, batchNo int, target *model.ConfigDeliveryTarget, act ActivationOutcome) (bool, error) {
	finish := s.clock()
	updates := map[string]any{
		"activate_confirmed": act.Confirmed,
		"best_effort":        act.BestEffort,
		"activation_message": act.Message,
		"receipt_json":       act.ReceiptJSON,
		"finished_at":        &finish,
	}
	switch {
	case act.Succeeded():
		// best effort 也记 activated（§14.1 明确如此），但 activate_confirmed=false +
		// best_effort=true + 诚实文案，UI 就不可能把它渲染成「已生效」。
		updates["status"] = model.DeliveryTargetActivated
		updates["reason"] = act.Message
		updates["reason_code"] = act.Code
	case act.TimedOut:
		// 用 act 自带的原因码而不是硬编码 TIMEOUT：restart 超时是 ACTIVATE_TIMEOUT
		// （等健康回归），与 hot_reload 的 TIMEOUT（等重载回执）是两件事，
		// 混成一个码会让排障时分不清「插件没回执」和「服务没起来」。
		code := act.Code
		if code == "" {
			code = ActivateCodeTimeout
		}
		updates["status"] = model.DeliveryTargetFailed
		updates["reason"] = act.Message
		updates["reason_code"] = code
	default:
		// **落盘成功但生效失败**：磁盘是新配置、内存是旧配置，重启就会变。必须单独可辨。
		updates["status"] = model.DeliveryTargetFailed
		updates["reason"] = fmt.Sprintf("落盘成功但生效失败：%s", act.Message)
		updates["reason_code"] = TargetCodeActivateFailed
	}
	if err := s.db.Model(target).Updates(updates).Error; err != nil {
		return false, err
	}
	// §17.8：审计必须同时记录落盘与生效两个状态，不能只写一个「失败」。
	s.recordAudit(0, "config_delivery.activate", order.ID, fmt.Sprintf(
		`{"orderId":%d,"instanceId":%d,"diskWritten":true,"activateConfirmed":%t,"bestEffort":%t,"timedOut":%t,"code":%q}`,
		order.ID, target.InstanceID, act.Confirmed, act.BestEffort, act.TimedOut, act.Code))
	return s.afterTargetTerminal(order, batchNo, target)
}

// finishTargetFailed 落盘阶段失败（磁盘没有新配置）。
func (s *ConfigDeliveryService) finishTargetFailed(order *model.ConfigDeliveryOrder, batchNo int, target *model.ConfigDeliveryTarget, code, reason string) (bool, error) {
	finish := s.clock()
	if err := s.db.Model(target).Updates(map[string]any{
		"status":      model.DeliveryTargetFailed,
		"reason":      reason,
		"reason_code": code,
		"finished_at": &finish,
	}).Error; err != nil {
		return false, err
	}
	return s.afterTargetTerminal(order, batchNo, target)
}

// markTargetDeferred 标 deferred：离线但不算失败（§8.2）。
func (s *ConfigDeliveryService) markTargetDeferred(order *model.ConfigDeliveryOrder, target *model.ConfigDeliveryTarget, reason string) (bool, error) {
	finish := s.clock()
	if err := s.db.Model(target).Updates(map[string]any{
		"status":      model.DeliveryTargetDeferred,
		"reason":      reason,
		"reason_code": TargetCodeOffline,
		"finished_at": &finish,
	}).Error; err != nil {
		return false, err
	}
	// deferred **不进失败率分母**，因此这里不需要（也不应该）触发熔断评估。
	return false, s.refreshCounters(order)
}

// afterTargetTerminal 目标进入终态后：刷新计数 + 即时评估熔断（§9.2）。
func (s *ConfigDeliveryService) afterTargetTerminal(order *model.ConfigDeliveryOrder, batchNo int, target *model.ConfigDeliveryTarget) (bool, error) {
	if err := s.refreshCounters(order); err != nil {
		return false, err
	}
	if order.FailureRateThreshold <= 0 {
		return false, nil
	}
	denom := order.PlannedCount - order.DeferredCount
	if denom <= 0 {
		return false, nil
	}
	rate := float64(order.FailedCount) / float64(denom) * 100
	if rate < float64(order.FailureRateThreshold) {
		return false, nil
	}
	reason := fmt.Sprintf("失败率 %.1f%% 达到熔断阈值 %d%%（失败 %d / 有效 %d，已排除 %d 台离线）",
		rate, order.FailureRateThreshold, order.FailedCount, denom, order.DeferredCount)
	if err := s.triggerBreak(order, batchNo, BreakReasonFailureRate,
		float64(order.FailureRateThreshold), rate, reason,
		"失败率超过阈值，已停止放量并保留后续批次"); err != nil {
		return false, err
	}
	return true, nil
}

// evaluateUnhealthyRate 观察窗结束后评估不健康率（§9 阈值之一）。
//
// 口径：**已生效目标的实例当前状态非 RUNNING** 视为不健康。这是这个阶段唯一可观测的
// 健康信号；TPS/玩家数等业务指标口径未定（spec §18 待定 1），不臆造。
func (s *ConfigDeliveryService) evaluateUnhealthyRate(order *model.ConfigDeliveryOrder, batchNo int) (bool, error) {
	if order.UnhealthyRateThreshold <= 0 {
		return false, nil
	}
	var rows []model.ConfigDeliveryTarget
	if err := s.db.Where("order_id = ? AND status = ?", order.ID, model.DeliveryTargetActivated).Find(&rows).Error; err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	statuses := s.instanceStatuses(targetInstanceIDs(rows))
	unhealthy := 0
	for _, r := range rows {
		if statuses[r.InstanceID] != model.InstanceStatusRunning {
			unhealthy++
		}
	}
	rate := unhealthyRatePercent(unhealthy, len(rows))
	if rate < float64(order.UnhealthyRateThreshold) {
		return false, nil
	}
	reason := fmt.Sprintf("不健康率 %.1f%% 达到熔断阈值 %d%%（%d/%d 台已生效目标当前非运行中）",
		rate, order.UnhealthyRateThreshold, unhealthy, len(rows))
	if err := s.triggerBreak(order, batchNo, BreakReasonUnhealthyRate,
		float64(order.UnhealthyRateThreshold), rate, reason,
		"观察窗内实例健康未回归，已停止放量"); err != nil {
		return false, err
	}
	return true, nil
}

// triggerBreak 执行熔断动作（§9.3）。
func (s *ConfigDeliveryService) triggerBreak(order *model.ConfigDeliveryOrder, batchNo int, reason string, threshold, observed float64, auditReason, skipReason string) error {
	now := s.clock()
	// ① 当前批置 failed。
	s.markBatchState(order, batchNo, "failed")
	// ② 批内未下发目标置 skipped（后续批保持 pending）。
	if err := s.db.Model(&model.ConfigDeliveryTarget{}).
		Where("order_id = ? AND batch_no = ? AND status IN ?", order.ID, batchNo,
			[]model.ConfigDeliveryTargetStatus{model.DeliveryTargetPending, model.DeliveryTargetPushing}).
		Updates(map[string]any{
			"status":      model.DeliveryTargetSkipped,
			"reason":      skipReason,
			"reason_code": TargetCodeCircuitBreak,
			"finished_at": &now,
		}).Error; err != nil {
		return err
	}
	// ③ 单置 paused（pause_kind=circuit_break）。
	order.Status = model.DeliveryStatusPaused
	order.PauseKind = string(model.DeliveryPauseCircuitBreak)
	order.PauseReason = auditReason
	order.BreakReason = reason
	order.BreakThreshold = threshold
	order.BreakObserved = observed
	order.FailedBatchNo = batchNo
	if err := s.db.Save(order).Error; err != nil {
		return err
	}
	if err := s.refreshCounters(order); err != nil {
		return err
	}
	// ④ 审计 actor=system + break_reason。
	s.recordAudit(0, "config_delivery.circuit_break", order.ID, fmt.Sprintf(
		`{"orderId":%d,"actor":%q,"batchNo":%d,"breakReason":%q,"threshold":%g,"observed":%g,"deferred":%d,"failed":%d,"planned":%d}`,
		order.ID, systemActorName, batchNo, reason, threshold, observed, order.DeferredCount, order.FailedCount, order.PlannedCount))
	slog.Warn("配置交付熔断", "orderId", order.ID, "batch", batchNo, "reason", reason,
		"threshold", threshold, "observed", observed)
	return nil
}

// ---------------------------------------------------------------------------
// 自动补推
// ---------------------------------------------------------------------------

// HandleProbeConnected 探针（重）连入时的补推钩子（§8.2）。
//
// 触发点是探针 `connected` 事件而不是心跳轮询：补推的价值在于「机器刚回来就把配置补上」，
// 而 connected 正是那一瞬间的信号；轮询只能更晚发现，且会为每台离线实例反复打库。
//
// 只对 rolling / observing / completed 的单补推。paused（含熔断）的单**不自动补推**：
// 那张单已经因为阈值被人为或自动叫停，此时再往外推就等于绕过熔断决策。
func (s *ConfigDeliveryService) HandleProbeConnected(instanceUUID string) {
	if strings.TrimSpace(instanceUUID) == "" {
		return
	}
	var inst model.Instance
	if err := s.db.Select("id", "uuid", "name", "status").Where("uuid = ?", instanceUUID).First(&inst).Error; err != nil {
		return
	}
	var orders []model.ConfigDeliveryOrder
	if err := s.db.Where("auto_retry_deferred = ? AND status IN ?", true,
		[]model.ConfigDeliveryStatus{
			model.DeliveryStatusRolling, model.DeliveryStatusObserving, model.DeliveryStatusCompleted,
		}).Find(&orders).Error; err != nil {
		return
	}
	for i := range orders {
		order := orders[i]
		var targets []model.ConfigDeliveryTarget
		if err := s.db.Where("order_id = ? AND instance_id = ? AND status = ?",
			order.ID, inst.ID, model.DeliveryTargetDeferred).Find(&targets).Error; err != nil {
			continue
		}
		for j := range targets {
			target := targets[j]
			limit := order.DeferredRetryLimit
			if limit <= 0 {
				limit = 3
			}
			if target.RetryCount >= limit {
				// 次数耗尽 → 标 failed（§8.2）。此后只有人工 resume 才会再看它。
				now := s.clock()
				_ = s.db.Model(&target).Updates(map[string]any{
					"status":      model.DeliveryTargetFailed,
					"reason":      fmt.Sprintf("离线补推已重试 %d 次仍未成功", target.RetryCount),
					"reason_code": TargetCodeRetryExhausted,
					"finished_at": &now,
				}).Error
				_ = s.refreshCounters(&order)
				continue
			}
			current := inst
			_, _ = s.retryDeferred(order, &target, &current)
		}
	}
}

// retryDeferred 对单台 deferred 目标补推一次。
func (s *ConfigDeliveryService) retryDeferred(order model.ConfigDeliveryOrder, target *model.ConfigDeliveryTarget, inst *model.Instance) (bool, error) {
	// 与 Run 共用同一把单级锁：避免补推与放量循环同时改同一台目标的两份状态。
	unlock := s.orderLocks.lock(order.ID)
	defer unlock()

	// 补推前重读实例状态：connected 事件到达时机可能略早于状态回写。
	var fresh model.Instance
	if err := s.db.Select("id", "uuid", "name", "status", "type", "role", "node_id", "work_dir").
		Where("id = ?", inst.ID).First(&fresh).Error; err != nil {
		return false, err
	}
	if fresh.Status != model.InstanceStatusRunning {
		return false, nil // 还没真正就绪，留给下一次事件
	}
	orderCopy := order
	now := s.clock()
	if err := s.db.Model(target).Updates(map[string]any{
		"status":      model.DeliveryTargetPushing,
		"retry_count": target.RetryCount + 1,
		"started_at":  &now,
		"reason":      "",
		"reason_code": "",
	}).Error; err != nil {
		return false, err
	}
	return s.pushTarget(&orderCopy, target.BatchNo, target, &fresh)
}

// ---------------------------------------------------------------------------
// 回滚
// ---------------------------------------------------------------------------

// RollbackOptions 回滚选项。
type RollbackOptions struct {
	// InstanceIDs 非空 = 目标级子集回滚（**配置版本不回退**，§10.2）。
	InstanceIDs []uint
	Reason      string
}

// DeliveryRollbackResult 回滚结果。
type DeliveryRollbackResult struct {
	OrderID            uint                           `json:"orderId"`
	RollbackID         uint                           `json:"rollbackId"`
	Scope              string                         `json:"scope"`
	Succeeded          int                            `json:"succeeded"`
	Failed             int                            `json:"failed"`
	LayerRolledBack    bool                           `json:"layerRolledBack"`
	RollbackOfRollback bool                           `json:"rollbackOfRollback"`
	Targets            []DeliveryRollbackTargetResult `json:"targets"`
}

// DeliveryRollbackTargetResult 单台回滚结果。
type DeliveryRollbackTargetResult struct {
	InstanceID uint   `json:"instanceId"`
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
}

// Rollback 回滚（§10）。
//
// 整批：每台还原快照 + 配置层版本回退 + 重新生效；
// 子集：只还原指定目标，**层版本不回退**；
// 再回滚：若单已 rolled_back / 目标已 rolled_back，本次动作是「回滚的回滚」——
// 把该目标写回本次交付的新版本内容并重新生效（§10.3）。
func (s *ConfigDeliveryService) Rollback(orderID uint, opts RollbackOptions, operatorID uint) (*DeliveryRollbackResult, error) {
	order, err := s.mustGet(orderID)
	if err != nil {
		return nil, err
	}
	if order.Status == model.DeliveryStatusRollingBack {
		return nil, fmt.Errorf("%w：该交付单正在回滚中", ErrDeliveryStateInvalid)
	}
	if order.Status != model.DeliveryStatusRolledBack && !order.Status.CanTransitTo(model.DeliveryStatusRollingBack) {
		return nil, fmt.Errorf("%w：%s 状态不允许回滚", ErrDeliveryStateInvalid, order.Status)
	}
	subset := len(opts.InstanceIDs) > 0
	scope := "full"
	if subset {
		scope = "subset"
	}
	// 回滚的回滚：单已回滚过，本次动作的目的是把它推回去。
	rollbackOfRollback := order.Status == model.DeliveryStatusRolledBack

	var targets []model.ConfigDeliveryTarget
	q := s.db.Where("order_id = ?", order.ID)
	if subset {
		q = q.Where("instance_id IN ?", opts.InstanceIDs)
	}
	if err := q.Order("instance_id asc").Find(&targets).Error; err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, ErrDeliveryTargetNotFound
	}

	prevStatus := order.Status
	order.Status = model.DeliveryStatusRollingBack
	if err := s.db.Save(order).Error; err != nil {
		return nil, err
	}

	result := &DeliveryRollbackResult{OrderID: order.ID, Scope: scope, RollbackOfRollback: rollbackOfRollback}
	instances := map[uint]*model.Instance{}
	for i := range targets {
		target := targets[i]
		inst, ok := instances[target.InstanceID]
		if !ok {
			var row model.Instance
			if err := s.db.First(&row, target.InstanceID).Error; err != nil {
				result.Failed++
				result.Targets = append(result.Targets, DeliveryRollbackTargetResult{
					InstanceID: target.InstanceID, Success: false, Error: "实例不存在",
				})
				continue
			}
			inst = &row
			instances[target.InstanceID] = inst
		}
		res := s.rollbackOne(order, &target, inst, rollbackOfRollback)
		if res.Success {
			result.Succeeded++
		} else {
			result.Failed++
		}
		result.Targets = append(result.Targets, res)
	}

	// 整批回滚才回退配置层版本（子集回滚保留版本，§10.2）。
	if !subset && !rollbackOfRollback && order.FromLayerVersionID != nil &&
		*order.FromLayerVersionID != 0 && s.files != nil {
		if _, lerr := s.files.RollbackLayer(order.ConfigFileID, order.TargetLayerLevel,
			order.TargetLayerScopeID, *order.FromLayerVersionID, operatorID,
			fmt.Sprintf("配置交付 #%d 回滚", order.ID)); lerr != nil {
			slog.Warn("回滚配置层版本失败", "orderId", order.ID, "error", lerr)
		} else {
			result.LayerRolledBack = true
		}
	}

	// 回滚记录（§10.3 的链条：这一轮回滚本身也能被后续回滚覆盖）。
	idsJSON, _ := json.Marshal(targetInstanceIDs(targets))
	rollback := &model.ConfigDeliveryRollback{
		OrderID: order.ID, Scope: scope, InstanceIDsJSON: string(idsJSON),
		LayerRolledBack:    result.LayerRolledBack,
		RollbackOfRollback: rollbackOfRollback,
		Reason:             opts.Reason,
		Succeeded:          result.Succeeded,
		Failed:             result.Failed,
		CreatedBy:          operatorID,
	}
	if rollbackOfRollback {
		var last model.ConfigDeliveryRollback
		if err := s.db.Where("order_id = ?", order.ID).Order("id desc").First(&last).Error; err == nil {
			rollback.RolledBackOrderID = &last.ID
		}
	}
	if err := s.db.Create(rollback).Error; err != nil {
		return nil, fmt.Errorf("记录回滚失败: %w", err)
	}
	result.RollbackID = rollback.ID

	now := s.clock()
	if result.Failed == 0 {
		order.Status = model.DeliveryStatusRolledBack
	} else if result.Succeeded > 0 {
		// 部分成功：仍按 rolled_back 收尾，但计数能看出有问题。
		order.Status = model.DeliveryStatusRolledBack
	} else {
		order.Status = prevStatus
		if prevStatus == model.DeliveryStatusRolledBack {
			order.Status = model.DeliveryStatusRolledBack
		}
	}
	if order.Status == model.DeliveryStatusRolledBack {
		order.FinishedAt = &now
	}
	if err := s.db.Save(order).Error; err != nil {
		return nil, err
	}
	s.recordAudit(operatorID, "config_delivery.rollback", order.ID, fmt.Sprintf(
		`{"orderId":%d,"scope":%q,"succeeded":%d,"failed":%d,"layerRolledBack":%t,"rollbackOfRollback":%t,"reason":%q}`,
		order.ID, scope, result.Succeeded, result.Failed, result.LayerRolledBack, rollbackOfRollback, opts.Reason))
	return result, nil
}

// rollbackOne 对单台目标执行还原 + 重新生效。
func (s *ConfigDeliveryService) rollbackOne(order *model.ConfigDeliveryOrder, target *model.ConfigDeliveryTarget, inst *model.Instance, rollbackOfRollback bool) DeliveryRollbackTargetResult {
	res := DeliveryRollbackTargetResult{InstanceID: target.InstanceID}
	if inst.Status != model.InstanceStatusRunning {
		// 离线实例无法还原（写不进去）。这里判失败而不是 deferred：回滚是人工介入动作，
		// 需要人知道「这几台没回成」，静默挂起会让磁盘与内存长期不一致。
		res.Error = "实例当前离线，无法回滚"
		return res
	}

	var content string
	var hasContent bool
	switch {
	case rollbackOfRollback:
		// 还原回滚前的状态 = 把本次交付写入的新版本内容再写回去。
		if target.NewVersionID != nil {
			var ver model.InstanceConfigVersion
			if err := s.db.First(&ver, *target.NewVersionID).Error; err == nil {
				content, hasContent = ver.Content, true
			}
		}
	default:
		// 优先用写前快照（spec §10.1：从 .jm-backup 找最近快照），
		// 快照缺失时回落到「写入前的实例配置版本」内容。
		if target.BackupPath != "" {
			if data, err := s.readInstanceFile(inst, target.BackupPath); err == nil {
				content, hasContent = string(data), true
			}
		}
		if !hasContent && target.PreviousVersionID != nil {
			var ver model.InstanceConfigVersion
			if err := s.db.First(&ver, *target.PreviousVersionID).Error; err == nil {
				content, hasContent = ver.Content, true
			}
		}
	}
	if !hasContent {
		res.Error = "找不到可还原的历史内容（无写前快照，也无历史版本）"
		return res
	}

	message := fmt.Sprintf("配置交付 #%d 回滚", order.ID)
	if rollbackOfRollback {
		message = fmt.Sprintf("配置交付 #%d 回滚的回滚", order.ID)
	}
	outcome := s.writeConfig(inst, order.FilePath, content, message)
	if !outcome.DiskWritten {
		res.Error = fallbackActivateMsg(outcome.ErrorCode, "回滚落盘失败") + ": " + outcome.ErrorMessage
		_ = s.db.Model(target).Updates(map[string]any{
			"reason":      "回滚失败：" + res.Error,
			"reason_code": TargetCodeRollbackFailed,
		}).Error
		return res
	}

	// 回滚同样要生效：磁盘改了而内存没改，重启才会变回旧配置。
	act := s.activation.Activate(inst, order, order.FilePath)
	updates := map[string]any{
		"rolled_back":          !rollbackOfRollback,
		"rollback_of_rollback": rollbackOfRollback,
		"activation_message":   act.Message,
		"activate_confirmed":   act.Confirmed,
		"best_effort":          act.BestEffort,
	}
	if rollbackOfRollback {
		updates["status"] = model.DeliveryTargetActivated
		updates["reason"] = "回滚已被回滚，恢复为本次交付的配置：" + act.Message
		updates["reason_code"] = "ROLLBACK_OF_ROLLBACK"
	} else {
		updates["status"] = model.DeliveryTargetPending
		updates["reason"] = "已回滚到变更前配置：" + act.Message
		updates["reason_code"] = "ROLLED_BACK"
	}
	if err := s.db.Model(target).Updates(updates).Error; err != nil {
		res.Error = err.Error()
		return res
	}
	res.Success = true
	return res
}

// ---------------------------------------------------------------------------
// 落盘
// ---------------------------------------------------------------------------

// writeConfig 落盘并记录版本（生产走 Worker gRPC，含 FR-530 的读回校验）。
func (s *ConfigDeliveryService) writeConfig(inst *model.Instance, filePath, content, message string) deliveryWriteOutcome {
	if s.writeFn != nil {
		return s.writeFn(inst, filePath, content, message)
	}
	return s.writeViaWorker(inst, filePath, content, message)
}

// writeViaWorker 直接调 Worker.WriteConfig：需要 error_code、backup_path、actual_hash
// 三个字段做回滚与诚实记账，而 ConfigService.Write 把它们折成了 error 文本。
//
// 版本表与 ConfigService.Write 完全一致（同一张 instance_config_versions），
// 因此两条写路径产生的数据在语义上没有分叉。
func (s *ConfigDeliveryService) writeViaWorker(inst *model.Instance, filePath, content, message string) deliveryWriteOutcome {
	out := deliveryWriteOutcome{}
	prevID := s.latestVersionID(inst.ID, filePath)
	out.PrevVersionID = prevID

	if s.pool == nil {
		out.ErrorCode = "NO_POOL"
		out.ErrorMessage = "未装配节点连接池"
		return out
	}
	var node model.Node
	if err := s.db.Select("uuid").First(&node, inst.NodeID).Error; err != nil || node.UUID == "" {
		out.ErrorCode = "NODE_NOT_FOUND"
		out.ErrorMessage = "节点不存在"
		return out
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		out.ErrorCode = "NODE_OFFLINE"
		out.ErrorMessage = "节点未连接"
		return out
	}
	// 内容未变化时不必写盘：既省一次写前快照，也避免制造无意义的历史版本。
	if prevID != nil {
		var prev model.InstanceConfigVersion
		if err := s.db.First(&prev, *prevID).Error; err == nil && prev.Content == content {
			out.DiskWritten = true
			out.Changed = false
			out.ActualHash = prev.ContentHash
			return out
		}
	}

	sum := sha256.Sum256([]byte(content))
	expected := hex.EncodeToString(sum[:])
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.WriteConfig(ctx, &workerpb.WriteConfigRequest{
		InstanceUuid: inst.UUID,
		Path:         filePath,
		Content:      content,
		ExpectedHash: expected,
	})
	if err != nil {
		out.ErrorCode = "WRITE_RPC_FAILED"
		out.ErrorMessage = err.Error()
		return out
	}
	out.BackupPath = resp.GetBackupPath()
	out.BackupCreated = resp.GetBackupCreated()
	out.ActualHash = resp.GetActualHash()
	if !resp.GetSuccess() {
		out.ErrorCode = fallbackActivateMsg(resp.GetErrorCode(), "WRITE_FAILED")
		out.ErrorMessage = resp.GetError()
		return out
	}
	ver := model.InstanceConfigVersion{
		InstanceID:  inst.ID,
		FilePath:    filePath,
		ContentHash: expected,
		Content:     content,
		Message:     message,
	}
	if err := s.db.Create(&ver).Error; err != nil {
		// 磁盘已写成功但版本没落库：必须报失败——回滚链会因此断掉。
		out.ErrorCode = "VERSION_SAVE_FAILED"
		out.ErrorMessage = err.Error()
		return out
	}
	out.DiskWritten = true
	out.Changed = true
	out.NewVersionID = &ver.ID
	return out
}

// readInstanceFile 读实例工作目录内某相对路径的文件（用于取回写前快照）。
func (s *ConfigDeliveryService) readInstanceFile(inst *model.Instance, relPath string) ([]byte, error) {
	if s.pool == nil {
		return nil, errors.New("未装配节点连接池")
	}
	var node model.Node
	if err := s.db.Select("uuid").First(&node, inst.NodeID).Error; err != nil || node.UUID == "" {
		return nil, errors.New("节点不存在")
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, errors.New("节点未连接")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := client.Worker.ReadFile(ctx, &workerpb.ReadFileRequest{
		InstanceUuid: inst.UUID,
		Path:         relPath,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetContent(), nil
}

// latestVersionID 取某实例某文件的最新版本 ID（无则 nil）。
func (s *ConfigDeliveryService) latestVersionID(instanceID uint, filePath string) *uint {
	var row model.InstanceConfigVersion
	if err := s.db.Where("instance_id = ? AND file_path = ?", instanceID, filePath).
		Order("id desc").First(&row).Error; err != nil {
		return nil
	}
	id := row.ID
	return &id
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

// normalizeDeliveryInput 校验并补默认值。
func normalizeDeliveryInput(in ConfigDeliveryInput, file *model.ConfigFile) (ConfigDeliveryInput, error) {
	out := in
	if file == nil {
		return out, fmt.Errorf("%w：配置文件不存在", ErrDeliveryInvalid)
	}
	if !model.ValidConfigScopeLevel(out.TargetLayerLevel) {
		return out, fmt.Errorf("%w：targetLayerLevel 只支持 group / instance", ErrDeliveryInvalid)
	}
	if out.TargetLayerScopeID == 0 {
		return out, fmt.Errorf("%w：targetLayerScopeId 不能为空", ErrDeliveryInvalid)
	}
	if strings.TrimSpace(out.FilePath) == "" {
		out.FilePath = file.Name
	}
	out.FilePath = strings.TrimSpace(out.FilePath)
	if out.FilePath == "" {
		return out, fmt.Errorf("%w：filePath 不能为空", ErrDeliveryInvalid)
	}
	if !model.ValidConfigDeliveryBatchMode(out.BatchMode) {
		return out, fmt.Errorf("%w：batchMode 只支持 percent / count", ErrDeliveryInvalid)
	}
	if len(out.BatchSizes) == 0 {
		// 不分批 = 一批全下（不是「非法」：小规模变更没必要分）。
		out.BatchSizes = []int{100}
		if out.BatchMode == model.DeliveryBatchCount {
			out.BatchSizes = nil
		}
	}
	for _, n := range out.BatchSizes {
		if n <= 0 {
			return out, fmt.Errorf("%w：batchSizes 必须为正数", ErrDeliveryInvalid)
		}
	}
	if out.BatchMode == model.DeliveryBatchPercent {
		sum := 0
		for _, n := range out.BatchSizes {
			sum += n
		}
		if sum > 100 {
			return out, fmt.Errorf("%w：percent 模式下 batchSizes 之和不能超过 100", ErrDeliveryInvalid)
		}
	}
	if !model.ValidConfigActivationMethod(out.ActivationMethod) {
		return out, fmt.Errorf("%w：activationMethod 只支持 hot_reload / restart / push_only", ErrDeliveryInvalid)
	}
	if out.ObserveWindowSec < 0 {
		return out, fmt.Errorf("%w：observeWindowSec 不能为负", ErrDeliveryInvalid)
	}
	if out.ObserveWindowSec == 0 {
		out.ObserveWindowSec = DefaultObserveWindowSec
	}
	if out.FailureRateThreshold < 0 || out.FailureRateThreshold > 100 {
		return out, fmt.Errorf("%w：failureRateThreshold 必须在 0..100", ErrDeliveryInvalid)
	}
	if out.UnhealthyRateThreshold < 0 || out.UnhealthyRateThreshold > 100 {
		return out, fmt.Errorf("%w：unhealthyRateThreshold 必须在 0..100", ErrDeliveryInvalid)
	}
	if out.ActivateTimeoutSec <= 0 {
		out.ActivateTimeoutSec = DefaultActivateTimeoutSec
	}
	if out.ReloadTimeoutSec <= 0 {
		out.ReloadTimeoutSec = DefaultReloadTimeoutSec
	}
	if out.DeferredRetryLimit <= 0 {
		out.DeferredRetryLimit = 3
	}
	if out.Selector.Empty() {
		return out, fmt.Errorf("%w：必须指定选择器（不支持隐式全量）", ErrDeliveryInvalid)
	}
	return out, nil
}

// resolveSelector 把选择器解析为实例集合（按 ID 升序，保证分批顺序稳定）。
func (s *ConfigDeliveryService) resolveSelector(sel DeliverySelector) ([]model.Instance, error) {
	if sel.Empty() {
		return nil, fmt.Errorf("%w：选择器为空", ErrDeliveryInvalid)
	}
	var all []model.Instance
	if err := s.db.Order("id asc").Find(&all).Error; err != nil {
		return nil, err
	}
	var groupInstanceIDs map[uint]struct{}
	if len(sel.GroupIDs) > 0 {
		subtree, err := s.groupSubtreeIDs(sel.GroupIDs, sel.IncludeDescendants)
		if err != nil {
			return nil, err
		}
		var members []model.InstanceGroupMember
		if err := s.db.Where("group_id IN ?", subtree).Find(&members).Error; err != nil {
			return nil, err
		}
		groupInstanceIDs = make(map[uint]struct{}, len(members))
		for _, m := range members {
			groupInstanceIDs[m.InstanceID] = struct{}{}
		}
	}
	explicit := make(map[uint]struct{}, len(sel.InstanceIDs))
	for _, id := range sel.InstanceIDs {
		explicit[id] = struct{}{}
	}
	nodes := make(map[uint]struct{}, len(sel.NodeIDs))
	for _, id := range sel.NodeIDs {
		nodes[id] = struct{}{}
	}
	tags := make(map[string]struct{}, len(sel.Tags))
	for _, t := range sel.Tags {
		if strings.TrimSpace(t) != "" {
			tags[strings.TrimSpace(t)] = struct{}{}
		}
	}

	out := make([]model.Instance, 0, len(all))
	for _, inst := range all {
		// 分组维度与显式 ID 维度是「并集」候选，标签/节点是「交集」收敛。
		if groupInstanceIDs != nil || len(explicit) > 0 {
			_, inGroup := groupInstanceIDs[inst.ID]
			_, inExplicit := explicit[inst.ID]
			if !inGroup && !inExplicit {
				continue
			}
		}
		if len(nodes) > 0 {
			if _, ok := nodes[inst.NodeID]; !ok {
				continue
			}
		}
		if len(tags) > 0 && !instanceHasAnyTag(&inst, tags) {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

// instanceHasAnyTag 判断实例是否带有任一给定标签。
func instanceHasAnyTag(inst *model.Instance, tags map[string]struct{}) bool {
	for _, t := range model.ParseTags(inst.Tags) {
		if _, ok := tags[t]; ok {
			return true
		}
	}
	return false
}

// groupSubtreeIDs 返回给定分组的自身（+ 后代）ID 集合。
func (s *ConfigDeliveryService) groupSubtreeIDs(roots []uint, includeDescendants bool) ([]uint, error) {
	set := make(map[uint]struct{}, len(roots))
	for _, r := range roots {
		set[r] = struct{}{}
	}
	if includeDescendants {
		var nodes []model.InstanceGroupNode
		if err := s.db.Find(&nodes).Error; err != nil {
			return nil, err
		}
		children := map[uint][]uint{}
		for _, n := range nodes {
			if n.ParentID != nil {
				children[*n.ParentID] = append(children[*n.ParentID], n.ID)
			}
		}
		queue := append([]uint(nil), roots...)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, c := range children[cur] {
				if _, seen := set[c]; seen {
					continue
				}
				set[c] = struct{}{}
				queue = append(queue, c)
			}
		}
	}
	out := make([]uint, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// layerHeads 取目标层的当前 head 与上一版（用于冻结与回滚）。
func (s *ConfigDeliveryService) layerHeads(configFileID uint, level model.ConfigScopeLevel, scopeID uint) (head uint, prev *uint, err error) {
	if s.files == nil {
		return 0, nil, errors.New("配置分层服务未装配")
	}
	history, herr := s.files.LayerHistory(configFileID, level, scopeID)
	if herr != nil {
		return 0, nil, herr
	}
	if len(history) == 0 {
		return 0, nil, fmt.Errorf("%w：该作用域还没有任何版本，无法交付", ErrDeliveryInvalid)
	}
	// 上一版作为整批回滚的版本回退目标；只有一版时为空（没有可回退的历史）。
	if len(history) > 1 {
		id := history[1].ID
		prev = &id
	}
	return history[0].ID, prev, nil
}

// replaceTargets 重建目标行（仅用于 draft 阶段）。分批号在此一次性分配。
func (s *ConfigDeliveryService) replaceTargets(order *model.ConfigDeliveryOrder, insts []model.Instance) error {
	var sizes []int
	_ = json.Unmarshal([]byte(order.BatchSizesJSON), &sizes)
	batches := assignBatches(len(insts), order.BatchMode, sizes)
	order.TotalBatches = len(computeBatchSizes(len(insts), order.BatchMode, sizes))
	order.PlannedCount = len(insts)
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("order_id = ?", order.ID).Delete(&model.ConfigDeliveryTarget{}).Error; err != nil {
			return err
		}
		for i, inst := range insts {
			row := model.ConfigDeliveryTarget{
				OrderID:      order.ID,
				InstanceID:   inst.ID,
				InstanceName: inst.Name,
				BatchNo:      batches[i],
				Status:       model.DeliveryTargetPending,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return tx.Model(order).Updates(map[string]any{
			"planned_count": order.PlannedCount,
			"total_batches": order.TotalBatches,
		}).Error
	})
}

// computeBatchSizes 把分批配置换算成每批台数。
//
// percent：n_i = total * size_i / 100（向下取整），余数并入最后一批；
// count：直接用绝对值，不足部分并入最后一批；
// 空：单批全量。
func computeBatchSizes(total int, mode model.ConfigDeliveryBatchMode, sizes []int) []int {
	if total <= 0 {
		return nil
	}
	if len(sizes) == 0 {
		return []int{total}
	}
	out := make([]int, 0, len(sizes))
	remaining := total
	for i, n := range sizes {
		last := i == len(sizes)-1
		var take int
		if mode == model.DeliveryBatchPercent {
			take = total * n / 100
		} else {
			take = n
		}
		if last {
			take = remaining
		}
		if take > remaining {
			take = remaining
		}
		if take <= 0 && !last {
			// 极小比例在向下取整后可能为 0：跳过空的中间批，避免造出无意义的批号。
			continue
		}
		out = append(out, take)
		remaining -= take
		if remaining <= 0 {
			break
		}
	}
	if len(out) == 0 {
		out = []int{total}
	}
	return out
}

// assignBatches 为每个目标分配批号（1 起）。
func assignBatches(total int, mode model.ConfigDeliveryBatchMode, sizes []int) []int {
	out := make([]int, total)
	batchNo := 1
	idx := 0
	for _, size := range computeBatchSizes(total, mode, sizes) {
		for i := 0; i < size && idx < total; i++ {
			out[idx] = batchNo
			idx++
		}
		batchNo++
	}
	for ; idx < total; idx++ {
		out[idx] = batchNo
	}
	return out
}

// pendingTargets 返回某批待下发的目标（deferred 不在其中：等补推而不是重跑批）。
func (s *ConfigDeliveryService) pendingTargets(orderID uint, batchNo int) ([]model.ConfigDeliveryTarget, error) {
	var rows []model.ConfigDeliveryTarget
	err := s.db.Where("order_id = ? AND batch_no = ? AND status = ?", orderID, batchNo, model.DeliveryTargetPending).
		Order("instance_id asc").Find(&rows).Error
	return rows, err
}

// countOfflinePending 统计「仍要交付但当前不可达」的目标数（§8.2 的分子）。
//
// 口径是 pending + deferred，而不是只管 pending：
//   - 只看 pending 的话，第 1 批把离线目标标成 deferred 之后，第 2 批再算离线率
//     分子就变成 0 了——「半个集群失联」这个信号会在第二批凭空消失，
//     后续批会照常放量，正是 §8.2 想避免的。
//   - 把 deferred 一并计入，离线率在整个变更期间是稳定的，熔断判定在任何一批都成立。
func (s *ConfigDeliveryService) countOfflinePending(order *model.ConfigDeliveryOrder, instances map[uint]*model.Instance) (int, error) {
	var rows []model.ConfigDeliveryTarget
	if err := s.db.Select("instance_id").
		Where("order_id = ? AND status IN ?", order.ID,
			[]model.ConfigDeliveryTargetStatus{model.DeliveryTargetPending, model.DeliveryTargetDeferred}).
		Find(&rows).Error; err != nil {
		return 0, err
	}
	offline := 0
	for _, r := range rows {
		inst := instances[r.InstanceID]
		// 实例已被删除也算「不可达」：它同样不该让在线目标跟着停摆，
		// 也不该在失败率里被当成配置写坏。
		if inst == nil || inst.Status != model.InstanceStatusRunning {
			offline++
		}
	}
	return offline, nil
}

// deferOfflineTargets 把**当前批**待下发但离线的目标标 deferred（离线率未触阈时）。
//
// 只处理当前批：后续批的目标保持 pending，等轮到它们时再各自判定。
// 一次性把全量离线目标都标掉会让「后续批保持 pending」这条 §9.3 语义失效，
// 也会让批次进度看起来像「全都跳过了」。
func (s *ConfigDeliveryService) deferOfflineTargets(order *model.ConfigDeliveryOrder, batchNo int, instances map[uint]*model.Instance) error {
	var rows []model.ConfigDeliveryTarget
	if err := s.db.Where("order_id = ? AND batch_no = ? AND status = ?", order.ID, batchNo, model.DeliveryTargetPending).
		Find(&rows).Error; err != nil {
		return err
	}
	now := s.clock()
	for _, r := range rows {
		inst := instances[r.InstanceID]
		if inst != nil && inst.Status == model.InstanceStatusRunning {
			continue
		}
		if err := s.db.Model(&model.ConfigDeliveryTarget{}).Where("id = ?", r.ID).Updates(map[string]any{
			"status":      model.DeliveryTargetDeferred,
			"reason":      "实例当前离线，等待上线后自动补推",
			"reason_code": TargetCodeOffline,
			"finished_at": &now,
		}).Error; err != nil {
			return err
		}
	}
	return s.refreshCounters(order)
}

// refreshCounters 从目标表重算计数（不靠增量自增，避免任何一次漏记永久偏移）。
func (s *ConfigDeliveryService) refreshCounters(order *model.ConfigDeliveryOrder) error {
	type agg struct {
		Status string
		N      int
	}
	var rows []agg
	err := s.db.Model(&model.ConfigDeliveryTarget{}).
		Select("status, count(*) as n").
		Where("order_id = ?", order.ID).
		Group("status").Scan(&rows).Error
	if err != nil {
		return err
	}
	counts := struct {
		pushed, activated, failed, deferred, skipped int
	}{}
	for _, r := range rows {
		switch model.ConfigDeliveryTargetStatus(r.Status) {
		case model.DeliveryTargetPushed, model.DeliveryTargetActivating:
			counts.pushed += r.N
		case model.DeliveryTargetActivated:
			counts.activated += r.N
		case model.DeliveryTargetFailed:
			counts.failed += r.N
		case model.DeliveryTargetDeferred:
			counts.deferred += r.N
		case model.DeliveryTargetSkipped:
			counts.skipped += r.N
		}
	}
	order.PushedCount = counts.pushed + counts.activated + counts.failed
	order.ActivatedCount = counts.activated
	order.FailedCount = counts.failed
	order.DeferredCount = counts.deferred
	order.SkippedCount = counts.skipped
	return s.db.Model(&model.ConfigDeliveryOrder{}).Where("id = ?", order.ID).Updates(map[string]any{
		"pushed_count":    order.PushedCount,
		"activated_count": order.ActivatedCount,
		"failed_count":    order.FailedCount,
		"deferred_count":  order.DeferredCount,
		"skipped_count":   order.SkippedCount,
	}).Error
}

// loadTargetInstances 一次读入本单涉及的全部实例（避免逐台查库）。
func (s *ConfigDeliveryService) loadTargetInstances(orderID uint) (map[uint]*model.Instance, error) {
	var rows []model.ConfigDeliveryTarget
	if err := s.db.Select("instance_id").Where("order_id = ?", orderID).Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := targetInstanceIDs(rows)
	out := make(map[uint]*model.Instance, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var insts []model.Instance
	if err := s.db.Where("id IN ?", ids).Find(&insts).Error; err != nil {
		return nil, err
	}
	for i := range insts {
		out[insts[i].ID] = &insts[i]
	}
	return out, nil
}

// instanceStatuses 批量读实例状态。
func (s *ConfigDeliveryService) instanceStatuses(ids []uint) map[uint]model.InstanceStatus {
	out := make(map[uint]model.InstanceStatus, len(ids))
	if len(ids) == 0 {
		return out
	}
	var rows []model.Instance
	if err := s.db.Select("id", "status").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.ID] = r.Status
	}
	return out
}

// currentConfigHash 取实例某文件的当前内容哈希。
func (s *ConfigDeliveryService) currentConfigHash(instanceID uint, filePath string) (string, bool, error) {
	var row model.InstanceConfigVersion
	err := s.db.Where("instance_id = ? AND file_path = ?", instanceID, filePath).
		Order("id desc").First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return row.ContentHash, true, nil
}

// markBatchState 记录批终态（批不是实体，挂在单上）。
func (s *ConfigDeliveryService) markBatchState(order *model.ConfigDeliveryOrder, batchNo int, state string) {
	states := decodeBatchStates(order.BatchStatesJSON)
	states[batchNo] = state
	if b, err := json.Marshal(states); err == nil {
		order.BatchStatesJSON = string(b)
		_ = s.db.Model(order).Update("batch_states_json", order.BatchStatesJSON).Error
	}
}

// decodeBatchStates 解析逐批状态 JSON。
func decodeBatchStates(raw string) map[int]string {
	out := map[int]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// transit 校验并执行状态转移。
func (s *ConfigDeliveryService) transit(id uint, to model.ConfigDeliveryStatus, mutate func(*model.ConfigDeliveryOrder) error) (*model.ConfigDeliveryOrder, error) {
	order, err := s.mustGet(id)
	if err != nil {
		return nil, err
	}
	from := order.Status
	if from != to && !from.CanTransitTo(to) {
		return nil, fmt.Errorf("%w：%s → %s 不是合法转移", ErrDeliveryStateInvalid, from, to)
	}
	if mutate != nil {
		if err := mutate(order); err != nil {
			return nil, err
		}
	}
	order.Status = to
	if err := s.db.Save(order).Error; err != nil {
		return nil, err
	}
	return order, nil
}

// transitTo 在已加载的 order 上做转移校验并保存。
func (s *ConfigDeliveryService) transitTo(order *model.ConfigDeliveryOrder, to model.ConfigDeliveryStatus) error {
	if order.Status != to && !order.Status.CanTransitTo(to) {
		return fmt.Errorf("%w：%s → %s 不是合法转移", ErrDeliveryStateInvalid, order.Status, to)
	}
	order.Status = to
	return s.db.Save(order).Error
}

// setStatus 仅改状态字段。
func (s *ConfigDeliveryService) setStatus(order *model.ConfigDeliveryOrder, to model.ConfigDeliveryStatus) error {
	if order.Status == to {
		return nil
	}
	if !order.Status.CanTransitTo(to) {
		return fmt.Errorf("%w：%s → %s 不是合法转移", ErrDeliveryStateInvalid, order.Status, to)
	}
	order.Status = to
	return s.db.Model(order).Update("status", to).Error
}

// mustGet 读取交付单。
func (s *ConfigDeliveryService) mustGet(id uint) (*model.ConfigDeliveryOrder, error) {
	var order model.ConfigDeliveryOrder
	if err := s.db.First(&order, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDeliveryNotFound
		}
		return nil, err
	}
	return &order, nil
}

// requestCancel 置运行期取消标记。
func (s *ConfigDeliveryService) requestCancel(orderID uint) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	s.cancelRequested[orderID] = true
}

// clearCancel 清除取消标记（交付单进入终态后调用）。
func (s *ConfigDeliveryService) clearCancel(orderID uint) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	delete(s.cancelRequested, orderID)
}

// isCancelRequested 读取取消标记。
func (s *ConfigDeliveryService) isCancelRequested(orderID uint) bool {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	return s.cancelRequested[orderID]
}

// ClearCancelForTest 清除取消标记（仅测试）。
func (s *ConfigDeliveryService) ClearCancelForTest(orderID uint) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	delete(s.cancelRequested, orderID)
}

// recordAudit 写审计（失败不阻断主流程）。userID=0 表示系统动作。
func (s *ConfigDeliveryService) recordAudit(userID uint, action string, orderID uint, detail string) {
	if s.audit == nil {
		return
	}
	s.audit.RecordSafe(userID, action, "config_delivery", strconv.FormatUint(uint64(orderID), 10), detail, "")
}

// targetInstanceIDs 取出目标行的实例 ID。
func targetInstanceIDs(rows []model.ConfigDeliveryTarget) []uint {
	out := make([]uint, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.InstanceID)
	}
	return out
}

// offlineRatePercent 离线率 %（分母为计划数）。
func offlineRatePercent(offline, planned int) float64 {
	if planned <= 0 {
		return 0
	}
	return float64(offline) / float64(planned) * 100
}

// shouldBreakOnOffline 离线率是否达到熔断阈值（≥10%）。
func shouldBreakOnOffline(offline, planned int) bool {
	if planned <= 0 {
		return false
	}
	return offlineRatePercent(offline, planned) >= float64(deliveryOfflineBreakPercent)
}

// failureRatePercent 失败率 %，**分母排除 deferred**（§9.1）。
func failureRatePercent(failed, planned, deferred int) float64 {
	denom := planned - deferred
	if denom <= 0 {
		return 0
	}
	return float64(failed) / float64(denom) * 100
}

// unhealthyRatePercent 不健康率 %。
func unhealthyRatePercent(unhealthy, activated int) float64 {
	if activated <= 0 {
		return 0
	}
	return float64(unhealthy) / float64(activated) * 100
}

// deliveryCounters 观察数据聚合用的小结构。
type deliveryCounters struct {
	planned              int
	failed, deferred     int
	activated, unhealthy int
}

func newDeliveryCounters(planned int) *deliveryCounters {
	return &deliveryCounters{planned: planned}
}

// observe 按目标状态累加。
func (c *deliveryCounters) observe(status model.ConfigDeliveryTargetStatus) {
	switch status {
	case model.DeliveryTargetFailed:
		c.failed++
	case model.DeliveryTargetDeferred:
		c.deferred++
	case model.DeliveryTargetActivated:
		c.activated++
	}
}

// boolValue 解引用布尔指针，nil 时取默认值。
func boolValue(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// maxInt 取较大值。
func deliveryMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
