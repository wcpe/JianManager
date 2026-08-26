package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 探针在线更新（FR-068/409，见 ADR-016）：把 CP 已缓存版本的下载引用经已有 gRPC
// DeployServerProbe 推到实例 plugins 目录。jar 是 JVM 已加载的 class 来源，热替换不生效，
// 故语义为「已就位，下次重启生效」；可选 restart=true 推送后重启使其立即生效。

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

// ErrProbeNotEmbedded 保留旧错误名以兼容调用方；现在表示 CP 没有可下发的制品版本库。
var ErrProbeNotEmbedded = errors.New("控制平面没有已缓存并选中的 ServerProbe 制品版本，无法推送更新")

// ProbeConnChecker 查询某实例的探针是否经插件桥反向 WS 连入（FR-065/066）。
// 生产环境由 PlayerEventService.IsProbeConnected 注入；为 nil 时一律视为未连入（仅影响展示）。
type ProbeConnChecker func(instanceUUID string) bool

// ProbeUpdateService 探针在线更新服务（FR-068）。
// 复用 InstanceService 的 gRPC 客户端池与 DB，批量更新走同步路径以精确计数（镜像 InstanceBatchService）。
type ProbeUpdateService struct {
	db        *gorm.DB
	pool      *cpgrpc.ClientPool
	bridge    *PluginBridgeService
	artifacts *ArtifactVersionService
	// connCheck 注入探针连接状态查询（FR-065/066 插件桥会话），nil 表示一律未连入。
	connCheck ProbeConnChecker

	// lastPushed 记录每实例「上次经本服务推送探针」的时间（CP 进程内内存态，重启清空）。
	mu         sync.RWMutex
	lastPushed map[string]time.Time // key: 实例 UUID
}

// NewProbeUpdateService 创建探针在线更新服务。
// bridge 用于推送时重新生成探针 config 的 bridge 段（实例级 token，与建服一致）；可为 nil
// （此时 config 不含 bridge 段，探针只跑 /metrics、不连反向 WS）。
func NewProbeUpdateService(db *gorm.DB, pool *cpgrpc.ClientPool, bridge *PluginBridgeService, artifacts ...*ArtifactVersionService) *ProbeUpdateService {
	var artifactSvc *ArtifactVersionService
	if len(artifacts) > 0 {
		artifactSvc = artifacts[0]
	}
	return &ProbeUpdateService{
		db:         db,
		pool:       pool,
		bridge:     bridge,
		artifacts:  artifactSvc,
		lastPushed: make(map[string]time.Time),
	}
}

// SetConnChecker 注入探针连接状态查询（FR-066 在线名册）。在 main 装配阶段调用，避免服务间循环依赖。
func (s *ProbeUpdateService) SetConnChecker(c ProbeConnChecker) {
	s.connCheck = c
}

// ProbeUpdateStatus 某实例的探针更新状态（供详情页「更新探针」区展示）。
type ProbeUpdateStatus struct {
	InstanceID          uint               `json:"instanceId"`
	InstanceUUID        string             `json:"instanceUuid"`
	ProbeConnected      bool               `json:"probeConnected"`
	EmbeddedVersion     string             `json:"embeddedVersion"`
	EmbeddedFingerprint string             `json:"embeddedFingerprint"`
	EmbeddedAvailable   bool               `json:"embeddedAvailable"`
	LibrariesAvailable  bool               `json:"librariesAvailable"`
	LibrariesBytes      int                `json:"librariesBytes"`
	LibrariesShortSha   string             `json:"librariesShortSha"`
	VersionID           uint               `json:"versionId"`
	Version             string             `json:"version"`
	VersionSHA256       string             `json:"versionSha256"`
	VersionOrigin       ProbeVersionOrigin `json:"versionOrigin"`
	VersionError        string             `json:"versionError,omitempty"`
	LastPushedAt        *time.Time         `json:"lastPushedAt"`
}

