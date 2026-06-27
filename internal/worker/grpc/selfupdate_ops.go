package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/platform/selfupdate"
	"github.com/wcpe/JianManager/internal/version"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 面板自更新 RPC（FR-081，见 ADR-020 §4）。
// Worker 经 CP gRPC 编排升级：CP 下发二进制地址/sha256，Worker 下载校验替换重启。
// daemon 模式下不杀游戏服——替换/重启只动 Worker 主进程，wrapper 子进程（持有 Java 游戏服）
// 独立存活；Worker 重启后 RecoverDaemonInstances 经 PID 文件重连存活 wrapper（ADR-003）。

// restartDelay 是替换成功后延迟重启的时长，用于先让 UpgradeWorker 的 gRPC 响应回到 CP，
// 再退出/重启进程（确保 CP 收到升级结果而非因连接被切而误判失败）。
const restartDelay = 800 * time.Millisecond

// GetVersion 返回 Worker 当前版本与平台信息（CP 自更新检查比对用，FR-081）。
// BackupVersion 透出本地升级前备份版本（无备份为空），供 CP 检查更新展示节点 backupVersion（FR-182）。
func (s *Server) GetVersion(_ context.Context, _ *workerpb.GetVersionRequest) (*workerpb.GetVersionResponse, error) {
	resp := &workerpb.GetVersionResponse{
		Version: version.Version,
		Os:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	if meta, ok := selfupdate.BackupInfo(selfupdate.ComponentWorker, s.root); ok {
		resp.BackupVersion = meta.Version
	}
	return resp, nil
}

// UpgradeWorker 下载目标二进制 → sha256 校验 → 替换自身 → 计划重启（FR-081）。
//
// 校验不符或替换失败时回 success=false、error 说明，绝不替换/重启。
// 替换成功后先返回 success=true，再异步延迟重启（restartFn 默认 selfupdate.Restart，
// 测试可经 SetRestartFunc 注入空操作避免真重启）。
func (s *Server) UpgradeWorker(ctx context.Context, req *workerpb.UpgradeWorkerRequest) (*workerpb.UpgradeWorkerResponse, error) {
	from := version.Version

	// action=rollback：回滚本地升级前备份，不下载（FR-182，见 ADR-042）。
	if strings.EqualFold(strings.TrimSpace(req.Action), workerRollbackAction) {
		return s.rollbackWorker(from), nil
	}

	if strings.TrimSpace(req.DownloadUrl) == "" {
		return &workerpb.UpgradeWorkerResponse{Success: false, Error: "下载地址为空", FromVersion: from}, nil
	}

	// 下载落到数据根 cache/；root 为 nil（极少数未初始化场景）时回退系统临时目录。
	cacheDir := os.TempDir()
	if s.root != nil {
		cacheDir = s.root.CacheDir()
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			return &workerpb.UpgradeWorkerResponse{Success: false, Error: fmt.Sprintf("创建缓存目录失败: %v", err), FromVersion: from}, nil
		}
	}
	dest := filepath.Join(cacheDir, fmt.Sprintf("worker-upgrade-%d", time.Now().UnixNano()))

	if err := selfupdate.DownloadWith(ctx, s.outboundClient(), req.DownloadUrl, req.Sha256, dest, req.AllowInsecure); err != nil {
		slog.Warn("Worker 升级下载/校验失败", "targetVersion", req.TargetVersion, "error", err)
		return &workerpb.UpgradeWorkerResponse{Success: false, Error: err.Error(), FromVersion: from}, nil
	}

	target, err := s.resolveExecutable()
	if err != nil {
		_ = os.Remove(dest)
		return &workerpb.UpgradeWorkerResponse{Success: false, Error: fmt.Sprintf("定位自身可执行文件失败: %v", err), FromVersion: from}, nil
	}
	// 替换前备份当前二进制供回滚（FR-182）；备份失败不阻断升级（仅记日志，本次升级无退路）。
	if err := selfupdate.BackupCurrentFrom(selfupdate.ComponentWorker, from, target, s.root); err != nil {
		slog.Warn("Worker 升级前备份失败，本次升级无回滚退路", "error", err)
	}
	if err := selfupdate.ReplaceExecutable(target, dest); err != nil {
		_ = os.Remove(dest)
		slog.Error("Worker 二进制替换失败", "targetVersion", req.TargetVersion, "error", err)
		return &workerpb.UpgradeWorkerResponse{Success: false, Error: err.Error(), FromVersion: from}, nil
	}

	slog.Info("Worker 二进制已替换，计划重启", "fromVersion", from, "targetVersion", req.TargetVersion)
	// 异步延迟重启：先让本 RPC 响应回到 CP，再重启进程。
	go func() {
		time.Sleep(restartDelay)
		s.doRestart()
	}()

	return &workerpb.UpgradeWorkerResponse{Success: true, FromVersion: from}, nil
}

// workerRollbackAction 是 UpgradeWorkerRequest.action 触发回滚的取值（FR-182）。
const workerRollbackAction = "rollback"

// rollbackWorker 回滚本地升级前备份（FR-182，见 ADR-042）：校验备份 sha → 换回 → 异步延迟重启。
// 无备份回 success=false（error 文案含 NO_BACKUP 供 CP 映射友好码）；不下载任何远端制品。
func (s *Server) rollbackWorker(from string) *workerpb.UpgradeWorkerResponse {
	target, err := s.resolveExecutable()
	if err != nil {
		return &workerpb.UpgradeWorkerResponse{Success: false, Error: fmt.Sprintf("定位自身可执行文件失败: %v", err), FromVersion: from}
	}
	meta, err := selfupdate.RollbackTo(selfupdate.ComponentWorker, target, s.root)
	if err != nil {
		if errors.Is(err, selfupdate.ErrNoBackup) {
			return &workerpb.UpgradeWorkerResponse{Success: false, Error: "NO_BACKUP: 无备份可回滚", FromVersion: from}
		}
		slog.Error("Worker 回滚失败", "error", err)
		return &workerpb.UpgradeWorkerResponse{Success: false, Error: err.Error(), FromVersion: from}
	}
	slog.Info("Worker 二进制已回滚，计划重启", "fromVersion", from, "toVersion", meta.Version)
	go func() {
		time.Sleep(restartDelay)
		s.doRestart()
	}()
	return &workerpb.UpgradeWorkerResponse{Success: true, FromVersion: from}
}

// doRestart 执行重启动作；默认 selfupdate.Restart（re-exec 后退出），可经 SetRestartFunc 替换（测试）。
func (s *Server) doRestart() {
	if s.restartFn != nil {
		s.restartFn()
		return
	}
	if err := selfupdate.Restart(); err != nil {
		slog.Error("Worker 自重启失败，退出由进程托管者拉起", "error", err)
	}
	// re-exec 已拉起新进程；退出当前进程让出端口/socket（由系统服务/脚本 supervisor 或新进程接管）。
	os.Exit(0)
}

// SetRestartFunc 注入重启动作（测试用：替换为不真重启的空操作或断言钩子）。
func (s *Server) SetRestartFunc(fn func()) {
	s.restartFn = fn
}

// resolveExecutable 返回待替换的可执行文件路径；默认 os.Executable()，可经 SetExecutablePath 覆盖（测试）。
func (s *Server) resolveExecutable() (string, error) {
	if s.execPath != "" {
		return s.execPath, nil
	}
	return os.Executable()
}

// SetExecutablePath 覆盖待替换的可执行文件路径（测试用：指向临时「假二进制」避免替换真测试二进制）。
func (s *Server) SetExecutablePath(path string) {
	s.execPath = path
}
