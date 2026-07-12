package grpc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// 全局包可视化管理（FR-307）：List 同步、Install 经任务中心异步（进度日志逐行上报）、
// Remove 同步。全部操作经 pkgmgr.Manager 落托管全局目录 + 托管 .npmrc + 托管 Node PATH。

// ListGlobalPackages 列出托管全局目录已装包（含 outdated 可更新标记，best-effort）。
func (s *Server) ListGlobalPackages(ctx context.Context, req *workerpb.ListGlobalPackagesRequest) (*workerpb.ListGlobalPackagesResponse, error) {
	if s.pkgMgr == nil {
		return &workerpb.ListGlobalPackagesResponse{Success: false, Error: "包管理器未启用"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	pkgs, err := s.pkgMgr.ListGlobal(ctx, strings.TrimSpace(req.Pm))
	if err != nil {
		return &workerpb.ListGlobalPackagesResponse{Success: false, Error: err.Error()}, nil
	}
	out := make([]*workerpb.GlobalPackage, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, &workerpb.GlobalPackage{Name: p.Name, Version: p.Version, Latest: p.Latest})
	}
	return &workerpb.ListGlobalPackagesResponse{Success: true, Packages: out}, nil
}

// InstallGlobalPackage 安装/升级全局包（task_id 必填走任务中心异步，FR-307）。
func (s *Server) InstallGlobalPackage(ctx context.Context, req *workerpb.InstallGlobalPackageRequest) (*workerpb.InstallGlobalPackageResponse, error) {
	if s.pkgMgr == nil {
		return &workerpb.InstallGlobalPackageResponse{Success: false, Error: "包管理器未启用"}, nil
	}
	if req.TaskId == "" {
		return &workerpb.InstallGlobalPackageResponse{Success: false, Error: "task_id 必填（全局包安装仅支持异步任务路径）"}, nil
	}
	taskID := req.TaskId
	taskCtx, cancel := context.WithCancel(context.Background())
	s.tasks.Start(taskID, cancel)
	pm, name, version := req.Pm, req.Name, req.Version
	go func() {
		defer cancel()
		s.tasks.AppendLog(taskID, fmt.Sprintf("经 %s 安装全局包 %s@%s", pm, name, orLatest(version)))
		err := s.pkgMgr.InstallGlobal(taskCtx, pm, name, version, func(line string) {
			s.tasks.AppendLog(taskID, line)
		})
		if err != nil {
			s.tasks.AppendLog(taskID, "安装失败: "+err.Error())
			s.tasks.Fail(taskID, err.Error())
			return
		}
		s.tasks.SetProgress(taskID, 100)
		s.tasks.Succeed(taskID, fmt.Sprintf(`{"name":%q,"version":%q,"pm":%q}`, name, orLatest(version), pm))
	}()
	return &workerpb.InstallGlobalPackageResponse{Success: true, TaskId: taskID}, nil
}

func orLatest(v string) string {
	if strings.TrimSpace(v) == "" {
		return "latest"
	}
	return v
}

// RemoveGlobalPackage 卸载全局包（同步，FR-307）。
func (s *Server) RemoveGlobalPackage(ctx context.Context, req *workerpb.RemoveGlobalPackageRequest) (*workerpb.RemoveGlobalPackageResponse, error) {
	if s.pkgMgr == nil {
		return &workerpb.RemoveGlobalPackageResponse{Success: false, Error: "包管理器未启用"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	if err := s.pkgMgr.RemoveGlobal(ctx, strings.TrimSpace(req.Pm), strings.TrimSpace(req.Name)); err != nil {
		return &workerpb.RemoveGlobalPackageResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.RemoveGlobalPackageResponse{Success: true}, nil
}
