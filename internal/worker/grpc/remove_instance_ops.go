package grpc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/internal/worker/search"
	"github.com/wcpe/JianManager/proto/workerpb"
)

const (
	// removeWorkDirAttempts / removeWorkDirBackoff：删除工作目录的校验重试参数。
	// RemoveAll 返回 nil 不代表目录真消失——FR-310 竞态下未死透的 Java 会在 unlink 之后重建 world/；
	// 故删完复核目录是否仍在，仍在则退避重试。正常路径已由 ReapDaemonForDelete 杀死写者，此处为纵深防御，
	// 同时吸收 Windows 上被占用文件的瞬时共享冲突。
	removeWorkDirAttempts = 5
	removeWorkDirBackoff  = 200 * time.Millisecond
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

	// FR-310：删除运行中 daemon 实例的竞态——manager.Remove 只杀 wrapper 组，Unix 上自成进程组的
	// Java 子进程未死，会在下方 RemoveAll 之后继续写 world region 把工作目录重建出来（且遗留 pid/sock）。
	// 删目录前先按 PID 记录强杀整棵进程树并确认死透，顺带清理遗留的 <uuid>.pid / <uuid>.sock。
	s.manager.ReapDaemonForDelete(req.InstanceUuid)

	resp := &workerpb.RemoveInstanceResponse{Success: true}
	switch {
	case req.SkipWorkDir:
		// 就地导入实例（FR-302，见 ADR-069）：CP 显式指示保留原目录，
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
		if err := removeInstanceWorkDir(workDir); err != nil {
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

// removeInstanceWorkDir 删除工作目录并校验其确已消失，最多重试 removeWorkDirAttempts 次。
// 兜底两种竞态：① 存活进程在一次「成功」的 RemoveAll 之后重建目录（Unix 上 unlink 成功而写者仍在——
// FR-310 的核心症状，正常路径已由 ReapDaemonForDelete 杀死写者消除，此处为纵深防御）；② Windows 上
// 被占用文件导致 RemoveAll 瞬时报共享冲突。目录最终不存在即视为成功（幂等）。
func removeInstanceWorkDir(dir string) error {
	var lastErr error
	for attempt := 0; attempt < removeWorkDirAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(removeWorkDirBackoff)
		}
		lastErr = os.RemoveAll(dir)
		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
			return nil // 确认消失（RemoveAll 成功且未被回写）
		}
		if lastErr == nil {
			// RemoveAll 报成功但目录仍在：被存活进程回写，退避后重试。
			lastErr = fmt.Errorf("工作目录删除后仍存在（疑被存活进程回写）: %s", dir)
		}
	}
	return lastErr
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
