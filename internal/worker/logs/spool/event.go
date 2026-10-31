package spool

import (
	"encoding/json"
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// Entry 是写入缓冲的一条事件（包在 payload 里的东西）。
//
// 为什么包一层而不是直接搮 logtypes.Event：
// 缓冲需要把「事件正文」与「它属于哪个源」绑在一起。目前 Event 里已经带了
// Source（含 LogSourceID），但显式带上 Key 可以让以后改 Event 结构时不影响旧缓冲文件
// 的语义兼容。
//
// **注意**：这里的 Key 不参与 event_id 计算。event_id 仍是
// hash(log_source_id, source_generation, record_start, record_end, parser_version)，
// 与 FR-505/FR-506 的口径一致：重投时靠它在 VL 侧去重。
type Entry struct {
	// Key 源标识（event.Source.LogSourceID）。
	Key string `json:"key"`
	// Event 事件本体。
	Event logtypes.Event `json:"event"`
}

// encodeEntry 把一个事件编码成可写入缓冲的 payload。
func encodeEntry(key string, ev logtypes.Event) ([]byte, error) {
	b, err := json.Marshal(Entry{Key: key, Event: ev})
	if err != nil {
		return nil, fmt.Errorf("spool: 编码事件: %w", err)
	}
	return b, nil
}

// decodeEntry 从 payload 解出一个事件。
func decodeEntry(payload []byte) (Entry, error) {
	var e Entry
	if err := json.Unmarshal(payload, &e); err != nil {
		return Entry{}, fmt.Errorf("spool: 解码事件: %w", err)
	}
	return e, nil
}

// AppendEvent 把一个事件写入缓冲。
func (s *Spool) AppendEvent(key string, ev logtypes.Event) (uint64, error) {
	payload, err := encodeEntry(key, ev)
	if err != nil {
		return 0, err
	}
	return s.Append(payload)
}

// PeekEvents 从读位开始读最多 max 条事件（不推进读位）。
//
// 单条解码失败时返回已解出的部分与错误，**不**推进读位——调用方可选择
// Ack 成功那部分或保留全部。
func (s *Spool) PeekEvents(max int) ([]Entry, error) {
	recs, err := s.Peek(max)
	if len(recs) == 0 {
		return nil, err
	}
	out := make([]Entry, 0, len(recs))
	for _, r := range recs {
		e, derr := decodeEntry(r.Payload)
		if derr != nil {
			return out, derr
		}
		out = append(out, e)
	}
	return out, err
}
