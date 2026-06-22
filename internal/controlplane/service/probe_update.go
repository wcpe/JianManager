package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 探针在线更新（FR-068，见 ADR-016）：把 CP 内嵌的最新 ServerProbe jar 经已有 gRPC
// DeployServerProbe（FR-010/ADR-014）推到实例 plugins 目录，覆盖在位 jar。jar 是 JVM 已加载
// 的 class 来源，热替换不生效，故语义为「已就位，下次重启生效」；可选 restart=true 推送后重启使其立即生效。
//
// 不改 proto、不改子模块：复用 DeployServerProbe 与建服时的探针 config 构造（metrics + bridge 段）。

const (
	// maxProbeUpdateTargets 单次批量更新目标数上限，与实例批量（FR-058）对齐。
	maxProbeUpdateTargets = 5000
	// probeUpdateConcurrency 批量更新的有界并发度。
	probeUpdateConcurrency = 16
	// maxProbeUpdateErrors 批量结果回传的失败明细上限。
	maxProbeUpdateErrors = 100
	// probeDeployTimeout 单实例 jar 下发的 gRPC 超时（jar 约 1MB，给足余量）。
	probeDeployTimeout = 30 * time.Second
)

// ErrProbeNotEmbedded 表示 CP 未内嵌探针 jar（未跑 make embed-probe），无可推送内容。
var ErrProbeNotEmbedded = errors.New("控制平面未内嵌 ServerProbe 探针 jar，无法推送更新")

// ProbeConnChecker 查询某实例的探针是否经插件桥反向 WS 连入（FR-065/066）。
// 生产环境由 PlayerEventService.IsProbeConnected 注入；为 nil 时一律视为未连入（仅影响展示）。
type ProbeConnChecker func(instanceUUID string) bool

// ProbeUpdateService 探针在线更新服务（FR-068）。
// 复用 InstanceService 的 gRPC 客户端池与 DB，批量更新走同步路径以精确计数（镜像 InstanceBatchService）。
type ProbeUpdateService struct {
	db     *gorm.DB
	pool   *cpgrpc.ClientPool
	bridge *PluginBridgeService
	// connCheck 注入探针连接状态查询（FR-065/066 插件桥会话），nil 表示一律未连入。
	connCheck ProbeConnChecker

	// lastPushed 记录每实例「上次经本服务推送探针」的时间（CP 进程内内存态，重启清空）。
	mu         sync.RWMutex
	lastPushed map[string]time.Time // key: 实例 UUID
}

// NewProbeUpdateService 创建探针在线更新服务。
// bridge 用于推送时重新生成探针 config 的 bridge 段（实例级 token，与建服一致）；可为 nil
// （此时 config 不含 bridge 段，探针只跑 /metrics、不连反向 WS）。
func NewProbeUpdateService(db *gorm.DB, pool *cpgrpc.ClientPool, bridge *PluginBridgeService) *ProbeUpdateService {
	return &ProbeUpdateService{
		db:         db,
		pool:       pool,
		bridge:     bridge,
		lastPushed: make(map[string]time.Time),
	}
}

// SetConnChecker 注入探针连接状态查询（FR-066 在线名册）。在 main 装配阶段调用，避免服务间循环依赖。
func (s *ProbeUpdateService) SetConnChecker(c ProbeConnChecker) {
	s.connCheck = c
}

// ProbeUpdateStatus 某实例的探针更新状态（供详情页「更新探针」区展示）。
type ProbeUpdateStatus struct {
	InstanceID          uint       `json:"instanceId"`
	InstanceUUID        string     `json:"instanceUuid"`
	ProbeConnected      bool       `json:"probeConnected"`
	EmbeddedVersion     string     `json:"embeddedVersion"`
	EmbeddedFingerprint string     `json:"embeddedFingerprint"`
	EmbeddedAvailable   bool       `json:"embeddedAvailable"`
	LastPushedAt        *time.Time `json:"lastPushedAt"`
}

// ProbeUpdateResult 单实例推送结果。
type ProbeUpdateResult struct {
	InstanceID          uint   `json:"instanceId"`
	Deployed            bool   `json:"deployed"`
	Restarted           bool   `json:"restarted"`
	ProbeConnected      bool   `json:"probeConnected"`
	EmbeddedVersion     string `json:"embeddedVersion"`
	EmbeddedFingerprint string `json:"embeddedFingerprint"`
	Message             string `json:"message"`
}

