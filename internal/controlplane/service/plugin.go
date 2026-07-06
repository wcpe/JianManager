package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	// ErrInvalidPluginName 非法插件文件名（含路径分隔符或非法后缀）。
	ErrInvalidPluginName = errors.New("非法的插件文件名")
	// ErrPluginFileExists 目标目录已有同名文件且未允许覆盖。
	ErrPluginFileExists = errors.New("插件文件已存在")
	// ErrPluginNotFound 实例插件目录下未找到该插件。
	ErrPluginNotFound = errors.New("插件不存在")
	// ErrInvalidPluginAsset 制品不是可部署的插件 jar。
	ErrInvalidPluginAsset = errors.New("制品不是可部署的插件 jar")
)

// disabledSuffix 禁用插件的文件名后缀：约定原文件名启用 / 加 `.disabled` 禁用。
// 与许多面板/启动器（如 BungeeCord、部分加载器）共识一致：重命名而非删除即可禁用。
const disabledSuffix = ".disabled"

const (
	maxPluginBatchTargets       = 500
	maxPluginBatchErrors        = 100
	maxPluginMetadataBytes      = 64 * 1024 * 1024
	maxPluginMetadataEntryBytes = 256 * 1024
)

// pluginDirs 列出本 FR 扫描的受控目录（相对实例 workDir）。
// Bukkit 系用 plugins/，Forge/Fabric 系用 mods/，资源包/数据包分别走 resourcepacks/ 与 datapacks/。
var pluginDirs = []string{"plugins", "mods", "resourcepacks", "datapacks"}

var pluginDirExt = map[string]string{
	"plugins":       ".jar",
	"mods":          ".jar",
	"resourcepacks": ".zip",
	"datapacks":     ".zip",
}

// pluginWorkerOps 是 PluginService 依赖的 Worker 文件操作子集（复用既有 file gRPC）。
// 由 workerpb.WorkerServiceClient 自然满足；抽出窄接口便于单测注入伪实现。
type pluginWorkerOps interface {
	ListFiles(ctx context.Context, in *workerpb.ListFilesRequest, opts ...grpc.CallOption) (*workerpb.ListFilesResponse, error)
	ReadFile(ctx context.Context, in *workerpb.ReadFileRequest, opts ...grpc.CallOption) (*workerpb.ReadFileResponse, error)
	WriteFile(ctx context.Context, in *workerpb.WriteFileRequest, opts ...grpc.CallOption) (*workerpb.WriteFileResponse, error)
	DeleteFile(ctx context.Context, in *workerpb.DeleteFileRequest, opts ...grpc.CallOption) (*workerpb.DeleteFileResponse, error)
	RenameFile(ctx context.Context, in *workerpb.RenameFileRequest, opts ...grpc.CallOption) (*workerpb.RenameFileResponse, error)
}

// PluginInfo 单个插件/模组/资源包/数据包的展示信息。
type PluginInfo struct {
	// Name 展示用文件名（已剥离 `.disabled` 后缀，按目录保持 `.jar` 或 `.zip`）。
	Name string `json:"name"`
	// Dir 所在目录（plugins / mods / resourcepacks / datapacks）。
	Dir string `json:"dir"`
	// Enabled 是否启用（true=原文件名，false=加 `.disabled` 后缀）。
	Enabled bool `json:"enabled"`
	// Size 字节数。
	Size int64 `json:"size"`
	// ModTime 修改时间（Unix 秒）。
	ModTime int64 `json:"modTime"`
	// Version 可选版本号，来自 jar/zip 内元信息。
	Version string `json:"version,omitempty"`
	// Author 可选作者摘要，来自 jar/zip 内元信息。
	Author string `json:"author,omitempty"`
	// Dependencies 可选依赖摘要，来自 jar/zip 内元信息。
	Dependencies []string `json:"dependencies,omitempty"`
}

// PluginBatchTarget 描述插件批量部署目标：显式 ids 或筛选条件二选一。
type PluginBatchTarget struct {
	IDs    []uint
	Filter *InstanceBatchFilter
}

