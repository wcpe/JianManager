//go:build !windows

package daemon

import (
	"net"
	"os"
)

// platformListen 在 Linux/macOS 上监听 Unix Socket。
// 复用前先删除可能残留的旧 socket 文件，避免「address already in use」。
func platformListen(addr string) (net.Listener, error) {
	_ = os.Remove(addr) // 忽略不存在
	return net.Listen("unix", addr)
}

// platformDial 在 Linux/macOS 上拨号到 Unix Socket。
func platformDial(addr string) (net.Conn, error) {
	return net.Dial("unix", addr)
}

// RemoveSocket 删除 Unix Socket 文件，忽略不存在错误。
func RemoveSocket(addr string) {
	_ = os.Remove(addr)
}
