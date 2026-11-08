//go:build linux || darwin

package tailer

import "os"

// inodeOf 从 FileInfo 取 inode 与 device（Unix 系）。
//
// 这些值**只用于诊断与指纹不可用时的兜底**，文件身份以指纹为准
// （理由见 FileIdentity 的注释：inode 会被复用）。
func inodeOf(fi os.FileInfo) (inode, dev uint64, ok bool) {
	st, ok := fi.Sys().(*syscallStatT)
	if !ok || st == nil {
		return 0, 0, false
	}
	return uint64(st.Ino), uint64(st.Dev), true
}