// PluginBatchDeployRequest 是插件批量部署请求。
type PluginBatchDeployRequest struct {
	AssetIDs    []uint
	Target      PluginBatchTarget
	Destination string
	Overwrite   bool
}

// PluginBatchDeployItem 是单实例单插件部署结果。
type PluginBatchDeployItem struct {
	InstanceID uint   `json:"instanceId"`
	AssetID    uint   `json:"assetId"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// PluginBatchDeployResult 是插件批量部署汇总。
type PluginBatchDeployResult struct {
	RequestedInstances int                     `json:"requestedInstances"`
	RequestedAssets    int                     `json:"requestedAssets"`
	Succeeded          int                     `json:"succeeded"`
	Failed             int                     `json:"failed"`
	Skipped            int                     `json:"skipped"`
	Results            []PluginBatchDeployItem `json:"results"`
}

type pluginBatchAsset struct {
	id       uint
	filename string
	content  []byte
}

type pluginUploadOptions struct {
	overwrite bool
}

type PluginUploadOption func(*pluginUploadOptions)

func WithPluginOverwrite(overwrite bool) PluginUploadOption {
	return func(options *pluginUploadOptions) {
		options.overwrite = overwrite
	}
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

// List 列出实例受控目录下的 jar/zip 制品，识别启用/禁用状态。
// 目录不存在视为空（新建实例尚无受控目录），不报错。
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
			s.applyPluginMetadata(inst.UUID, worker, dir, f.Name, f.Size, &info)
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

// Upload 上传受控制品并部署到实例：jar 入制品库（type=plugin，sha256 去重），再经 file gRPC 写入目标目录。
// dir 为空时默认 plugins/；overwrite=false 时拒绝覆盖同名文件。返回入库后的资产（asset 为 nil 时返回 nil 资产）。
func (s *PluginService) Upload(instanceID uint, dir, filename string, content []byte, opts ...PluginUploadOption) (*model.Asset, error) {
	dir = normalizeDir(dir)
	if err := validatePluginFileName(dir, filename); err != nil {
		return nil, err
	}
	options := pluginUploadOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}

	inst, worker, err := s.client(instanceID)
	if err != nil {
		return nil, err
	}

	if !options.overwrite {
		exists, err := pluginFileExists(inst.UUID, worker, dir, filename)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, ErrPluginFileExists
		}
	}

	// jar 先入制品库：内容寻址去重，便于 FR-053 批量部署与追溯。
	var asset *model.Asset
	if s.asset != nil && pluginDirExt[dir] == ".jar" {
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

// BatchDeploy 从制品库选择插件 jar，同步批量部署到多个实例。
func (s *PluginService) BatchDeploy(req PluginBatchDeployRequest, scopeIDs []uint, scope bool) (*PluginBatchDeployResult, error) {
	if len(req.AssetIDs) == 0 {
		return nil, fmt.Errorf("至少选择一个插件制品")
	}
	if len(req.Target.IDs) == 0 && req.Target.Filter == nil {
		return nil, fmt.Errorf("需指定目标实例或筛选条件")
	}
	assets, err := s.loadBatchAssets(req.AssetIDs)
	if err != nil {
		return nil, err
	}
	targets, skipped, err := s.resolveBatchDeployTargets(req.Target, scopeIDs, scope)
	if err != nil {
		return nil, err
	}
	if len(targets) > maxPluginBatchTargets {
		return nil, fmt.Errorf("批量目标数 %d 超过上限 %d", len(targets), maxPluginBatchTargets)
	}

	result := &PluginBatchDeployResult{
		RequestedInstances: len(targets) + skipped,
		RequestedAssets:    len(assets),
		Skipped:            skipped,
		Results:            []PluginBatchDeployItem{},
	}
	if len(targets) == 0 {
		return result, nil
	}

	dir := normalizeDir(req.Destination)
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, instanceBatchConcurrency)
	)
	for i := range targets {
		inst := targets[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			items := s.deployAssetsToInstance(&inst, assets, dir, req.Overwrite)

			mu.Lock()
			defer mu.Unlock()
			for _, item := range items {
				if item.OK {
					result.Succeeded++
				} else {
					result.Failed++
				}
				if len(result.Results) < maxPluginBatchErrors || item.OK {
					result.Results = append(result.Results, item)
				}
			}
		}()
	}
	wg.Wait()
	return result, nil
}

// Delete 删除实例插件目录下的指定插件（同时匹配启用/禁用两种文件名）。
// name 为展示名（不含 `.disabled`）；dir 为空时默认 plugins/。
func (s *PluginService) Delete(instanceID uint, dir, name string) error {
	dir = normalizeDir(dir)
	if err := validatePluginFileName(dir, name); err != nil {
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

// Toggle 启用/禁用受控制品：经 file Rename gRPC 在原文件名与 `.disabled` 后缀间重命名（不删除文件）。
// name 为展示名；dir 为空时默认 plugins/。返回切换后的启用状态。
func (s *PluginService) Toggle(instanceID uint, dir, name string) (enabled bool, err error) {
	dir = normalizeDir(dir)
	if err := validatePluginFileName(dir, name); err != nil {
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

func (s *PluginService) loadBatchAssets(assetIDs []uint) ([]pluginBatchAsset, error) {
	if s.asset == nil {
		return nil, fmt.Errorf("制品库未配置")
	}
	assets := make([]pluginBatchAsset, 0, len(assetIDs))
	for _, id := range assetIDs {
		asset, err := s.asset.GetByID(id)
		if err != nil {
			return nil, err
		}
		if asset.Type != model.AssetTypePlugin {
			return nil, ErrInvalidPluginAsset
		}
		filename, err := pluginAssetFilename(asset)
		if err != nil {
			return nil, err
		}
		content, err := os.ReadFile(s.asset.AbsPath(asset))
		if err != nil {
			return nil, fmt.Errorf("读取插件制品失败: %w", err)
		}
		assets = append(assets, pluginBatchAsset{id: id, filename: filename, content: content})
	}
	return assets, nil
}

func (s *PluginService) resolveBatchDeployTargets(target PluginBatchTarget, scopeIDs []uint, scope bool) ([]model.Instance, int, error) {
	var instances []model.Instance
	if len(target.IDs) > 0 {
		q := applyInstanceBatchFilter(s.db.Model(&model.Instance{}).Preload("Node"), InstanceBatchFilter{}, scopeIDs, scope)
		if err := q.Where("instances.id IN ?", target.IDs).Find(&instances).Error; err != nil {
			return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
		}
		skipped := len(target.IDs) - len(instances)
		if skipped < 0 {
			skipped = 0
		}
		return instances, skipped, nil
	}

	f := InstanceBatchFilter{}
	if target.Filter != nil {
		f = *target.Filter
	}
	q := applyInstanceBatchFilter(s.db.Model(&model.Instance{}).Preload("Node"), f, scopeIDs, scope)
	if err := q.Limit(maxPluginBatchTargets + 1).Find(&instances).Error; err != nil {
		return nil, 0, fmt.Errorf("查询批量目标失败: %w", err)
	}
	return instances, 0, nil
}

func (s *PluginService) deployAssetsToInstance(inst *model.Instance, assets []pluginBatchAsset, dir string, overwrite bool) []PluginBatchDeployItem {
	items := make([]PluginBatchDeployItem, 0, len(assets))
	for _, asset := range assets {
		item := PluginBatchDeployItem{InstanceID: inst.ID, AssetID: asset.id}
		if err := s.deployAssetToInstance(inst, asset, dir, overwrite); err != nil {
			item.Error = err.Error()
		} else {
			item.OK = true
		}
		items = append(items, item)
	}
	return items
}

func (s *PluginService) deployAssetToInstance(inst *model.Instance, asset pluginBatchAsset, dir string, overwrite bool) error {
	if inst.WorkDir == "" {
		return ErrWorkDirNotSet
	}
	if inst.Node.Status != model.NodeStatusOnline {
		return ErrNodeNotOnline
	}
	worker, ok := s.workerFor(inst.Node.UUID)
	if !ok {
		return ErrNodeNotConnected
	}
	if !overwrite {
		exists, err := pluginFileExists(inst.UUID, worker, dir, asset.filename)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("文件已存在: %s/%s", dir, asset.filename)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := worker.WriteFile(ctx, &workerpb.WriteFileRequest{
		InstanceUuid: inst.UUID,
		Path:         dir + "/" + asset.filename,
		Content:      asset.content,
	})
	if err != nil {
		return fmt.Errorf("部署插件失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("部署插件失败: %s", resp.Error)
	}
	return nil
}

func pluginFileExists(instanceUUID string, worker pluginWorkerOps, dir, filename string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := worker.ListFiles(ctx, &workerpb.ListFilesRequest{
		InstanceUuid: instanceUUID,
		Path:         dir,
	})
	if err != nil {
		return false, nil
	}
	for _, f := range resp.Files {
		if f.Name == filename || f.Name == filename+disabledSuffix {
			return true, nil
		}
	}
	return false, nil
}

func pluginAssetFilename(asset *model.Asset) (string, error) {
	filename := strings.TrimSpace(asset.Filename)
	if filename == "" {
		filename = strings.TrimSpace(asset.Name)
	}
	if filename != "" && filepath.Ext(filename) == "" {
		filename += ".jar"
	}
	if err := validatePluginName(filename); err != nil {
		return "", ErrInvalidPluginAsset
	}
	return filename, nil
}

type pluginMetadata struct {
	version      string
	author       string
	dependencies []string
}

func (s *PluginService) applyPluginMetadata(instanceUUID string, worker pluginWorkerOps, dir, actualName string, size int64, info *PluginInfo) {
	if size <= 0 || size > maxPluginMetadataBytes {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := worker.ReadFile(ctx, &workerpb.ReadFileRequest{
		InstanceUuid: instanceUUID,
		Path:         dir + "/" + actualName,
	})
	if err != nil || len(resp.Content) == 0 {
		return
	}
	meta := parsePluginMetadata(resp.Content)
	info.Version = meta.version
	info.Author = meta.author
	info.Dependencies = meta.dependencies
}

func parsePluginMetadata(content []byte) pluginMetadata {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return pluginMetadata{}
	}
	for _, file := range reader.File {
		name := strings.ToLower(strings.TrimPrefix(file.Name, "/"))
		raw, ok := readMetadataEntry(file)
		if !ok {
			continue
		}
		switch name {
		case "plugin.yml", "bungee.yml":
			return parsePluginYAML(raw)
		case "fabric.mod.json":
			return parseFabricModJSON(raw)
		case "meta-inf/mods.toml":
			return parseModsToml(raw)
		case "pack.mcmeta":
			return parsePackMcmeta(raw)
		}
	}
	return pluginMetadata{}
}

func readMetadataEntry(file *zip.File) ([]byte, bool) {
	if file.UncompressedSize64 > maxPluginMetadataEntryBytes {
		return nil, false
	}
	reader, err := file.Open()
	if err != nil {
		return nil, false
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, maxPluginMetadataEntryBytes+1))
	if err != nil || len(raw) > maxPluginMetadataEntryBytes {
		return nil, false
	}
	return raw, true
}

func parsePluginYAML(raw []byte) pluginMetadata {
	values := parseKeyValues(string(raw), ":")
	deps := append(parseMetadataList(values["depend"]), parseMetadataList(values["softdepend"])...)
	return pluginMetadata{
		version:      values["version"],
		author:       firstNonEmpty(values["author"], values["authors"]),
		dependencies: uniqueMetadataValues(deps),
	}
}

func parseModsToml(raw []byte) pluginMetadata {
	var version string
	var author string
	var deps []string
	inDependency := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "[[dependencies."):
			inDependency = true
			continue
		case strings.HasPrefix(line, "["):
			inDependency = false
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		cleaned := cleanMetadataValue(value)
		if inDependency {
			if key == "modid" {
				deps = append(deps, cleaned)
			}
			continue
		}
		if key == "version" && version == "" {
			version = cleaned
		}
		if (key == "author" || key == "authors") && author == "" {
			author = cleaned
		}
	}
	return pluginMetadata{
		version:      version,
		author:       author,
		dependencies: uniqueMetadataValues(deps),
	}
}

func parseFabricModJSON(raw []byte) pluginMetadata {
	var body struct {
		Version string         `json:"version"`
		Authors any            `json:"authors"`
		Depends map[string]any `json:"depends"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return pluginMetadata{}
	}
	deps := make([]string, 0, len(body.Depends))
	for name := range body.Depends {
		deps = append(deps, name)
	}
	sort.Strings(deps)
	return pluginMetadata{version: body.Version, author: metadataAuthor(body.Authors), dependencies: deps}
}

