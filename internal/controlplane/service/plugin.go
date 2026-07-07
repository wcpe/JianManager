package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

var (
	// ErrInvalidPluginName 非法插件文件名（含路径分隔符或非 jar）。
	ErrInvalidPluginName = errors.New("非法的插件文件名")
	// ErrPluginNotFound 实例插件目录下未找到该插件。
	ErrPluginNotFound = errors.New("插件不存在")
	// ErrPluginAssetRequired 批量部署未选择插件制品。
	ErrPluginAssetRequired = errors.New("需选择插件制品")
	// ErrPluginTargetRequired 批量部署未指定目标实例。
	ErrPluginTargetRequired = errors.New("需指定目标实例")
	// ErrInvalidPluginAsset 制品不是可部署插件。
	ErrInvalidPluginAsset = errors.New("非法的插件制品")
	// ErrPluginAssetFileUnavailable 制品物理文件不可读取。
	ErrPluginAssetFileUnavailable = errors.New("插件制品文件不可用")
)

// disabledSuffix 禁用插件的文件名后缀：约定 `.jar` 启用 / `.jar.disabled` 禁用。
// 与许多面板/启动器（如 BungeeCord、部分加载器）共识一致：重命名而非删除即可禁用。
const disabledSuffix = ".disabled"

// pluginDirs 列出本 FR 扫描的插件/模组目录（相对实例 workDir）。
// Bukkit 系用 plugins/，Forge/Fabric 系用 mods/；两者都扫，按实际存在的目录聚合。
var pluginDirs = []string{"plugins", "mods"}

// pluginWorkerOps 是 PluginService 依赖的 Worker 文件操作子集（复用既有 file gRPC）。
// 由 workerpb.WorkerServiceClient 自然满足；抽出窄接口便于单测注入伪实现。
type pluginWorkerOps interface {
	ListFiles(ctx context.Context, in *workerpb.ListFilesRequest, opts ...grpc.CallOption) (*workerpb.ListFilesResponse, error)
	WriteFile(ctx context.Context, in *workerpb.WriteFileRequest, opts ...grpc.CallOption) (*workerpb.WriteFileResponse, error)
	DeleteFile(ctx context.Context, in *workerpb.DeleteFileRequest, opts ...grpc.CallOption) (*workerpb.DeleteFileResponse, error)
	RenameFile(ctx context.Context, in *workerpb.RenameFileRequest, opts ...grpc.CallOption) (*workerpb.RenameFileResponse, error)
}

// PluginInfo 单个插件/模组的展示信息。
type PluginInfo struct {
	// Name 展示用文件名（已剥离 `.disabled` 后缀，始终以 `.jar` 结尾）。
	Name string `json:"name"`
	// Dir 所在目录（plugins / mods），用于区分插件与模组。
	Dir string `json:"dir"`
	// Enabled 是否启用（true=`.jar`，false=`.jar.disabled`）。
	Enabled bool `json:"enabled"`
	// Size 字节数。
	Size int64 `json:"size"`
	// ModTime 修改时间（Unix 秒）。
	ModTime int64 `json:"modTime"`
}

// PluginBatchDeployRequest 插件批量部署请求（FR-053）。
type PluginBatchDeployRequest struct {
	AssetIDs []uint
	IDs      []uint
	Filter   *InstanceBatchFilter
}

// PluginBatchDeployInstanceResult 单实例部署结果。
type PluginBatchDeployInstanceResult struct {
	ID      uint   `json:"id"`
	Name    string `json:"name"`
	Skipped bool   `json:"skipped"`
	Reason  string `json:"reason,omitempty"`
	Error   string `json:"error,omitempty"`
}

// PluginBatchDeployResult 批量部署聚合结果，计数口径为实例。
type PluginBatchDeployResult struct {
	Total   int                               `json:"total"`
	Success int                               `json:"success"`
	Failed  int                               `json:"failed"`
	Skipped int                               `json:"skipped"`
	Results []PluginBatchDeployInstanceResult `json:"results"`
}

type pluginBatchAsset struct {
	ID       uint
	Filename string
	Content  []byte
}

