package service

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 历史会话的工具调用回显（补缺）。
//
// ## 为什么需要它
//
// 工具调用的过程只存在于 `assistant_events`（事件流），而历史消息读的是
// `assistant_messages`。于是用户回看旧会话时**看不到当时调了哪些工具、结果如何**——
// 而数据其实一直在库里。这是功能缺失，不是验收项。
//
// ## 为什么按消息聚合而不是回放事件流
//
// 事件流是「过程」（text_delta 逐帧、usage、run_end…），消息是「结果」。
// 历史页要的是「这条回复里发生了哪几次工具调用、各自什么状态」，
// 因此按 `message_id` 聚合三类工具事件（tool_call / tool_status / tool_result），
// 用 callId 串起来——与前端实时视图的聚合口径一致（§5）。
//
// ## 为什么回放要复用同一个 callId 归并规则
//
// 若历史侧另写一套归并，迟早与实时侧漂移（例如一边按 seq、一边按到达顺序），
// 表现为「同一轮对话，实时看是一个状态、历史看是另一个」。

// AssistantToolCallRecord 一条工具调用的历史快照。
type AssistantToolCallRecord struct {
	CallID string `json:"callId"`
	// Name 工具名。
	Name string `json:"name"`
	// Status 最终状态：running / pending_confirmation / ok / failed。
	// 未收到 status 事件时按「有结果=ok、无结果=running」推断，见下方注释。
	Status string `json:"status"`
	// ErrorCode 失败时的分类码（后端 ai.Classify 的结果）。
	ErrorCode string `json:"errorCode,omitempty"`
	// ArgsJSON 调用参数（原样，已由事件层限长）。
	ArgsJSON string `json:"argsJson,omitempty"`
	// Preview 结果预览（已由事件层按 2048 rune 截断过一次）。
	Preview string `json:"preview,omitempty"`
	// Truncated 结果是否被截断。
	Truncated bool `json:"truncated,omitempty"`
	// Seq 首次出现的 seq，供前端稳定排序（同一轮内可能多次调用）。
	Seq int `json:"seq"`
}

// ListMessageToolCalls 返回某会话内「按 message_id 分组」的工具调用记录。
//
// 只回有工具调用的消息；没有工具调用的消息不出现在 map 里（前端据此跳过渲染）。
func (s *AssistantChatService) ListMessageToolCalls(owner, conversationID uint) (map[uint][]AssistantToolCallRecord, error) {
	if _, err := s.GetConversation(owner, conversationID); err != nil {
		return nil, err
	}

	// 只取三类与工具相关的事件；text_delta/usage 与工具回显无关，拉进来只是浪费。
	var events []model.AssistantEvent
	if err := s.db.
		Where("conversation_id = ?", conversationID).
		Where("type IN ?", []string{
			model.AssistantEventTypeToolCall,
			model.AssistantEventTypeToolStatus,
			model.AssistantEventTypeToolResult,
		}).
		Order("run_id asc, seq asc").
		Find(&events).Error; err != nil && err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("查询工具事件失败: %w", err)
	}

	// 聚合：按 (messageID, callID) 归并。
	//
	// 为什么键里要带 messageID：同一会话里 callId 理论上唯一，但 messageID 是
	// 「这条调用属于哪条回复」的锚点——没有它就无法把记录挂到正确消息下。
	type key struct {
		msgID  uint
		callID string
	}
	order := make([]key, 0, len(events))
	byCall := make(map[key]*AssistantToolCallRecord, len(events))

	touch := func(msgID uint, callID string, seq int) *AssistantToolCallRecord {
		k := key{msgID: msgID, callID: callID}
		rec, ok := byCall[k]
		if !ok {
			rec = &AssistantToolCallRecord{CallID: callID, Seq: seq}
			byCall[k] = rec
			order = append(order, k)
		}
		return rec
	}

	for _, ev := range events {
		// tool_call 事件带 messageID（锚点），status/result 只带 callID——
		// 所以后两者要用「已记录的 callID → msgID」映射回填。
		var data map[string]any
		if err := json.Unmarshal([]byte(ev.Data), &data); err != nil {
			continue // 单条坏载荷不该让整次历史读取失败
		}
		callID, _ := data["callId"].(string)
		if callID == "" && ev.CallID != nil {
			callID = *ev.CallID
		}
		if callID == "" {
			continue
		}

		msgID := uint(0)
		if ev.MessageID != nil {
			msgID = *ev.MessageID
		} else {
			// 无锚点：回填到「同 callID 且已带锚点」的那条。
			for k := range byCall {
				if k.callID == callID {
					msgID = k.msgID
					break
				}
			}
		}
		if msgID == 0 {
			// 既无锚点也无同 callId 的锚点记录：无法归到任何消息，跳过。
			continue
		}

		rec := touch(msgID, callID, ev.Seq)
		switch ev.Type {
		case model.AssistantEventTypeToolCall:
			if name, ok := data["name"].(string); ok {
				rec.Name = name
			}
			if raw, ok := data["argsJson"]; ok {
				if blob, err := json.Marshal(raw); err == nil {
					rec.ArgsJSON = string(blob)
				}
			}
			// 有了调用但还没有状态：先标 running（与实时视图一致）。
			if rec.Status == "" {
				rec.Status = "running"
			}
		case model.AssistantEventTypeToolStatus:
			if st, ok := data["status"].(string); ok && st != "" {
				rec.Status = st
			}
			if code, ok := data["errorCode"].(string); ok {
				rec.ErrorCode = code
			}
		case model.AssistantEventTypeToolResult:
			if prev, ok := data["preview"].(string); ok {
				rec.Preview = prev
			}
			if tr, ok := data["truncated"].(bool); ok {
				rec.Truncated = tr
			}
			// 有结果但没收到终态 status：按 ok 推断。
			//
			// 为什么允许推断：历史数据可能来自 status 事件落库失败/截断的轮次，
			// 而「有结果」本身就是执行完成的证据。这比显示 running（永远转圈）诚实。
			if rec.Status == "" || rec.Status == "running" {
				rec.Status = "ok"
			}
		}
	}

	out := make(map[uint][]AssistantToolCallRecord, len(byCall))
	for _, k := range order {
		out[k.msgID] = append(out[k.msgID], *byCall[k])
	}
	return out, nil
}