// ProbeUpdateBatchFilter 批量更新目标筛选条件（复用实例批量的筛选语义）。
type ProbeUpdateBatchFilter = InstanceBatchFilter

// ProbeUpdateBatchRequest 批量更新请求。目标由 IDs 或 Filter 二选一指定。
type ProbeUpdateBatchRequest struct {
	IDs     []uint
	Filter  *ProbeUpdateBatchFilter
	Restart bool
}

// ProbeUpdateBatchError 批量更新单条失败明细。
type ProbeUpdateBatchError struct {
	InstanceID uint   `json:"instanceId"`
	Error      string `json:"error"`
}

// ProbeUpdateBatchResult 批量更新结果计数。
type ProbeUpdateBatchResult struct {
	Requested int                     `json:"requested"`
	Succeeded int                     `json:"succeeded"`
	Failed    int                     `json:"failed"`
	Skipped   int                     `json:"skipped"`
	Errors    []ProbeUpdateBatchError `json:"errors"`
}

// EmbeddedInfo 返回内嵌探针 jar 的元信息（版本 + 短指纹 + 是否内嵌）。
func (s *ProbeUpdateService) EmbeddedInfo() cpembed.ProbeJarInfo {
	return cpembed.ServerProbeJarInfo()
}

// Status 返回某实例的探针更新状态。实例不存在返回 gorm.ErrRecordNotFound。
func (s *ProbeUpdateService) Status(instanceID uint) (*ProbeUpdateStatus, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		return nil, err
	}
	info := cpembed.ServerProbeJarInfo()
	return &ProbeUpdateStatus{
		InstanceID:          inst.ID,
		InstanceUUID:        inst.UUID,
		ProbeConnected:      s.isConnected(inst.UUID),
		EmbeddedVersion:     info.Version,
		EmbeddedFingerprint: info.Fingerprint,
		EmbeddedAvailable:   info.Available,
		LastPushedAt:        s.lastPushedAt(inst.UUID),
	}, nil
}

// Update 把内嵌最新探针 jar 推到指定实例的 plugins 目录（下次重启生效）。
// restart=true 时推送成功后由调用方重启（本服务只标记 restarted 计划，实际重启委托 restartFn，
// 见 router 装配）。jar 未内嵌返回 ErrProbeNotEmbedded；节点未连/下发失败返回包装错误。
func (s *ProbeUpdateService) Update(instanceID uint) (*ProbeUpdateResult, error) {
	info := cpembed.ServerProbeJarInfo()
	if !info.Available {
		return nil, ErrProbeNotEmbedded
	}
	var inst model.Instance
	if err := s.db.Preload("Node").First(&inst, instanceID).Error; err != nil {
		return nil, err
	}
	if err := s.deployTo(&inst); err != nil {
		return nil, err
	}
	s.markPushed(inst.UUID)
	return &ProbeUpdateResult{
		InstanceID:          inst.ID,
		Deployed:            true,
		ProbeConnected:      s.isConnected(inst.UUID),
		EmbeddedVersion:     info.Version,
		EmbeddedFingerprint: info.Fingerprint,
		Message:             "探针 jar 已就位，下次重启生效",
	}, nil
}