// PluginService 插件/模组单服管理（FR-052）。
// 复用 file gRPC（ListFiles/WriteFile/RenameFile/DeleteFile）完成实际文件操作，
// 上传时先入制品库（AssetService，type=plugin，sha256 去重）再部署到实例 plugins/。
// 不直接读写实例工作目录（归 Worker 所有，遵守架构不变量）。
type PluginService struct {
	db    *gorm.DB
	pool  *cpgrpc.ClientPool
	asset *AssetService
	// workerResolver 为测试钩子：非 nil 时替代连接池解析 Worker 文件操作。生产为 nil。
	workerResolver func(nodeUUID string) (pluginWorkerOps, bool)
}

// NewPluginService 创建插件服务。asset 用于上传去重入库，可为 nil（此时上传跳过入库直接部署）。
func NewPluginService(db *gorm.DB, pool *cpgrpc.ClientPool, asset *AssetService) *PluginService {
	return &PluginService{db: db, pool: pool, asset: asset}
}

// List 列出实例 plugins/ 与 mods/ 目录下的插件 jar，识别启用/禁用状态。
// 目录不存在视为空（新建实例尚无 plugins/ 目录），不报错。
func (s *PluginService) List(instanceID uint) ([]PluginInfo, error) {
	inst, worker, err := s.client(instanceID)
	if err != nil {
		return nil, err
	}

	out := make([]PluginInfo, 0)
	for _, dir := range pluginDirs {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resp, err := worker.ListFiles(ctx, &workerpb.ListFilesRequest{
			InstanceUuid: inst.UUID,
			Path:         dir,
		})
		cancel()
		if err != nil {
			// 目录不存在（Worker os.ReadDir 失败）时跳过，不视为错误。
			continue
		}
		for _, f := range resp.Files {
			if f.IsDir {
				continue
			}
			info, ok := parsePluginEntry(f.Name, dir)
			if !ok {
				continue
			}
			info.Size = f.Size
			info.ModTime = f.ModTime
			out = append(out, info)
		}
	}
	// 稳定排序：先按目录，再按展示名，便于前端展示与测试断言。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir < out[j].Dir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Upload 上传插件并部署到实例：先入制品库（type=plugin，sha256 去重），再经 file gRPC 写入目标目录。
// dir 为空时默认 plugins/。返回入库后的资产（asset 为 nil 时返回 nil 资产）。
func (s *PluginService) Upload(instanceID uint, dir, filename string, content []byte) (*model.Asset, error) {
	dir = normalizeDir(dir)
	if err := validatePluginName(filename); err != nil {
		return nil, err
	}

	inst, worker, err := s.client(instanceID)
	if err != nil {
		return nil, err
	}

	// 先入制品库：内容寻址去重，便于 FR-053 批量部署与追溯。入库失败不阻断部署。
	var asset *model.Asset
	if s.asset != nil {
		a, ierr := s.asset.Ingest(io.NopCloser(strings.NewReader(string(content))), IngestParams{
			Type:     model.AssetTypePlugin,
			Name:     strings.TrimSuffix(filename, ".jar"),
			Filename: filename,
		})
		if ierr != nil {
			return nil, fmt.Errorf("插件入库失败: %w", ierr)
		}
		asset = a
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := worker.WriteFile(ctx, &workerpb.WriteFileRequest{
		InstanceUuid: inst.UUID,
		Path:         dir + "/" + filename,
		Content:      content,
	})
	if err != nil {
		return nil, fmt.Errorf("部署插件失败: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("部署插件失败: %s", resp.Error)
	}
	return asset, nil
}

// BatchDeploy 从制品库选择插件并批量写入多个实例的 plugins/ 目录（FR-053）。
func (s *PluginService) BatchDeploy(req PluginBatchDeployRequest, scopeIDs []uint, scope bool) (*PluginBatchDeployResult, error) {
	assets, err := s.loadPluginBatchAssets(req.AssetIDs)
	if err != nil {
		return nil, err
	}
	instances, skipped, err := s.resolvePluginBatchTargets(req, scopeIDs, scope)
	if err != nil {
		return nil, err
	}
	if len(instances) > maxInstanceBatchTargets {
		return nil, fmt.Errorf("批量目标数 %d 超过上限 %d", len(instances), maxInstanceBatchTargets)
	}
	return s.deployPluginBatch(instances, assets, skipped), nil
}

func (s *PluginService) deployPluginBatch(instances []model.Instance, assets []pluginBatchAsset, skipped int) *PluginBatchDeployResult {
	result := &PluginBatchDeployResult{Total: len(instances) + skipped, Skipped: skipped, Results: []PluginBatchDeployInstanceResult{}}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, instanceBatchConcurrency)
	for i := range instances {
		inst := instances[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			item := s.deployPluginBatchOne(&inst, assets)
			mu.Lock()
			appendPluginBatchResult(result, item)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(result.Results, func(i, j int) bool { return result.Results[i].ID < result.Results[j].ID })
	return result
}

func appendPluginBatchResult(result *PluginBatchDeployResult, item PluginBatchDeployInstanceResult) {
	result.Results = append(result.Results, item)
	if item.Skipped {
		result.Skipped++
		return
	}
	if item.Error != "" {
		result.Failed++
		return
	}
	result.Success++
}

func (s *PluginService) deployPluginBatchOne(inst *model.Instance, assets []pluginBatchAsset) PluginBatchDeployInstanceResult {
	item := PluginBatchDeployInstanceResult{ID: inst.ID, Name: inst.Name}
	if inst.WorkDir == "" {
		item.Skipped = true
		item.Reason = ErrWorkDirNotSet.Error()
		return item
	}
	if inst.Node.Status != model.NodeStatusOnline {
		item.Error = ErrNodeNotOnline.Error()
		return item
	}
	worker, ok := s.workerFor(inst.Node.UUID)
	if !ok {
		item.Error = ErrNodeNotConnected.Error()
		return item
	}
	if err := writePluginBatchAssets(inst.UUID, worker, assets); err != nil {
		item.Error = err.Error()
	}
	return item
}

func writePluginBatchAssets(instanceUUID string, worker pluginWorkerOps, assets []pluginBatchAsset) error {
	for _, asset := range assets {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := worker.WriteFile(ctx, &workerpb.WriteFileRequest{InstanceUuid: instanceUUID, Path: "plugins/" + asset.Filename, Content: asset.Content})
		cancel()
		if err != nil {
			return fmt.Errorf("部署插件 %s 失败: %w", asset.Filename, err)
		}
		if !resp.Success {
			return fmt.Errorf("部署插件 %s 失败: %s", asset.Filename, resp.Error)
		}
	}
	return nil
}

func (s *PluginService) loadPluginBatchAssets(assetIDs []uint) ([]pluginBatchAsset, error) {
	if s.asset == nil {
		return nil, ErrPluginAssetFileUnavailable
	}
	if len(assetIDs) == 0 {
		return nil, ErrPluginAssetRequired
	}
	out := make([]pluginBatchAsset, 0, len(assetIDs))
	for _, id := range assetIDs {
		asset, err := s.loadPluginBatchAsset(id)
		if err != nil {
			return nil, err
		}
		out = append(out, *asset)
	}
	return out, nil
}

func (s *PluginService) loadPluginBatchAsset(assetID uint) (*pluginBatchAsset, error) {
	asset, err := s.asset.GetByID(assetID)
	if err != nil {
		return nil, err
	}
	filename, err := pluginAssetFilename(asset)
	if err != nil {
		return nil, err
	}
	content, err := readPluginAssetContent(s.asset, asset)
	if err != nil {
		return nil, err
	}
	return &pluginBatchAsset{ID: asset.ID, Filename: filename, Content: content}, nil
}

func pluginAssetFilename(asset *model.Asset) (string, error) {
	if asset.Type != model.AssetTypePlugin {
		return "", ErrInvalidPluginAsset
	}
	filename := strings.TrimSpace(asset.Filename)
	if filename == "" {
		filename = strings.TrimSpace(asset.Name)
	}
	if err := validatePluginName(filename); err != nil {
		return "", ErrInvalidPluginAsset
	}
	return filename, nil
}

func readPluginAssetContent(assetSvc *AssetService, asset *model.Asset) ([]byte, error) {
	path := assetSvc.AbsPath(asset)
	if path == "" {
		return nil, ErrPluginAssetFileUnavailable
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPluginAssetFileUnavailable, err)
	}
	return content, nil
}

func (s *PluginService) resolvePluginBatchTargets(req PluginBatchDeployRequest, scopeIDs []uint, scope bool) ([]model.Instance, int, error) {
	if len(req.IDs) == 0 && req.Filter == nil {
		return nil, 0, ErrPluginTargetRequired
	}
	if len(req.IDs) > 0 && req.Filter != nil {
		return nil, 0, fmt.Errorf("ids 与 filter 只能指定一个")
	}
	return s.queryPluginBatchTargets(req, scopeIDs, scope)
}

func (s *PluginService) queryPluginBatchTargets(req PluginBatchDeployRequest, scopeIDs []uint, scope bool) ([]model.Instance, int, error) {
	if len(req.IDs) > 0 {
		return s.queryPluginBatchTargetIDs(req.IDs, scopeIDs, scope)
	}
	f := InstanceBatchFilter{}
	if req.Filter != nil {
		f = *req.Filter
	}
	var instances []model.Instance
	q := applyInstanceBatchFilter(s.db.Model(&model.Instance{}).Preload("Node"), f, scopeIDs, scope)
	if err := q.Limit(maxInstanceBatchTargets + 1).Find(&instances).Error; err != nil {
		return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
	}
	return instances, 0, nil
}

func (s *PluginService) queryPluginBatchTargetIDs(ids []uint, scopeIDs []uint, scope bool) ([]model.Instance, int, error) {
	var instances []model.Instance
	q := applyInstanceBatchFilter(s.db.Model(&model.Instance{}).Preload("Node"), InstanceBatchFilter{}, scopeIDs, scope)
	if err := q.Where("instances.id IN ?", ids).Find(&instances).Error; err != nil {
		return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
	}
	skipped := len(ids) - len(instances)
	if skipped < 0 {
		skipped = 0
	}
	return instances, skipped, nil
}

// Delete 删除实例插件目录下的指定插件（同时匹配启用/禁用两种文件名）。
// name 为展示名（不含 `.disabled`）；dir 为空时默认 plugins/。
func (s *PluginService) Delete(instanceID uint, dir, name string) error {
	dir = normalizeDir(dir)
	if err := validatePluginName(name); err != nil {
		return err
	}

	inst, worker, err := s.client(instanceID)
	if err != nil {
		return err
	}

	actual, err := s.resolveActualName(inst.UUID, worker, dir, name)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := worker.DeleteFile(ctx, &workerpb.DeleteFileRequest{
		InstanceUuid: inst.UUID,
		Path:         dir + "/" + actual,
	})
	if err != nil {
		return fmt.Errorf("删除插件失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("删除插件失败: %s", resp.Error)
	}
	return nil
}

// Toggle 启用/禁用插件：经 file Rename gRPC 在 `.jar` 与 `.jar.disabled` 间重命名（不删除文件）。
// name 为展示名；dir 为空时默认 plugins/。返回切换后的启用状态。
func (s *PluginService) Toggle(instanceID uint, dir, name string) (enabled bool, err error) {
	dir = normalizeDir(dir)
	if err := validatePluginName(name); err != nil {
		return false, err
	}

	inst, worker, err := s.client(instanceID)
	if err != nil {
		return false, err
	}

	actual, err := s.resolveActualName(inst.UUID, worker, dir, name)
	if err != nil {
		return false, err
	}
	target, nowEnabled := toggledName(actual)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := worker.RenameFile(ctx, &workerpb.RenameFileRequest{
		InstanceUuid: inst.UUID,
		OldPath:      dir + "/" + actual,
		NewPath:      dir + "/" + target,
	})
	if err != nil {
		return false, fmt.Errorf("切换插件状态失败: %w", err)
	}
	if !resp.Success {
		return false, fmt.Errorf("切换插件状态失败: %s", resp.Error)
	}
	return nowEnabled, nil
}

// resolveActualName 在目录中找到展示名 name 对应的实际文件名（`name` 或 `name.disabled`）。
func (s *PluginService) resolveActualName(instanceUUID string, worker pluginWorkerOps, dir, name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := worker.ListFiles(ctx, &workerpb.ListFilesRequest{
		InstanceUuid: instanceUUID,
		Path:         dir,
	})
	if err != nil {
		return "", ErrPluginNotFound
	}
	enabled := name
	disabled := name + disabledSuffix
	for _, f := range resp.Files {
		if f.Name == enabled || f.Name == disabled {
			return f.Name, nil
		}
	}
	return "", ErrPluginNotFound
}

// worker 测试钩子：覆盖「按节点取 Worker 文件操作」的解析方式，便于单测注入伪实现。
// 生产为 nil，走默认连接池查找。
func (s *PluginService) workerFor(nodeUUID string) (pluginWorkerOps, bool) {
	if s.workerResolver != nil {
		return s.workerResolver(nodeUUID)
	}
	client, ok := s.pool.Get(nodeUUID)
	if !ok {
		return nil, false
	}
	return client.Worker, true
}

// client 加载实例及其节点的 Worker 文件操作句柄，沿用 file/config 服务的校验
// （workDir 必须存在、节点在线且已连接）。
func (s *PluginService) client(instanceID uint) (*model.Instance, pluginWorkerOps, error) {
	var inst model.Instance
	if err := s.db.Preload("Node").First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrInstanceNotFound
		}
		return nil, nil, fmt.Errorf("查询实例失败: %w", err)
	}
	if inst.WorkDir == "" {
		return nil, nil, ErrWorkDirNotSet
	}
	if inst.Node.Status != model.NodeStatusOnline {
		return nil, nil, ErrNodeNotOnline
	}
	w, ok := s.workerFor(inst.Node.UUID)
	if !ok {
		return nil, nil, ErrNodeNotConnected
	}
	return &inst, w, nil
}

// parsePluginEntry 解析目录项为插件信息：仅接受 `*.jar` / `*.jar.disabled`，其余（非 jar）返回 false。
// 返回的 Name 已剥离 `.disabled` 后缀，Enabled 标记启用状态。
func parsePluginEntry(filename, dir string) (PluginInfo, bool) {
	name := filename
	enabled := true
	if strings.HasSuffix(name, disabledSuffix) {
		name = strings.TrimSuffix(name, disabledSuffix)
		enabled = false
	}
	if !strings.HasSuffix(strings.ToLower(name), ".jar") {
		return PluginInfo{}, false
	}
	return PluginInfo{Name: name, Dir: dir, Enabled: enabled}, true
}

// toggledName 计算切换后的文件名与切换后的启用状态：
// `foo.jar`（启用）→ `foo.jar.disabled`（禁用）；`foo.jar.disabled`（禁用）→ `foo.jar`（启用）。
func toggledName(actual string) (target string, nowEnabled bool) {
	if strings.HasSuffix(actual, disabledSuffix) {
		return strings.TrimSuffix(actual, disabledSuffix), true
	}
	return actual + disabledSuffix, false
}

// validatePluginName 校验展示名安全：禁止路径分隔符/路径遍历，且必须是 `.jar`（不含 `.disabled`）。
func validatePluginName(name string) error {
	if name == "" {
		return ErrInvalidPluginName
	}
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		return ErrInvalidPluginName
	}
	if strings.HasSuffix(name, disabledSuffix) {
		// 展示名不应带 `.disabled`，避免歧义（禁用态由文件名后缀表达，不由调用方传入）。
		return ErrInvalidPluginName
	}
	if !strings.HasSuffix(strings.ToLower(name), ".jar") {
		return ErrInvalidPluginName
	}
	return nil
}

// normalizeDir 归一化目标目录：仅允许 plugins/mods，其余（含空）回落到 plugins。
func normalizeDir(dir string) string {
	switch dir {
	case "mods":
		return "mods"
	default:
		return "plugins"
	}
}
