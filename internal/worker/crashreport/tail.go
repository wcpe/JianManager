// Package crashreport 组装并上报实例崩溃快照（FR-313）：
// 进程非正常退出时把退出码/信号/时长 + 终端环形缓冲的尾部输出，
// 经与注册/心跳同信道的 gRPC ReportCrashSnapshot 上报 CP 持久化。
package crashreport

const (
	// DefaultTailLines 尾部输出截取行数上限（N=200，写死不做配置，见 spec §6）。
	DefaultTailLines = 200
	// DefaultTailBytes 尾部输出截取字节上限（64KB，与终端环形缓冲容量对齐）。
	DefaultTailBytes = 64 * 1024
)

// Tail 从输出缓冲截取尾部：最后 maxLines 行、至多 maxBytes 字节（不足取全部）。
// 先按字节上限从头裁剪，再从尾部反向数换行取最后 maxLines 行；末尾的结尾换行符
// 不计为一行（"a\nb\n" 是 2 行不是 3 行）。maxLines<=0 表示不按行数限制。
func Tail(data []byte, maxLines, maxBytes int) string {
	if maxBytes > 0 && len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	if maxLines <= 0 || len(data) == 0 {
		return string(data)
	}
	end := len(data)
	if data[end-1] == '\n' {
		end--
	}
	lines := 0
	for i := end - 1; i >= 0; i-- {
		if data[i] == '\n' {
			lines++
			if lines == maxLines {
				return string(data[i+1:])
			}
		}
	}
	return string(data)
}
