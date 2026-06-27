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

	if s.pool != nil {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
