package daemon

import (
	"encoding/json"
	"os"
	"syscall"
)

// EventJavaExit 是 ExitEvent.Event 的取值：被托管进程非正常退出（FR-313）。
// 事件字段留作区分未来其他 wrapper 事件，Worker 侧按值过滤、未知事件忽略。
const EventJavaExit = "java_exit"

// ExitEvent 是 wrapper 检测到被托管进程非正常退出时，经控制通道（ChannelControl +
// TypeEvent）上抛给 Worker 的事件负载（JSON，FR-313）。daemon 模式下进程由 wrapper
// 直接托管，退出码/信号只有 wrapper 能拿到，须经此事件转交 Worker 组装崩溃快照。
type ExitEvent struct {
	// Event 事件类型，恒为 EventJavaExit。
	Event string `json:"event"`
	// ExitCode 进程退出码；无法获知时为 -1。
	ExitCode int `json:"exit_code"`
	// Signal 终止信号名（Unix，如 killed/terminated）；Windows / 非信号退出为空。
	Signal string `json:"signal,omitempty"`
	// DurationMs 本次运行时长（毫秒）。
	DurationMs int64 `json:"duration_ms"`
}

// EncodeExitEventFrame 把退出事件编码为控制通道事件帧。
func EncodeExitEventFrame(ev ExitEvent) (*Frame, error) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	return &Frame{
		Header:  Header{Channel: ChannelControl, Type: TypeEvent},
		Payload: payload,
	}, nil
}

// DecodeExitEvent 从事件帧负载解析退出事件；非 java_exit 事件返回 ok=false（忽略）。
func DecodeExitEvent(payload []byte) (ExitEvent, bool) {
	var ev ExitEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return ExitEvent{}, false
	}
	if ev.Event != EventJavaExit {
		return ExitEvent{}, false
	}
	return ev, true
}

// ExitInfo 从进程退出状态提取退出码与信号名（FR-313）。
// ps 为 nil（Wait 出错且无退出状态）时退出码取 -1。信号仅 Unix 有意义：被信号终止时
// 返回 syscall.Signal 的字符串形式（如 killed/terminated）；Windows 的 WaitStatus
// Signaled() 恒为 false，信号恒为空（与 spec「Windows 信号留空」一致）。
func ExitInfo(ps *os.ProcessState) (exitCode int, signal string) {
	if ps == nil {
		return -1, ""
	}
	exitCode = ps.ExitCode()
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		signal = ws.Signal().String()
	}
	return exitCode, signal
}
