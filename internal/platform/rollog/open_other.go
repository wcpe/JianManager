//go:build !windows

package rollog

import "os"

// openActiveFile 打开（必要时创建）活跃日志文件并定位到末尾。
//
// POSIX 上 os.OpenFile 的语义已经满足全部需求：O_APPEND 保证每次写入追加到末尾，
// 且「删除已打开的文件」本就合法（目录项立即消失，句柄继续有效直到关闭），
// 因此不需要像 Windows 分支那样额外申请共享删除权限。
// 保留这个薄封装是为了让两平台共用同一处调用点，避免两份打开逻辑漂移。
func openActiveFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}
