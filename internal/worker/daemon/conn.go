package daemon

import (
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"strings"
)

// SocketAddr 为实例生成跨平台的通信地址。
// Linux/macOS：Unix Socket 文件路径（<pidDir>/<uuid>.sock）。
// Windows：Named Pipe 名称（\\.\pipe\jianmanager-<uuid>）。
// 返回的地址可直接传给 Listen/Dial。
func SocketAddr(pidDir, instanceUUID string) string {
	if runtime.GOOS == "windows" {
		// Named Pipe 名称限制：不含路径分隔，统一前缀 + uuid
		return `\\.\pipe\jianmanager-` + sanitizePipeName(instanceUUID)
	}
	// Unix Socket 路径有长度上限（约 108），放在 pidDir 下用 uuid 命名
	return filepath.Join(pidDir, instanceUUID+".sock")
}

// sanitizePipeName 去除 Named Pipe 名称中不允许的字符。
func sanitizePipeName(s string) string {
	s = strings.ReplaceAll(s, "\\", "")
	s = strings.ReplaceAll(s, "/", "")
	s = strings.ReplaceAll(s, ":", "")
	return s
}

// PIDFileName 返回实例 PID 文件路径。
func PIDFileName(pidDir, instanceUUID string) string {
	return filepath.Join(pidDir, instanceUUID+".pid")
}

// Listen 在指定地址监听，返回平台无关的 net.Listener。
// 具体传输（Unix Socket / Named Pipe）由平台文件实现。
func Listen(addr string) (net.Listener, error) {
	if addr == "" {
		return nil, fmt.Errorf("监听地址为空")
	}
	return platformListen(addr)
}

// Dial 拨号到指定地址。
func Dial(addr string) (net.Conn, error) {
	if addr == "" {
		return nil, fmt.Errorf("拨号地址为空")
	}
	return platformDial(addr)
}

// platformListen / platformDial / RemoveSocket 由平台文件实现：
//   - conn_unix.go    （Linux/macOS: Unix Socket）
//   - conn_windows.go （Windows: Named Pipe，基于 npipe）
