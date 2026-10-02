//go:build !windows

package acquire

import (
	"os"
	"syscall"
)

// fileIdentity 返回文件的**物理身份**（设备号 + inode），ok=false 表示该平台/该文件
// 拿不到可靠身份（调用方据此退回「尺寸 + 前缀哈希」判据）。
//
// 为什么必须看 inode 而不是只看尺寸与内容前缀（用户质疑 1，2026-10-02）：
// 轮转检测此前只有两条判据——新文件比游标短（`size < cursor`）或前 512 字节哈希变了。
// 二者都漏掉一种真实形态：**替换文件内容更大、且开头与旧文件逐字节相同**。
// MC 服务端重启后写的 latest.log 恰好长这样——版本 banner、启动参数等固定行会把开头
// 填成同一段字节。此时 size ≥ cursor 且 prefix 相同 ⇒ 判定为「未轮转」⇒ 采集器继续用
// 旧的 segmentStart/cursor 去读**新文件**，于是新文件的前 cursor 个字节被当成已读跳过
// （静默丢内容），旧文件的未读尾部则永远不会被接管。
// inode 在「同名替换」时必然改变，是唯一能无条件识破这种替换的信号。
func fileIdentity(fi os.FileInfo) (uint64, uint64, bool) {
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(stat.Dev), uint64(stat.Ino), true
}
