//go:build !windows

package daemon

import (
	"fmt"
	"net"
	"os"
	"time"
)

// unixSocketPathMax 是 Unix domain socket 路径的字节上限，按平台取值
// （见 unix_socket_path_linux.go / unix_socket_path_darwin.go）。
//
// 为什么要在监听前自查（2026-10-26 真机）：socket 路径由 <data_dir>/var/servers/<uuid>.sock
// 拼成，uuid 固定 36 字节。部署目录一深就会超限，而内核只回 "bind: invalid argument"——
// 该错误不指向根因，现场表现为实例反复 CRASHED 且无从判断，排查成本极高。
//
// 取值取舍：宁可放行让内核判定，也不要误拦。预检只在「必然失败」时提前给出可读诊断。

// platformListen 在 Linux/macOS 上监听 Unix Socket。
// 复用前先删除可能残留的旧 socket 文件，避免「address already in use」。
func platformListen(addr string) (net.Listener, error) {
	// 超限时给出指向根因的诊断，而不是把内核的 invalid argument 直接抛给上层。
	if n := len(addr); n > unixSocketPathMax {
		return nil, fmt.Errorf(
			"socket 路径过长：%d 字节，上限 %d 字节（addr=%q）。"+
				"Unix domain socket 路径受 sockaddr_un.sun_path 限制，"+
				"请缩短 data_dir 或部署目录层级",
			n, unixSocketPathMax, addr)
	}
	_ = os.Remove(addr) // 忽略不存在
	return net.Listen("unix", addr)
}

// platformDial 在 Linux/macOS 上拨号到 Unix Socket。
// 用 DialTimeout 避免对端 accept 队列打满时无限阻塞（F9）；调用方（connectLoop / 探活）
// 自带重试或外层超时。
func platformDial(addr string) (net.Conn, error) {
	return platformDialTimeout(addr, platformDialDefaultTimeout)
}

// platformDialTimeout 以超时拨号到 Unix Socket。
func platformDialTimeout(addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("unix", addr, timeout)
}

// RemoveSocket 删除 Unix Socket 文件，忽略不存在错误。
func RemoveSocket(addr string) {
	_ = os.Remove(addr)
}
