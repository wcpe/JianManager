package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 全局包可视化管理（FR-307）：List/Remove 代理 Worker 同步 RPC，Install 走任务中心异步
// （复用 FR-183/ADR-040 的 jdk_install 范式）。PM 一律取节点已配置偏好（FR-306 落库值），
// 不由调用方指定——单一真相源，避免面板与节点偏好漂移。

// GlobalPackageView 已装全局包视图。
type GlobalPackageView struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Latest  string `json:"latest,omitempty"` // 非空=可更新到该版
}

// SetTaskService 注入任务中心（异步安装用）。
func (s *PMConfigService) SetTaskService(tasks *TaskService) { s.tasks = tasks }

// ListGlobalPackages 列出节点托管全局目录的已装包（FR-307）。
func (s *PMConfigService) ListGlobalPackages(nodeID uint) ([]GlobalPackageView, string, error) {
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return nil, "", err
	}
	cfg, _, err := s.load(nodeID)
	if err != nil {
		return nil, "", err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, cfg.PM, ErrNodeOffline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	resp, err := client.Worker.ListGlobalPackages(ctx, &workerpb.ListGlobalPackagesRequest{Pm: cfg.PM})
	if err != nil {
		return nil, cfg.PM, fmt.Errorf("Worker ListGlobalPackages RPC 失败: %w", err)
	}
	if !resp.Success {
		return nil, cfg.PM, fmt.Errorf("%s", resp.Error)
	}
	out := make([]GlobalPackageView, 0, len(resp.Packages))
	for _, p := range resp.Packages {
		out = append(out, GlobalPackageView{Name: p.Name, Version: p.Version, Latest: p.Latest})
	}
	return out, cfg.PM, nil
}

// InstallGlobalPackageAsync 异步安装/升级全局包（202 语义，FR-307）：建任务 → 下发 Worker
// 即返 → 置 running。进度/日志/终态经心跳 tasks 上报（taskreg 通道）。version 空=latest。
func (s *PMConfigService) InstallGlobalPackageAsync(nodeID uint, name, version string, createdBy uint) (*model.Task, error) {
	if s.tasks == nil {
		return nil, fmt.Errorf("任务中心未启用，无法异步安装")
	}
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return nil, err
	}
	cfg, _, err := s.load(nodeID)
	if err != nil {
		return nil, err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}

	taskID := uuid.NewString()
	ver := version
	if ver == "" {
		ver = "latest"
	}
	title := fmt.Sprintf("安装全局包 %s@%s", name, ver)
	detail := fmt.Sprintf("节点 %s · %s · pm=%s", node.Name, title, cfg.PM)
	task, err := s.tasks.CreateTask(taskID, nodeID, model.TaskKindPkgInstall, title, detail, createdBy)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.InstallGlobalPackage(ctx, &workerpb.InstallGlobalPackageRequest{
		Pm: cfg.PM, Name: name, Version: version, TaskId: taskID,
	})
	if err != nil {
		if markErr := s.tasks.MarkFailed(taskID, fmt.Sprintf("下发 Worker 失败: %v", err)); markErr != nil {
			slog.Warn("标记全局包安装任务失败状态失败", "taskId", taskID, "error", markErr)
		}
		return nil, fmt.Errorf("Worker InstallGlobalPackage RPC 失败: %w", err)
	}
	if !resp.Success {
		if markErr := s.tasks.MarkFailed(taskID, fmt.Sprintf("Worker 拒绝安装: %s", resp.Error)); markErr != nil {
			slog.Warn("标记全局包安装任务失败状态失败", "taskId", taskID, "error", markErr)
		}
		return nil, fmt.Errorf("Worker 拒绝安装: %s", resp.Error)
	}
	if err := s.tasks.MarkRunning(taskID); err != nil {
		return nil, err
	}
	return task, nil
}

// RemoveGlobalPackage 卸载全局包（同步，FR-307）。
func (s *PMConfigService) RemoveGlobalPackage(nodeID uint, name string) error {
	node, err := s.nodeByID(nodeID)
	if err != nil {
		return err
	}
	cfg, _, err := s.load(nodeID)
	if err != nil {
		return err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ErrNodeOffline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
	defer cancel()
	resp, err := client.Worker.RemoveGlobalPackage(ctx, &workerpb.RemoveGlobalPackageRequest{Pm: cfg.PM, Name: name})
	if err != nil {
		return fmt.Errorf("Worker RemoveGlobalPackage RPC 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}
