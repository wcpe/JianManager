package process

import "os/exec"

// setDetachedAttr 让子进程脱离当前进程组。
// daemon wrapper 必须脱离 Worker 进程组：Worker 退出/重启时 wrapper 继续存活，
// 由 wrapper 托管的 Java 游戏服不受影响（ADR-003 进程隔离目标）。
// 具体实现由平台文件提供：
//   - detach_unix.go    （Linux/macOS: Setsid 新建会话）
//   - detach_windows.go （Windows: CREATE_NEW_PROCESS_GROUP）
func setDetachedAttr(cmd *exec.Cmd) {
	setDetachedAttrPlatform(cmd)
}
