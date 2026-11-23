package process

import (
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// gbkToUTF8 将 GBK 字节解码为 UTF-8。
//
// Windows 上 cmd.exe / Java 子进程输出常为 GBK；Linux 上中文 locale 的 JVM
// 同样会按 GBK 写出（日志现场事故：`[Lodestone] �Ѱַ�ѽ�`），所以**不再限定平台**，
// 改用与 logs/normalize/charset.go 相同的 auto 判定：
//   - 合法 UTF-8 → 原样返回（绝不误判）；
//   - 非法 UTF-8 → 尝试 GB18030（GBK 超集）解码，解码结果含 U+FFFD 视为不可信并回退原数据。
//
// 受管进程的输出行经该函数的两处调用点：instanceWriter.onOutput（direct spawn）
// 与 daemon.go 的帧转发（托管实例）。
func gbkToUTF8(p []byte) []byte {
	if utf8.Valid(p) {
		return p
	}
	utf8Data, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), p)
	if err != nil || !utf8.Valid(utf8Data) {
		return p
	}
	// GB18030 解码失败字节会产生 U+FFFD；真实 GBK 中文不会。含替换符视为不可信。
	for i := 0; i+2 < len(utf8Data); i++ {
		if utf8Data[i] == 0xef && utf8Data[i+1] == 0xbf && utf8Data[i+2] == 0xbd {
			return p
		}
	}
	return utf8Data
}