// ProbeUpdateResult 单实例推送结果。
type ProbeUpdateResult struct {
	InstanceID          uint   `json:"instanceId"`
	Deployed            bool   `json:"deployed"`
	Restarted           bool   `json:"restarted"`
	ProbeConnected      bool   `json:"probeConnected"`
	EmbeddedVersion     string `json:"embeddedVersion"`
	EmbeddedFingerprint string `json:"embeddedFingerprint"`
	LibrariesAvailable  bool   `json:"librariesAvailable"`
	LibrariesBytes      int    `json:"librariesBytes"`
	LibrariesShortSha   string `json:"librariesShortSha"`
	VersionID           uint   `json:"versionId"`
	Version             string `json:"version"`
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

// Status 返回某实例的探针更新状态。实例不存在返回 gorm.ErrRecordNotFound。
func (s *ProbeUpdateService) Status(instanceID uint) (*ProbeUpdateStatus, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		return nil, err
	}
	result := &ProbeUpdateStatus{
		InstanceID:     inst.ID,
		InstanceUUID:   inst.UUID,
		ProbeConnected: s.isConnected(inst.UUID),
		LastPushedAt:   s.lastPushedAt(inst.UUID),
	}
	if s.artifacts == nil {
		result.VersionError = "制品版本库未配置"
		return result, nil
	}
	version, origin, err := s.artifacts.ResolveInstanceProbeVersion(inst.ID)
	if err != nil {
		result.VersionError = err.Error()
		return result, nil
	}
	result.VersionID = version.ID
	result.Version = version.Version
	result.VersionSHA256 = version.ExpectedSHA256
	result.VersionOrigin = origin
	result.EmbeddedVersion = version.Version
	result.EmbeddedFingerprint = version.ExpectedSHA256
	result.EmbeddedAvailable = true
	result.LibrariesAvailable = false
	result.LibrariesBytes = 0
	result.LibrariesShortSha = ""
	return result, nil
}

// Update 把已解析探针版本推到指定实例的 plugins 目录（下次重启生效）。
// restart=true 时推送成功后由调用方重启（本服务只标记 restarted 计划，实际重启委托 restartFn，
// 见 router 装配）。未选已缓存版本返回 ErrProbeNotEmbedded；节点未连/下发失败返回包装错误。
func (s *ProbeUpdateService) Update(instanceID uint) (*ProbeUpdateResult, error) {
	return s.UpdateWithBaseURL(instanceID, "")
}

// UpdateWithBaseURL 把解析后的版本引用下发给 Worker；Worker 从 CP 本地 CAS 拉 jar。
func (s *ProbeUpdateService) UpdateWithBaseURL(instanceID uint, baseURL string) (*ProbeUpdateResult, error) {
	var inst model.Instance
	if err := s.db.Preload("Node").First(&inst, instanceID).Error; err != nil {
		return nil, err
	}
	// 代理实例不适用探针（FIX，真机：对 BungeeCord 代理点「更新探针」走完整推送链路后在依赖预置阶段失败）：
	// ServerProbe 是 Bukkit 插件，代理端（BungeeCord/Waterfall/Velocity）无法加载，推了也白推。
	// 置于版本解析之前：不适用与是否已选探针版本无关。
	if inst.Role == model.InstanceRoleProxy {
		return nil, fmt.Errorf("代理实例不适用 ServerProbe 探针（Bukkit 插件，代理端无法加载），无需推送")
	}
	if s.artifacts != nil {
		version, _, err := s.artifacts.ResolveInstanceProbeVersion(inst.ID)
		if err != nil {
			return nil, err
		}
		if err := s.deployVersionTo(&inst, version, baseURL); err != nil {
			return nil, err
		}
		s.markPushed(inst.UUID)
		return &ProbeUpdateResult{
			InstanceID:          inst.ID,
			Deployed:            true,
			ProbeConnected:      s.isConnected(inst.UUID),
			EmbeddedVersion:     version.Version,
			EmbeddedFingerprint: version.ExpectedSHA256,
			VersionID:           version.ID,
			Version:             version.Version,
			Message:             "探针 jar 已就位，下次重启生效",
		}, nil
	}
	return nil, ErrProbeNotEmbedded
}

// Batch 批量推送：解析目标 → 资源隔离收敛 → 有界并发同步推送 → 计数（镜像 FR-058）。
// 未选已缓存版本时整体拒绝（ErrProbeNotEmbedded）。restart 由调用方在成功项上各自异步重启。
// 返回结果中 Succeeded 列表通过 onDeployed 回调暴露给调用方（用于 restart）。
func (s *ProbeUpdateService) Batch(req ProbeUpdateBatchRequest, scopeIDs []uint, scope bool, onDeployed func(inst *model.Instance)) (*ProbeUpdateBatchResult, error) {
	return s.BatchWithBaseURL(req, scopeIDs, scope, "", onDeployed)
}

