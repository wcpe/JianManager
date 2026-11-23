//go:build windows

package rollog

import (
	"os"

	"golang.org/x/sys/windows"
)

// openActiveFile 以「允许他人删除/改名」的共享模式打开活跃日志文件。
//
// # 为什么不能直接用 os.OpenFile
//
// Go 的 syscall.Open 在 Windows 上申请的共享模式是
// `FILE_SHARE_READ | FILE_SHARE_WRITE`，**不含 FILE_SHARE_DELETE**。
// 于是只要本进程持有日志句柄，其他进程（运维 rm、外部 logrotate、
// 甚至本包压缩后删除轮转档）对同名文件的 DeleteFile / MoveFile 都会
// 失败并返回 ERROR_SHARING_VIOLATION（"另一个程序正在使用此文件"）。
//
// 这不是理论问题：本包自己的 reopenIfVanishedLocked 就是为「日志被外部删掉后
// 自动重建」而写的（见 2026-10-29 真机事故），但在 Windows 上外部**根本删不掉**，
// 那条自愈路径永远不会被触发；外部 logrotate 也只能报错退出。
//
// 补上 FILE_SHARE_DELETE 后，Windows 的行为向 POSIX 靠拢：
// 别人可以删掉目录项并改名，本进程持有的句柄继续指向原 inode 直到关闭。
// 这正是 reopenIfVanishedLocked 已经假设的语义，无需改动其逻辑。
//
// 语义差异需知悉：Windows 上「删除」在有打开句柄时是**延迟删除**——
// 目录项立即不可见（Stat 报 IsNotExist），物理空间在最后一个句柄关闭后回收。
// 对日志轮转而言这与 Linux 一致：新写入落到新文件，旧数据不再被引用。
func openActiveFile(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	// GENERIC_WRITE 已含追加写所需的 FILE_APPEND_DATA（见 syscall.Open 的同类处理）；
	// 这里显式用 FILE_APPEND_DATA 而非 GENERIC_WRITE，避免误带 FILE_WRITE_DATA
	// 导致「从文件头覆盖写」的语义漂移。
	access := uint32(windows.GENERIC_READ | windows.FILE_APPEND_DATA)
	h, err := windows.CreateFile(
		p,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(h), path)
	// 保持追加语义：即使句柄以 APPEND 权限打开，也显式定位到末尾，
	// 与 os.OpenFile(..., O_APPEND) 的既有行为对齐。
	if _, err := f.Seek(0, 2); err != nil {
		_ = f.Close()
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return f, nil
}
