package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

var (
	ErrJDKNotFound = errors.New("JDK 不存在")
	ErrJDKInUse    = errors.New("JDK 正被实例占用")
	ErrNodeOffline = errors.New("节点未连接")
	// ErrInvalidJDKArch 安装请求 arch 不被支持（FR-289），路由映射 422。
	ErrInvalidJDKArch = errors.New("不支持的 arch")
)

type JDKService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
	// settings 提供平台设置生效值（jdk.mirror.<vendor>），使运行时配置的镜像源真生效；
	// 为 nil 时安装走 Worker 本地 env/默认源（FR-063）。
	settings SettingsReader
	// tasks 是全局任务中心服务（FR-183，见 ADR-040）；非 nil 时 Install 走异步任务路径
	// （建 Task→Worker 启动即返回 taskId→返回 taskId，不再阻塞 20min）。为 nil 时回退同步路径。
	tasks *TaskService
}

func NewJDKService(db *gorm.DB, pool *cpgrpc.ClientPool) *JDKService {
	return &JDKService{db: db, pool: pool}
}

// SetTaskService 注入任务中心服务，启用 JDK 安装异步化（FR-183，见 ADR-040）。
// 在 main 装配阶段调用；不调用则 Install 回退同步阻塞路径（向后兼容）。
func (s *JDKService) SetTaskService(t *TaskService) {
	s.tasks = t
}

// SetSettingsReader 注入平台设置读取器（FR-063）。在 main 装配阶段调用，避免构造期循环依赖。
func (s *JDKService) SetSettingsReader(r SettingsReader) {
	s.settings = r
}

// mirrorBaseForVendor 取该 vendor 的下载基址生效值（平台设置 jdk.mirror.<vendor>）。
// 未注入设置读取器或 vendor 无对应键时返回空，由 Worker 回退本地 env/默认源。
func (s *JDKService) mirrorBaseForVendor(vendor string) string {
	if s.settings == nil {
		return ""
	}
	key := jdkMirrorSettingKey(vendor)
	if key == "" {
		return ""
	}
	return s.settings.EffectiveValue(key)
}

// jdkMirrorSettingKey 把 vendor 映射到平台设置键 jdk.mirror.<vendor>（含常见别名归一）。
func jdkMirrorSettingKey(vendor string) string {
	switch strings.ToLower(vendor) {
	case "temurin", "adoptium":
		return SettingKeyJDKMirrorTemurin
	case "corretto", "amazon":
		return SettingKeyJDKMirrorCorretto
	case "zulu", "azul":
		return SettingKeyJDKMirrorZulu
	}
	return ""
}

type CreateJDKRequest struct {
	Vendor       string `json:"vendor" binding:"required"`
	MajorVersion int    `json:"majorVersion" binding:"required"`
	Version      string `json:"version" binding:"required"`
	Arch         string `json:"arch" binding:"required"`
	Path         string `json:"path" binding:"required"`
	Managed      bool   `json:"managed"`
}

type InstallJDKRequest struct {
	Vendor       string `json:"vendor" binding:"required"`
	MajorVersion int    `json:"majorVersion" binding:"required"`
	Arch         string `json:"arch" binding:"required"`
	// Version 具体 JDK 版本（FR-178，可选，如 "21.0.4"）。非空时 Worker 经 foojay 按具体版本解析；
	// 为空取该大版本最新 GA。
	Version string `json:"version"`
}

// normalizeJDKArch 把安装请求的 arch 别名归一为下载源（adoptium/foojay 等）认的写法（FR-289）：
// Go/容器系常用 amd64/arm64，下载源只认 x64/aarch64——直透会产出误导性的「下载返回 HTTP 404」
// （真机复现）。未知 arch 返回 ErrInvalidJDKArch 由路由映射 422，不再透传出 404。
func normalizeJDKArch(arch string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(arch))
	switch a {
	case "x64", "amd64", "x86_64", "x86-64":
		return "x64", nil
	case "aarch64", "arm64":
		return "aarch64", nil
	case "x86", "i386", "i686":
		return "x86", nil
	case "arm", "arm32":
		return "arm", nil
	case "ppc64le", "s390x", "riscv64":
		return a, nil // 下载源原生认的小众架构，原样放行
	default:
		return "", fmt.Errorf("%w: %q（支持 x64/amd64、aarch64/arm64、x86、arm 等）", ErrInvalidJDKArch, arch)
	}
}

