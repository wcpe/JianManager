//go:build windows

package daemon

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	// Windows 的读句柄默认不共享删除，原子替换与短暂读冲突时需有界重试。
	pidFileRetryAttempts = 20
	pidFileRetryDelay    = 5 * time.Millisecond
)

// readPIDFile 处理 Windows 原子替换期间的短暂共享冲突，避免把完整记录误判为不可读。
func readPIDFile(path string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < pidFileRetryAttempts; attempt++ {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !isPIDFileSharingError(err) || attempt == pidFileRetryAttempts-1 {
			break
		}
		time.Sleep(pidFileRetryDelay)
	}
	return nil, lastErr
}

// renamePIDFile 处理 Windows 目标文件仍被读取时的短暂替换冲突。
func renamePIDFile(oldPath, newPath string) error {
	var lastErr error
	for attempt := 0; attempt < pidFileRetryAttempts; attempt++ {
		err := os.Rename(oldPath, newPath)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isPIDFileSharingError(err) || attempt == pidFileRetryAttempts-1 {
			break
		}
		time.Sleep(pidFileRetryDelay)
	}
	return lastErr
}

// isPIDFileSharingError 只把 Windows 文件共享/锁冲突视为可重试错误。
func isPIDFileSharingError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
