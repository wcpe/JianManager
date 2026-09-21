//go:build !windows

package daemon

import (
	"net"
	"os"
	"time"
)

// platformListen 在 Linux/macOS 上监听 Unix Socket。
// 复用前先删除可能残留的旧 socket 文件，避免「address already in use」。
func platformListen(addr string) (net.Listener, error) {
	_ = os.Remove(addr) // 忽略不存在
	return net.Listen("unix", addr)
}

// platformDial 在 Linux/macOS 上拨号到 Unix Socket。
// 用 DialTimeout 避免对端 accept 队列打满时无限阻塞（FR-456 F9）；调用方（connectLoop / 探活）
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