func parsePackMcmeta(raw []byte) pluginMetadata {
	var body struct {
		Pack struct {
			PackFormat int `json:"pack_format"`
		} `json:"pack"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Pack.PackFormat == 0 {
		return pluginMetadata{}
	}
	return pluginMetadata{version: "pack_format " + strconv.Itoa(body.Pack.PackFormat)}
}

func parseKeyValues(content, sep string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, sep)
		if ok {
			values[strings.ToLower(strings.TrimSpace(key))] = cleanMetadataValue(value)
		}
	}
	return values
}

func parseMetadataList(raw string) []string {
	raw = strings.Trim(raw, "[] ")
	if raw == "" {
		return nil
	}
	items := strings.Split(raw, ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value := cleanMetadataValue(item); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cleanMetadataValue(raw string) string {
	return strings.Trim(strings.TrimSpace(raw), `"'`)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func uniqueMetadataValues(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func metadataAuthor(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if part := metadataAuthor(item); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		if name, ok := value["name"].(string); ok {
			return name
		}
	}
	return ""
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

// parsePluginEntry 解析目录项为受控制品信息：按目录仅接受 jar/zip 及其 `.disabled` 形态。
// 返回的 Name 已剥离 `.disabled` 后缀，Enabled 标记启用状态。
func parsePluginEntry(filename, dir string) (PluginInfo, bool) {
	dir = normalizeDir(dir)
	name := filename
	enabled := true
	if strings.HasSuffix(name, disabledSuffix) {
		name = strings.TrimSuffix(name, disabledSuffix)
		enabled = false
	}
	if validatePluginFileName(dir, name) != nil {
		return PluginInfo{}, false
	}
	return PluginInfo{Name: name, Dir: dir, Enabled: enabled}, true
}

// toggledName 计算切换后的文件名与切换后的启用状态：
// `foo.jar|zip`（启用）→ `foo.jar|zip.disabled`（禁用）；禁用态反向切回启用。
func toggledName(actual string) (target string, nowEnabled bool) {
	if strings.HasSuffix(actual, disabledSuffix) {
		return strings.TrimSuffix(actual, disabledSuffix), true
	}
	return actual + disabledSuffix, false
}

// validatePluginName 校验插件 jar 展示名安全：禁止路径分隔符/路径遍历，且必须是 `.jar`。
func validatePluginName(name string) error {
	return validatePluginFileName("plugins", name)
}

func validatePluginFileName(dir, name string) error {
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
	ext := pluginDirExt[normalizeDir(dir)]
	if ext == "" || !strings.HasSuffix(strings.ToLower(name), ext) {
		return ErrInvalidPluginName
	}
	return nil
}

// normalizeDir 归一化目标目录：仅允许受控目录，其余（含空）回落到 plugins。
func normalizeDir(dir string) string {
	switch dir {
	case "mods", "resourcepacks", "datapacks":
		return dir
	default:
		return "plugins"
	}
}
