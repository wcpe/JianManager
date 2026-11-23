package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/rollog"
	workercfg "github.com/wcpe/JianManager/internal/worker"
)

// processLogFile 是 Worker 进程自身日志的文件写入器（自轮转）。
//
// # 为什么 Worker 需要自己写文件
//
// 四个部署脚本（install-worker / deploy-worker / deploy-cp / cp-lifecycle）里
// **都没有** StandardOutput=，于是生产形态只能是以下两种，且都有硬伤：
//   - systemd journald：CP 面板读不到 Worker 的 journald，节点出问题只能 SSH；
//   - 运维手动 `>> worker.out`：进程持有 fd，logrotate 只能 copytruncate，
//     每次轮转要读写整个文件（2026-10-27 现场 35GB 就是这么来的）。
//
// 进程自己写文件后，轮转变成一次 rename（O(1)、零字节复制），且路径确定，
// 面板诊断接口可以稳定定位它。
//
// stderr 仍然保留（io.MultiWriter），前台运行 / 容器 / CI 场景不受影响。
var processLogFile *rollog.Writer

// openProcessLogger 打开进程日志文件并返回双写 writer（stderr + 文件）。
//
// 用 dataroot.Resolve 而不是 dataroot.Init：Resolve 只算路径不建目录，
// 而日志必须先于 Init 初始化——启动早期的失败（如数据根不可写）本身就要能落到文件上。
// rollog.Open 会自建父目录，两者相容。
func openProcessLogger(dataDir string, cfg workercfg.LogConfig) (io.Writer, error) {
	root, err := dataroot.Resolve(dataDir)
	if err != nil {
		return nil, fmt.Errorf("解析数据根失败: %w", err)
	}
	ret := cfg.Retention()
	w, err := rollog.Open(rollog.Config{
		Path:            filepath.Join(root.LogDir(), "worker.log"),
		MaxSizeBytes:    ret.MaxSizeBytes,
		MaxBackups:      ret.MaxBackups,
		MaxAgeDays:      ret.MaxAgeDays,
		DisableCompress: !ret.Compress,
		OnError: func(e error) {
			// 刻意不经 slog：日志组件自身的错误写回 slog 会递归。
			fmt.Fprintf(os.Stderr, "[rollog] %v\n", e)
		},
	})
	if err != nil {
		return nil, err
	}
	processLogFile = w
	return io.MultiWriter(os.Stderr, w), nil
}

// ProcessLogPath 返回进程日志文件的绝对路径（供诊断接口读取）。
// 未初始化时返回空串。
func ProcessLogPath() string {
	if processLogFile == nil {
		return ""
	}
	return processLogFile.Path()
}

// closeProcessLogger 在进程退出时刷盘并等待后台压缩收尾。
func closeProcessLogger() {
	if processLogFile == nil {
		return
	}
	if err := processLogFile.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "[rollog] 关闭日志文件失败: %v\n", err)
	}
}

// installProcessLogger 是 main 里的单点装配：打开文件、组装 handler、替换默认 logger。
// 失败直接退出（日志完全不可写属于必须 fail-fast 的启动条件）。
func installProcessLogger(dataDir string, lv *slog.LevelVar, format string, cfg workercfg.LogConfig) {
	out, err := openProcessLogger(dataDir, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[worker] 初始化进程日志失败: %v\n", err)
		os.Exit(1)
	}
	opts := &slog.HandlerOptions{Level: lv}
	if format == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(out, opts)))
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(out, opts)))
}