// Batch 批量推送：解析目标 → 资源隔离收敛 → 有界并发同步推送 → 计数（镜像 FR-058）。
// jar 未内嵌时整体拒绝（ErrProbeNotEmbedded）。restart 由调用方在成功项上各自异步重启。
// 返回结果中 Succeeded 列表通过 onDeployed 回调暴露给调用方（用于 restart）。
func (s *ProbeUpdateService) Batch(req ProbeUpdateBatchRequest, scopeIDs []uint, scope bool, onDeployed func(inst *model.Instance)) (*ProbeUpdateBatchResult, error) {
	if !cpembed.ServerProbeJarInfo().Available {
		return nil, ErrProbeNotEmbedded
	}
	instances, skipped, err := s.resolveTargets(req, scopeIDs, scope)
	if err != nil {
		return nil, err
	}
	if len(instances) > maxProbeUpdateTargets {
		return nil, fmt.Errorf("批量目标数 %d 超过上限 %d", len(instances), maxProbeUpdateTargets)
	}

	result := &ProbeUpdateBatchResult{
		Requested: len(instances),
		Skipped:   skipped,
		Errors:    []ProbeUpdateBatchError{},
	}
	if len(instances) == 0 {
		return result, nil
	}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, probeUpdateConcurrency)
	)
	for i := range instances {
		inst := instances[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			derr := s.deployTo(&inst)

			mu.Lock()
			if derr != nil {
				result.Failed++
				if len(result.Errors) < maxProbeUpdateErrors {
					result.Errors = append(result.Errors, ProbeUpdateBatchError{InstanceID: inst.ID, Error: derr.Error()})
				}
			} else {
				result.Succeeded++
				s.markPushed(inst.UUID)
				if onDeployed != nil {
					onDeployed(&inst)
				}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	return result, nil
}

// resolveTargets 解析批量目标实例（预加载节点），按可访问实例集合收敛。
// 返回 (目标实例列表, skipped)。skipped 为请求 IDs 中不存在或越权被剔除的数量（存在性隐藏）。
func (s *ProbeUpdateService) resolveTargets(req ProbeUpdateBatchRequest, scopeIDs []uint, scope bool) ([]model.Instance, int, error) {
	var instances []model.Instance

	if len(req.IDs) > 0 {
		q := applyInstanceBatchFilter(s.db.Model(&model.Instance{}).Preload("Node"), InstanceBatchFilter{}, scopeIDs, scope)
		if err := q.Where("instances.id IN ?", req.IDs).Find(&instances).Error; err != nil {
			return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
		}
		skipped := len(req.IDs) - len(instances)
		if skipped < 0 {
			skipped = 0
		}
		return instances, skipped, nil
	}

	f := InstanceBatchFilter{}
	if req.Filter != nil {
		f = *req.Filter
	}
	q := applyInstanceBatchFilter(s.db.Model(&model.Instance{}).Preload("Node"), f, scopeIDs, scope)
	if err := q.Limit(maxProbeUpdateTargets + 1).Find(&instances).Error; err != nil {
		return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
	}
	return instances, 0, nil
}

// deployTo 把内嵌探针 jar + 重新生成的探针 config 经 gRPC DeployServerProbe 推到实例所属 Worker。
// 探针未连入也照常推送（jar 落盘即生效于下次重启）；节点未连/下发失败返回包装错误。
func (s *ProbeUpdateService) deployTo(inst *model.Instance) error {
	if inst.Node.UUID == "" {
		return fmt.Errorf("实例 %d 缺少关联节点", inst.ID)
	}
	jar := cpembed.ServerProbeJar()
	if len(jar) == 0 {
		return ErrProbeNotEmbedded
	}
	client, ok := s.pool.Get(inst.Node.UUID)
	if !ok {
		return fmt.Errorf("Worker %s 未连接", inst.Node.UUID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), probeDeployTimeout)
	defer cancel()

	resp, err := client.Worker.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
		InstanceUuid: inst.UUID,
		Jar:          jar,
		ConfigYaml:   buildServerProbeConfig(inst.ProbePort, s.bridgeBlock(inst.UUID, inst.Node.WSPort)),
	})
	if err != nil {
		return fmt.Errorf("gRPC DeployServerProbe 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker 部署探针失败: %s", resp.Error)
	}
	return nil
}

// bridgeBlock 为实例签发插件桥 token 并生成探针 config.yml 的 bridge 段（FR-065，与建服一致）。
// bridge 服务未注入或签发失败时返回空串（探针不连反向 WS，/metrics 不受影响）。
func (s *ProbeUpdateService) bridgeBlock(instanceUUID string, wsPort int) string {
	if s.bridge == nil {
		return ""
	}
	token, err := s.bridge.IssueToken(instanceUUID)
	if err != nil {
		slog.Warn("更新探针时签发插件桥 token 失败（探针将不连反向 WS）", "instance", instanceUUID, "err", err)
		return ""
	}
	return s.bridge.BuildBridgeConfigBlock(pluginBridgeWSURL(wsPort), instanceUUID, token)
}

func (s *ProbeUpdateService) isConnected(instanceUUID string) bool {
	if s.connCheck == nil {
		return false
	}
	return s.connCheck(instanceUUID)
}

func (s *ProbeUpdateService) markPushed(instanceUUID string) {
	s.mu.Lock()
	s.lastPushed[instanceUUID] = time.Now()
	s.mu.Unlock()
}

func (s *ProbeUpdateService) lastPushedAt(instanceUUID string) *time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if t, ok := s.lastPushed[instanceUUID]; ok {
		tt := t
		return &tt
	}
	return nil
}
