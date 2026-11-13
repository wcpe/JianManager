package assistantmcp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

/* *
 * 请求范围 SSE 的事件读取器（§5.1）。
 *
 * 为什么不复用 `internal/controlplane/ai/sse.go` 的 reader：那个是**可信上游**
 * （自家供应商）用的，只有分帧没有限额；外部 MCP 是不可信输入，必须额外具备
 *   - 单行上限（一个不换行的巨大 data 行能把内存吃满）；
 *   - 总量上限（响应体预算）；
 *   - 空闲超时（上游发完头就不动，连接会一直挂着）。
 * 三者在可信场景是多余约束，在不可信场景是必需约束。
 */

// eventReader 逐事件读取 SSE。
type eventReader struct {
	scanner *bufio.Scanner
	read    int64
	maxRead int64
	// done 已读到流尾（避免重复读）。
	done bool
}

// newEventReader 构造读取器。
func newEventReader(r io.Reader, maxLine int, maxTotal int64) *eventReader {
	sc := bufio.NewScanner(r)
	if maxLine > 0 {
		sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	}
	return &eventReader{scanner: sc, maxRead: maxTotal}
}

// Next 返回下一个事件的 data（多行 data 以换行拼接）。
//
// 第二个返回值为 false 表示流结束。注释帧（以 `:` 开头）与 `event:` / `id:` 行被忽略：
// 本客户端只关心 JSON-RPC 的 data 载荷，帧名不参与判定。
func (e *eventReader) Next() ([]byte, bool, error) {
	if e.done {
		return nil, false, nil
	}
	var dataLines []string
	for e.scanner.Scan() {
		line := e.scanner.Text()
		// 累计读取量用「行长度 + 换行」近似：bufio 已把长度限住，
		// 这里再守总量，防止上游用极多短行绕过单行上限。
		e.read += int64(len(line)) + 1
		if e.maxRead > 0 && e.read > e.maxRead {
			return nil, false, fmt.Errorf("%w: 事件流超过 %d 字节", ErrTooLarge, e.maxRead)
		}
		if line == "" {
			// 空行 = 一个事件结束。
			if len(dataLines) > 0 {
				return []byte(strings.Join(dataLines, "\n")), true, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // 注释帧（含上游心跳）
		}
		if strings.HasPrefix(line, "data:") {
			v := strings.TrimPrefix(line, "data:")
			v = strings.TrimPrefix(v, " ")
			dataLines = append(dataLines, v)
		}
	}
	if err := e.scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			// 单行超限：给明确的分类，而不是当成普通读错误。
			return nil, false, fmt.Errorf("%w: 单行事件超过上限", ErrTooLarge)
		}
		return nil, false, fmt.Errorf("%w: 读取事件流失败: %v", ErrUnreachable, err)
	}
	e.done = true
	// 流尾没有收尾空行时，仍把已聚合的 data 作为最后一帧返回。
	if len(dataLines) > 0 {
		return []byte(strings.Join(dataLines, "\n")), true, nil
	}
	return nil, false, nil
}