func (s *JDKService) List(nodeID uint) ([]model.NodeJDK, error) {
	if err := s.syncFromWorker(nodeID); err != nil {
		slog.Debug("JDK 同步失败（容忍）", "nodeId", nodeID, "error", err)
	}
	var jdks []model.NodeJDK
	if err := s.db.Where("node_id = ?", nodeID).Order("major_version desc, id desc").Find(&jdks).Error; err != nil {
		return nil, fmt.Errorf("查询 JDK 列表失败: %w", err)
	}
	return jdks, nil
}

func (s *JDKService) Create(nodeID uint, req CreateJDKRequest) (*model.NodeJDK, error) {
	if _, err := s.nodeByID(nodeID); err != nil {
		return nil, err
	}
	jdk := &model.NodeJDK{NodeID: nodeID, Vendor: req.Vendor, MajorVersion: req.MajorVersion, Version: req.Version, Arch: req.Arch, Path: req.Path, Managed: req.Managed}
	if err := s.db.Create(jdk).Error; err != nil {
		return nil, fmt.Errorf("登记 JDK 失败: %w", err)
	}
	return jdk, nil
}

// InstallAsync 异步发起 JDK 安装（FR-183，见 ADR-040）：建 Task → 令 Worker 启动即返回 taskId →
// 把 Task 置为 running → 返回 Task（HTTP 202 语义，不再阻塞最长 20min）。
// 落 model.NodeJDK 与完成站内信由心跳终态副作用完成（见 TaskService.IngestSnapshots）。
// createdBy 为发起用户 ID（任务归属 + 完成站内信收件人）。
// 要求已注入 TaskService（SetTaskService）；未注入则回退同步 Install（返回错误提示）。
func (s *JDKService) InstallAsync(nodeID uint, req InstallJDKRequest, createdBy uint) (*model.Task, error) {
	if s.tasks == nil {
		return nil, fmt.Errorf("任务中心未启用，无法异步安装")
	}
	// arch 别名归一化（FR-289）：先于建任务/下发，未知 arch 在此拒绝。
	arch, err := normalizeJDKArch(req.Arch)
	if err != nil {
		return nil, err
	}
	req.Arch = arch
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}

	taskID := uuid.NewString()
	title := fmt.Sprintf("安装 JDK %s %d", req.Vendor, req.MajorVersion)
	detail := fmt.Sprintf("节点 %s · %s · arch=%s", node.Name, title, req.Arch)
	task, err := s.tasks.CreateTask(taskID, nodeID, model.TaskKindJDKInstall, title, detail, createdBy)
	if err != nil {
		return nil, err
	}

	// 下发 Worker：携带 task_id，Worker 启动即返回（不再等下载完成）。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.InstallJDK(ctx, &workerpb.InstallJDKRequest{
		Vendor:       req.Vendor,
		MajorVersion: int32(req.MajorVersion),
		Arch:         req.Arch,
		Version:      req.Version,
		MirrorBase:   s.mirrorBaseForVendor(req.Vendor),
		TaskId:       taskID,
	})
	if err != nil {
		_ = s.tasks.MarkFailed(taskID, fmt.Sprintf("下发 Worker 失败: %v", err))
		return nil, fmt.Errorf("Worker InstallJDK RPC 失败: %w", err)
	}
	if !resp.Success {
		_ = s.tasks.MarkFailed(taskID, fmt.Sprintf("Worker 拒绝安装: %s", resp.Error))
		return nil, fmt.Errorf("Worker 拒绝安装: %s", resp.Error)
	}
	if err := s.tasks.MarkRunning(taskID); err != nil {
		slog.Warn("标记任务 running 失败", "taskId", taskID, "error", err)
	}
	task.State = model.TaskStateRunning
	return task, nil
}

// ProbeResult JDK 探测结果（FR-228），对齐 workerpb.ProbeJDKResponse。
type ProbeResult struct {
	Valid        bool   `json:"valid"`
	Vendor       string `json:"vendor"`
	MajorVersion int    `json:"majorVersion"`
	Version      string `json:"version"`
	Arch         string `json:"arch"`
	JavaHome     string `json:"javaHome"`
	Error        string `json:"error,omitempty"`
}

