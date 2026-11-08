//go:build linux || darwin

package tailer

import "syscall"

// syscallStatT 是各平台 Stat_t 的别名。
//
// 为什么用别名而不是直接写 syscall.Stat_t：
// linux 与 darwin 的字段名都是 Ino/Dev，但类型不同（darwin 上 Dev 是 int32）。
// 集中在这里做一次转换，inode_unix.go 里就能写一套代码。
type syscallStatT = syscall.Stat_t
