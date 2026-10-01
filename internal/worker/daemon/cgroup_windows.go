//go:build windows

package daemon

// Windows 无 systemd/cgroup2 语义：Worker 由服务管理器（SCM）托管，重启不按 cgroup 连坐子进程，
// 故这里一律空操作——保持与 Unix 相同的函数签名，让调用方无需分支。

// EscapeWorkerUnitCgroup 在 Windows 上无事可做，恒返回未迁移。
func EscapeWorkerUnitCgroup(instanceUUID string) (string, error) { return "", nil }

// MigratePIDTreeToCgroup 在 Windows 上无事可做。
func MigratePIDTreeToCgroup(rootPID int, instanceUUID string) (int, error) { return 0, nil }

// RemoveDaemonCgroupDir 在 Windows 上无事可做。
func RemoveDaemonCgroupDir(instanceUUID string) {}

// SweepEmptyDaemonCgroupDirs 在 Windows 上无事可做。
func SweepEmptyDaemonCgroupDirs() {}
