package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/rollog"
)

// 平台进程日志的文件后端。
//
// # 为什么需要它
//
// 此前 CP 只写 stdout（initLogger 里的 os.Stdout），文件完全由外部重定向产生
// （systemd journald 或 `nohup >> cp.log`）。这带来两个实际问题：
//   - 面板读不到：诊断接口无法定位文件，因为进程自己都不知道写到了哪；
//   - 轮转昂贵：外部重定向下 logrotate 只能 copytruncate，每次轮转要读写整个文件。
//
// 进程自己持有文件后，路径确定（诊断接口可读），轮转是一次 rename（O(1)、零复制）。
// stdout 仍然保留（io.MultiWriter），journald/容器场景不受影响。

// processLogPath 保存当前进程日志的绝对路径，供诊断路由读取。
// 用 atomic 是因为诊断请求来自 HTTP 协程，而写入在启动早期。
var processLogPath atomic.Value

// processLogWriter 保存写入器，用于优雅退出时关闭（等后台 gzip 收尾）。
var processLogWriter *rollog.Writer

// installPlatformLogger 是 CP 日志的单点装配：打开文件 + 组装 handler。
//
// 这里用 dataroot.Resolve 而不是 dataroot.Init（后者在 main 稍后才调用）：
// Resolve 只算路径不建目录，而日志要先于一切初始化——启动早期的错误
// （包括“数据根不可写”）本身就要能落到文件上。rollog.Open 会自建父目录。
func installPlatformLogger(cfg config.LogConfig) {
	config.LogLevelVar.Set(config.ParseLogLevel(cfg.Level))

	var out io.Writer = os.Stdout
	if root, err := dataroot.Resolve(os.Getenv(dataroot.EnvVar)); err != nil {
		fmt.Fprintf(os.Stderr, "[cp] 解析数据根失败（仅写 stdout）: %v\n", err)
	} else {
		path := filepath.Join(root.LogDir(), "control-plane.log")
		w, openErr := rollog.Open(rollog.Config{
			Path: path,
			OnError: func(e error) {
				// 刻意不经 slog：日志组件自身的错误写回 slog 会递归。
				fmt.Fprintf(os.Stderr, "[rollog] %v\n", e)
			},
		})
		if openErr != nil {
			// 文件打不开不是致命错误：stdout 仍可用，不能因为日志文件就让 CP 起不来。
			fmt.Fprintf(os.Stderr, "[cp] 打开进程日志文件失败（仅写 stdout）: %v\n", openErr)
		} else {
			processLogWriter = w
			processLogPath.Store(path)
			out = io.MultiWriter(os.Stdout, w)
		}
	}

	opts := &slog.HandlerOptions{Level: config.LogLevelVar}
	if cfg.Format == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(out, opts)))
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(out, opts)))
}

// platformLogPath 返回平台进程日志文件路径串（诊断路由用）。空串 = 未启用文件。
func platformLogPath() string {
	if v, ok := processLogPath.Load().(string); ok {
		return v
	}
	return ""
}

// closePlatformLogger 在进程退出时刷盘并等后台压缩收尾。
func closePlatformLogger() {
	if processLogWriter == nil {
		return
	}
	if err := processLogWriter.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "[rollog] 关闭日志文件失败: %v\n", err)
	}
}
