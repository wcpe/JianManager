//go:build windows

package daemon

// IgnoreBrokenPipe 在 Windows 上为空操作：Windows 无 SIGPIPE 语义，写已断管道返回错误而非终止进程，
// wrapper 天然不受管道断裂牵连（见 FR-341、sigpipe_unix.go）。
func IgnoreBrokenPipe() {}
