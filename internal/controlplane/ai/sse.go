package ai

import (
	"bufio"
	"io"
	"strings"
)

// sseFrame 一个 SSE 事件帧（已按空行聚合完毕）。
type sseFrame struct {
	// Event `event:` 字段（部分协议用它区分事件类型，缺省为空）。
	Event string
	// Data `data:` 字段已按规范用 \n 拼接多行。
	Data string
}

// sseReader 逐帧读取 SSE。
//
// 为什么自己写而不引库：需要的语义很少（按行读到空行聚合 event/data、忽略注释与
// 其它字段、支持 [DONE] 哨兵），而第三方客户端往往绑定自己的传输与重试策略。
//
// 规范细节：`data:` 后若紧跟一个空格要去掉；一个事件可有多行 data，用 \n 连接；
// 空行表示帧结束；`:` 开头是注释，直接忽略。
type sseReader struct {
	sc *bufio.Scanner
}

// newSSEReader 构造 SSE 读取器。
//
// 缓冲区放大到 1MiB：工具参数与长文本会以很长的单行 data 出现，默认 64KiB 的
// Scanner 上限会让「正常但很长的一帧」触发 bufio.Scanner: token too long 这种
// 看起来像网络错误、实际是解析器上限的故障。
func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &sseReader{sc: sc}
}

// Next 读取下一帧。ok=false 表示流结束或读取失败（err 非 nil 时）。
func (s *sseReader) Next() (frame sseFrame, ok bool, err error) {
	var (
		dataLines []string
		hasData   bool
	)
	for s.sc.Scan() {
		line := s.sc.Text()
		if line == "" {
			// 空行：若已累积数据则这一帧结束；否则是连续空行，跳过。
			if hasData {
				frame.Data = strings.Join(dataLines, "\n")
				return frame, true, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // 注释行（部分服务用它做心跳）。
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			// 无冒号的行按规范视为字段名 + 空值，本场景无意义，忽略。
			continue
		}
		// 规范：冒号后紧跟的单个空格要去掉。
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			frame.Event = value
		case "data":
			dataLines = append(dataLines, value)
			hasData = true
		}
	}
	if scanErr := s.sc.Err(); scanErr != nil {
		return sseFrame{}, false, scanErr
	}
	// 流结束时若仍有未以空行闭合的数据，按最后一帧返回（部分服务不发尾随空行）。
	if hasData {
		frame.Data = strings.Join(dataLines, "\n")
		return frame, true, nil
	}
	return sseFrame{}, false, nil
}
