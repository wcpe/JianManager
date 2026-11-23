//go:build !windows

package daemon

import "os"

// readPIDFile 读取 PID 文件；POSIX 下原子替换不会与读句柄冲突。
func readPIDFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// renamePIDFile 在 POSIX 下直接执行同目录原子替换。
func renamePIDFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