// Probe 探测节点上某路径（JDK home 或 java 可执行文件）的 JDK 信息（FR-228），供登记前自动填厂商/版本/架构。
// 节点不存在 ErrNodeNotFound、离线 ErrNodeOffline；探测本身失败（非 JDK）作为 Valid=false + Error 返回（非 error）。
func (s *JDKService) Probe(nodeID uint, path string) (*ProbeResult, error) {
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := client.Worker.ProbeJDK(ctx, &workerpb.ProbeJDKRequest{Path: path})
	if err != nil {
		return nil, fmt.Errorf("Worker ProbeJDK RPC 失败: %w", err)
	}
	return &ProbeResult{
		Valid:        resp.Valid,
		Vendor:       resp.Vendor,
		MajorVersion: int(resp.MajorVersion),
		Version:      resp.Version,
		Arch:         resp.Arch,
		JavaHome:     resp.JavaHome,
		Error:        resp.Error,
	}, nil
}

// Install 同步发起 JDK 安装（阻塞至完成，最长 20min）。保留供未启用任务中心时回退与既有测试。
// 生产路径已改用 InstallAsync（FR-183，见 ADR-040）。
func (s *JDKService) Install(nodeID uint, req InstallJDKRequest) (*model.NodeJDK, error) {
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	resp, err := client.Worker.InstallJDK(ctx, &workerpb.InstallJDKRequest{
		Vendor:       req.Vendor,
		MajorVersion: int32(req.MajorVersion),
		Arch:         req.Arch,
		Version:      req.Version,
		MirrorBase:   s.mirrorBaseForVendor(req.Vendor),
	})
	if err != nil {
		return nil, fmt.Errorf("Worker InstallJDK RPC 失败: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("Worker 拒绝安装: %s", resp.Error)
	}
	if resp.Jdk == nil {
		return nil, fmt.Errorf("Worker 返回缺少 JDK 详情")
	}
	jdk := &model.NodeJDK{
		NodeID:       nodeID,
		Vendor:       resp.Jdk.Vendor,
		MajorVersion: int(resp.Jdk.MajorVersion),
		Version:      resp.Jdk.Version,
		Arch:         resp.Jdk.Arch,
		Path:         resp.Jdk.Path,
		Managed:      true,
	}
	if err := s.db.Create(jdk).Error; err != nil {
		return nil, fmt.Errorf("保存 JDK 记录失败: %w", err)
	}
	return jdk, nil
}

func (s *JDKService) Delete(nodeID, jdkID uint) ([]model.Instance, error) {
	var jdk model.NodeJDK
	if err := s.db.Where("id = ? AND node_id = ?", jdkID, nodeID).First(&jdk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJDKNotFound
		}
		return nil, fmt.Errorf("查询 JDK 失败: %w", err)
	}
	var used []model.Instance
	if err := s.db.Where("node_id = ? AND jdk_id = ?", nodeID, jdkID).Find(&used).Error; err != nil {
		return nil, err
	}
	if len(used) > 0 {
		return used, ErrJDKInUse
	}

	// 仅「内部下载（托管）」的 JDK 才删除 Worker 上的文件；外部登记的只删记录、绝不动用户磁盘文件（FR-228 细化：
	// 外部 JDK 由用户自管，平台无权删其文件；托管 JDK 在平台 data 目录下，删记录时一并清理文件）。
	if jdk.Managed && s.pool != nil {
		if node, err := s.nodeByID(nodeID); err == nil {
			if client, ok := s.pool.Get(node.UUID); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				resp, err := client.Worker.RemoveJDK(ctx, &workerpb.RemoveJDKRequest{Path: jdk.Path})
				if err != nil {
					return nil, fmt.Errorf("Worker RemoveJDK RPC 失败: %w", err)
				}
				if !resp.Success {
					return nil, fmt.Errorf("Worker 拒绝删除: %s", resp.Error)
				}
			}
		}
	}

	return nil, s.db.Delete(&model.NodeJDK{}, jdkID).Error
}