// BatchWithBaseURL 按同一 CP 基址并发下发版本引用；每个 Worker 获得独立短期下载 token。
func (s *ProbeUpdateService) BatchWithBaseURL(req ProbeUpdateBatchRequest, scopeIDs []uint, scope bool, baseURL string, onDeployed func(inst *model.Instance)) (*ProbeUpdateBatchResult, error) {
	if s.artifacts == nil {
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

			derr := s.deployTarget(&inst, baseURL)

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

func (s *ProbeUpdateService) deployTarget(inst *model.Instance, baseURL string) error {
	if s.artifacts == nil {
		return ErrProbeNotEmbedded
	}
	version, _, err := s.artifacts.ResolveInstanceProbeVersion(inst.ID)
	if err != nil {
		return err
	}
	return s.deployVersionTo(inst, version, baseURL)
}

// resolveTargets 解析批量目标实例（预加载节点），按可访问实例集合收敛。
// 返回 (目标实例列表, skipped)。skipped 为请求 IDs 中不存在或越权被剔除的数量（存在性隐藏）。
func (s *ProbeUpdateService) resolveTargets(req ProbeUpdateBatchRequest, scopeIDs []uint, scope bool) ([]model.Instance, int, error) {
	var instances []model.Instance

	if len(req.IDs) > 0 {
		q := applyInstanceBatchFilter(s.db.Model(&model.Instance{}).Preload("Node"), InstanceBatchFilter{}, scopeIDs, scope)
		// 代理实例不适用探针（Bukkit 插件），批量目标静默跳过并计入 skipped。
		if err := q.Where("instances.id IN ? AND instances.role <> ?", req.IDs, model.InstanceRoleProxy).Find(&instances).Error; err != nil {
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
	if err := q.Where("instances.role <> ?", model.InstanceRoleProxy).Limit(maxProbeUpdateTargets + 1).Find(&instances).Error; err != nil {
		return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
	}
	return instances, 0, nil
}

// deployVersionTo 下发已缓存制品的 CP 本地 URL；不传 jar 字节或运行库压缩包。
func (s *ProbeUpdateService) deployVersionTo(inst *model.Instance, version *model.ArtifactVersion, baseURL string) error {
	client, err := s.workerForDeployment(inst)
	if err != nil {
		return err
	}
	token, err := s.artifacts.IssueProbeDownloadToken(ProbeDownloadTokenScope{VersionID: version.ID, NodeUUID: inst.Node.UUID})
	if err != nil {
		return err
	}
	downloadURL, err := s.artifacts.BuildProbeDownloadURL(baseURL, version.ID, token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeDeployTimeout)
	defer cancel()
	resp, err := client.Worker.DeployServerProbe(ctx, buildDeployServerProbeDownloadRequest(
		inst.UUID,
		downloadURL,
		version.ExpectedSHA256,
		version.Version,
		buildServerProbeConfig(inst.ProbePort, s.bridgeBlock(inst.UUID, inst.Node.WSPort)),
	))
	if err != nil {
		return fmt.Errorf("gRPC DeployServerProbe 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("Worker 部署探针失败: %s", resp.Error)
	}
	return nil
}

func (s *ProbeUpdateService) workerForDeployment(inst *model.Instance) (*cpgrpc.Client, error) {
	if inst.Node.UUID == "" {
		return nil, fmt.Errorf("实例 %d 缺少关联节点", inst.ID)
	}
	// 节点离线快速失败（FIX，真机：探针/节点异常时点「更新探针」长时间转圈到超时）：
	// 节点不在线时连接池可能残留失活的反向隧道客户端（pool.Get 仍返回 ok），
	// DeployServerProbe 会阻塞到 probeDeployTimeout(30s) 才失败——前端看起来「一直 loading」。
	// 先按心跳态判定：节点非在线直接返回明确原因（HTTP 4xx），让前端秒回错误、不空转。
	// 置于 jar/pool 检查之前，确保离线态在任何 CP 构建（含未内嵌 jar 的开发环境）下均可命中。
	if inst.Node.Status != model.NodeStatusOnline {
		return nil, fmt.Errorf("节点 %s 当前离线，无法推送探针：请先让节点上线再重试", inst.Node.Name)
	}
	client, ok := s.pool.Get(inst.Node.UUID)
	if !ok {
		return nil, fmt.Errorf("Worker %s 未连接", inst.Node.UUID)
	}
	return client, nil
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
