//go:build windows

package eventstore

import "os"

func syncDirectoryHandle(_ *os.File) error {
	// Windows 的只读目录句柄不能获得可移植的刷新保证，不能借此宣称目录项断电耐久。
	// 共享入口仍验证目录打开、类型和关闭；段/清单文件同步、原子改名的真实失败继续传播。
	return nil
}
