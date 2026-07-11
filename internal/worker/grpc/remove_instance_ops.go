package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/internal/worker/search"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// RemoveInstance 移除实例注册并删除其工作目录与派生搜索索引（CP 删除实例时真清理，
// 兑现删除确认文案「所有数据将被删除」）。
// 运行态守卫防 CP/Worker 状态漂移把在跑的服务器连目录端掉；目录删除仅限托管区
// （数据根 var/servers，ADR-010 系统分配）之内——托管区外（历史手填绝对路径）跳过
// 删除但不阻断实例删除，经 work_dir_skipped/skip_reason 回报，绝不越界 RemoveAll。
func (s *Server) RemoveInstance(ctx context.Context, req *workerpb.RemoveInstanceRequest) (*workerpb.RemoveInstanceResponse, error) {
	// 工作目录以注册表为准（注册时已解析为绝对路径）；未注册（Worker 重启后未重推）时
	// 据 CP 下发的相对目录兜底解析。
	workDir := ""
	if inst, ok := s.manager.GetInstance(req.InstanceUuid); ok {
		switch inst.State {
		case process.StateRunning, process.StateStarting, process.StateStopping:
			return &workerpb.RemoveInstanceResponse{
				Success: false,
				Error:   fmt.Sprintf("实例进程仍在运行（状态 %s），拒绝删除工作目录", inst.State),
			}, nil
		}
		workDir = inst.WorkDir
	}
	if workDir == "" && req.WorkDir != "" && s.root != nil {
		workDir = s.root.Abs(req.WorkDir)
	}

	// 先移除注册（幂等：不存在返回 nil），实例从此不可再被启动/文件操作。
	_ = s.manager.Remove(req.InstanceUuid)

	resp := &workerpb.RemoveInstanceResponse{Success: true}
	switch {
	case req.SkipWorkDir:
		// 就地导入实例（FR-302，见 ADR-XXXX）：CP 显式指示保留原目录，
		// 与下方托管区守卫互为双保险——任一生效原目录都完好。
		resp.WorkDirSkipped = true
		resp.SkipReason = "就地导入实例：按 CP 指示保留原目录，未删除任何文件"
	case workDir == "":
		resp.WorkDirSkipped = true
		resp.SkipReason = "工作目录未知（实例未注册且请求未下发目录），未删除任何文件"
	case s.root == nil:
		resp.WorkDirSkipped = true
		resp.SkipReason = "本节点无数据根，无法界定托管区，未删除工作目录"
	case !underDir(s.root.ServersDir(), workDir):
		resp.WorkDirSkipped = true
		resp.SkipReason = fmt.Sprintf("工作目录 %s 不在托管区 (%s) 内，未删除", workDir, s.root.ServersDir())
	default:
		if err := os.RemoveAll(workDir); err != nil {
			return &workerpb.RemoveInstanceResponse{Success: false, Error: fmt.Sprintf("删除工作目录失败: %v", err)}, nil
		}
	}
	if resp.WorkDirSkipped {
		slog.Warn("实例删除：跳过工作目录清理", "instanceId", req.InstanceUuid, "reason", resp.SkipReason)
	} else {
		slog.Info("实例删除：已清理工作目录", "instanceId", req.InstanceUuid, "workDir", workDir)
	}

	// 派生搜索索引随实例一并清理（ADR-017：索引是可随时删除重建的本地派生资产）。
	s.dropSearchIndex(req.InstanceUuid)
	return resp, nil
}

// dropSearchIndex 移除实例的搜索索引对象与磁盘索引目录。
// 未懒创建过索引对象时也要清磁盘（索引可能由上次进程生命周期留下）。
func (s *Server) dropSearchIndex(instanceUUID string) {
	s.searchMu.Lock()
	ix, ok := s.searchIndexes[instanceUUID]
	delete(s.searchIndexes, instanceUUID)
	s.searchMu.Unlock()
	if !ok {
		if s.root == nil {
			return
		}
		ix = search.NewIndex(s.root.IndexDir(), instanceUUID, nil)
	}
	if err := ix.Remove(); err != nil {
		slog.Warn("清理实例搜索索引失败", "instanceId", instanceUUID, "error", err)
	}
}

// underDir 判定 target 是否严格位于 base 目录之内（不含 base 本身）。
// 用于把 RemoveAll 限制在托管区内，参考 jdk.Manager.Remove 的同类防护。
func underDir(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
