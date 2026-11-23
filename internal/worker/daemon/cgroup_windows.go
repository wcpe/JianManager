//go:build windows

package daemon

import "errors"

// Windows 无 systemd/cgroup2 语义：Worker 由服务管理器（SCM）托管，重启不按 cgroup 连坐子进程，
// 故这里一律空操作——保持与 Unix 相同的函数签名，让调用方无需分支。

// errNoCgroup2 表示本平台无 cgroup2 语义。cgroup.go（跨平台）中的函数变量以此作为默认实现，
// 保证这些变量在所有平台都能初始化（此前 Windows 构建因缺该默认实现而失败）。
var errNoCgroup2 = errors.New("本平台无 cgroup2 语义")

// readOwnCgroupPath 在 Windows 上无 cgroup2 层级可读，恒失败（调用方据此跳过隔离逻辑）。
func readOwnCgroupPath() (string, error) { return "", errNoCgroup2 }

// queryWorkerUnitKillMode 在 Windows 上无 systemd 单元属性，恒失败（调用方据此跳过自检）。
func queryWorkerUnitKillMode(string, bool) (string, bool) { return "", false }

// EscapeWorkerUnitCgroup 在 Windows 上无事可做，恒返回未迁移。
func EscapeWorkerUnitCgroup(instanceUUID string) (string, error) { return "", nil }

// MigratePIDTreeToCgroup 在 Windows 上无事可做。
func MigratePIDTreeToCgroup(rootPID int, instanceUUID string) (int, error) { return 0, nil }

// RemoveDaemonCgroupDir 在 Windows 上无事可做。
func RemoveDaemonCgroupDir(instanceUUID string) {}

// SweepEmptyDaemonCgroupDirs 在 Windows 上无事可做。
func SweepEmptyDaemonCgroupDirs() {}

// WarnIfUnitKillModeUnprotected 在 Windows 上无事可做：Worker 由 SCM 托管，
// 停止服务不按 cgroup 连坐子进程，不存在该告警的问题面。
func WarnIfUnitKillModeUnprotected() {}
