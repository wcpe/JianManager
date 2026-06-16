package process

import (
	"runtime"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// gbkToUTF8 将 GBK 字节解码为 UTF-8（Windows 上 cmd.exe / Java 子进程输出常为 GBK）。
// 非 Windows 平台直接返回原数据。解码失败回退原数据（可能本身就是 UTF-8）。
func gbkToUTF8(p []byte) []byte {
	if runtime.GOOS != "windows" {
		return p
	}
	utf8Data, _, err := transform.Bytes(simplifiedchinese.GBK.NewDecoder(), p)
	if err != nil {
		return p
	}
	return utf8Data
}
