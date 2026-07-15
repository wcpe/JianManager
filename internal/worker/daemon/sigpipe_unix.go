//go:build !windows

package daemon

import (
	"os/signal"
	"syscall"
)

// IgnoreBrokenPipe 让 wrapper 进程忽略 SIGPIPE（Unix）。
//
// wrapper 的 stdout/stderr 由父 Worker 设为 OS 管道（见 process/daemon.go 的 newSlogWriter：
// 非 *os.File 的 io.Writer 会让 exec 建管道、读端留在 Worker 内）。Worker 重启/崩溃后管道读端
// 关闭，wrapper 下一次日志写 fd 1/2 会触发 SIGPIPE——Go 运行时对 fd 1/2 的 SIGPIPE 默认**终止
// 进程**，从而拖死本应存活的 daemon 游戏服（见 ADR-003 守护进程 Wrapper、FR-341）。
//
// 忽略后，写已断的 fd 返回 EPIPE（由 slog 静默丢弃该行）而不终止进程；wrapper 存活，等待 Worker
// 经 Unix Socket 重连接管。仅影响 wrapper 子进程自身的日志流，不影响游戏服 stdin/stdout（走独立
// 管道，随 socket 重连恢复）。
func IgnoreBrokenPipe() {
	signal.Ignore(syscall.SIGPIPE)
}
