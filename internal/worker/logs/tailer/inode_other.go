//go:build !linux && !darwin

package tailer

import "os"

// inodeOf 在 Windows 等平台不可用。
//
// 返回 ok=false 让调用方知道「拿不到 inode」，而不是返回 0——
// 返回 0 会让不同文件看起来是同一个身份。
//
// 本项目的生产环境是 Linux（系统服务），Windows 只用于开发；
// 且文件身份以指纹为准，拿不到 inode 不影响正确性。
func inodeOf(fi os.FileInfo) (inode, dev uint64, ok bool) {
	return 0, 0, false
}