// Update modifies a registered JDK. Rejects if instances reference it.
func (s *JDKService) Update(nodeID, jdkID uint, req CreateJDKRequest) (*model.NodeJDK, error) {
	if _, err := s.nodeByID(nodeID); err != nil {
		return nil, err
	}
	var jdk model.NodeJDK
	if err := s.db.Where("id = ? AND node_id = ?", jdkID, nodeID).First(&jdk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJDKNotFound
		}
		return nil, err
	}
	var used []model.Instance
	if err := s.db.Where("node_id = ? AND jdk_id = ?", nodeID, jdkID).Find(&used).Error; err != nil {
		return nil, err
	}
	if len(used) > 0 {
		return nil, ErrJDKInUse
	}
	updates := map[string]interface{}{
		"vendor":        req.Vendor,
		"major_version": req.MajorVersion,
		"version":       req.Version,
		"arch":          req.Arch,
		"path":          req.Path,
		"managed":       req.Managed,
	}
	if err := s.db.Model(&jdk).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("update jdk failed: %w", err)
	}
	var refreshed model.NodeJDK
	if err := s.db.Where("id = ?", jdkID).First(&refreshed).Error; err != nil {
		return nil, err
	}
	return &refreshed, nil
}

func (s *JDKService) ResolveForInstance(nodeID, jdkID uint, javaMajor int) (*model.NodeJDK, error) {
	if jdkID == 0 && javaMajor == 0 {
		return nil, nil
	}
	var jdk model.NodeJDK
	q := s.db.Where("node_id = ?", nodeID)
	if jdkID > 0 {
		q = q.Where("id = ?", jdkID)
	} else {
		q = q.Where("major_version = ?", javaMajor).Order("id desc")
	}
	if err := q.First(&jdk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrJDKNotFound
		}
		return nil, err
	}
	return &jdk, nil
}

// SyncFromWorker 强制从 Worker 同步该节点的 JDK 库存（FR-301 手动刷新入口）。
// 与 List 内的隐式同步同一实现：失败由调用方容忍（DB 旧数据仍可用、前端显旧数据）。
func (s *JDKService) SyncFromWorker(nodeID uint) error {
	return s.syncFromWorker(nodeID)
}

func (s *JDKService) syncFromWorker(nodeID uint) error {
	if s.pool == nil {
		return nil
	}
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ErrNodeOffline
	}
	// FIX-5：List 每次都同步阻塞此 gRPC（登记/打开 JDK 面板均触发），原 30s 超时使 worker gRPC
	// 卡顿/连接陈旧时 GET /jdks 一卡 30s（前端无反馈）。同步是「容忍失败」的尽力而为（失败仍回 DB 数据），
	// 故收敛到 UI 友好的 5s：健康 worker 远低于此、陈旧连接快速失败回退 DB，不再长卡。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.Worker.ListJDKs(ctx, &workerpb.ListJDKsRequest{})
	if err != nil {
		return fmt.Errorf("ListJDKs RPC: %w", err)
	}
	for _, j := range resp.Jdks {
		var existing model.NodeJDK
		err := s.db.Where("node_id = ? AND path = ?", nodeID, j.Path).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := s.db.Create(&model.NodeJDK{
				NodeID:       nodeID,
				Vendor:       j.Vendor,
				MajorVersion: int(j.MajorVersion),
				Version:      j.Version,
				Arch:         j.Arch,
				Path:         j.Path,
				Managed:      j.Managed,
			}).Error; err != nil {
				slog.Warn("同步 JDK 失败（插入）", "error", err)
			}
		} else if err == nil {
			existing.Vendor = j.Vendor
			existing.MajorVersion = int(j.MajorVersion)
			existing.Version = j.Version
			existing.Arch = j.Arch
			existing.Managed = j.Managed
			if err := s.db.Save(&existing).Error; err != nil {
				slog.Warn("同步 JDK 失败（更新）", "error", err)
			}
		}
	}
	// 记录本节点库存同步成功时间（FR-301）：运行时资产页「上次同步」与刷新语义的锚点。
	if err := s.db.Model(&model.Node{}).Where("id = ?", nodeID).
		Update("runtime_synced_at", time.Now()).Error; err != nil {
		slog.Warn("记录运行时同步时间失败", "nodeId", nodeID, "error", err)
	}
	return nil
}

func (s *JDKService) nodeByID(nodeID uint) (*model.Node, error) {
	var n model.Node
	if err := s.db.First(&n, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	return &n, nil
}
