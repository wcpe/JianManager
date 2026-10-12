//go:build windows

package acquire

import "os"

// fileIdentity 在 Windows 上不提供 inode 判据：`syscall.Stat_t` 没有稳定的 inode 语义，
// 用句柄信息（BY_HANDLE_FILE_INFORMATION 的 FileIndex）才能拿到等价信号，而本包不引入
// 额外依赖。ok=false 时调用方退回「尺寸 + 前缀哈希」判据——那两条判据在 Windows 上仍然
// 生效，只是漏掉「替换文件更大且开头逐字节相同」这一种形态（见 unix 版本的说明）。
func fileIdentity(fi os.FileInfo) (uint64, uint64, bool) {
	_ = fi
	return 0, 0, false
}
