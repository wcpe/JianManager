package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
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
//（workDir 必须存在、节点在线且已连接）。
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
