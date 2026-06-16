//go:build windows

package daemon

import (
	"net"
	"time"

	npipe "gopkg.in/natefinch/npipe.v2"
)

// platformListen 在 Windows 上监听 Named Pipe。
// npipe.Listen 返回的 listener 实现 net.Listener。
func platformListen(addr string) (net.Listener, error) {
	return npipe.Listen(addr)
}

// platformDial 在 Windows 上拨号到 Named Pipe。
// 用 DialTimeout 避免在管道未就绪时无限阻塞（npipe.Dial 默认 WaitNamedPipe 超时为无限）。
// 调用方（daemonStrategy.connectLoop）负责重试。
func platformDial(addr string) (net.Conn, error) {
	return npipe.DialTimeout(addr, 2*time.Second)
}

// RemoveSocket 在 Windows 上是空操作：Named Pipe 无对应文件。
func RemoveSocket(addr string) {}
