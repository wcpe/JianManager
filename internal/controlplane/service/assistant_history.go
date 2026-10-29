package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 历史消息编辑与回退。
//
// ## 语义边界（用户已决策）
//
// **只回退会话内容，不撤销已发生的副作用。** 回退到「启动实例」之前，
// 那个实例仍然是启动着的。UI 必须显式提示这一点，否则用户会以为回退等于撤销。
//
// 为什么不做副作用撤销：需要为每个工具定义逆操作（重启、发命令本质上不可逆），
// 且「自动反向执行一遍写操作」本身是高危动作，应当另立 FR 并单独确认。

// ErrAssistantMessageNotEditable 该消息不可编辑（非本人会话、非 user 角色、或已被取代）。
var ErrAssistantMessageNotEditable = errors.New("该消息不可编辑")

// ErrAssistantRollbackBlocked 会话有进行中的运行，不能回退。
var ErrAssistantRollbackBlocked = errors.New("会话有进行中的运行，请先停止或等待完成")

// EditUserMessage 编辑一条用户消息（§6.2）。
//
// 只允许改**自己会话里的 user 消息**：assistant 回复是历史事实，
// tool 消息是执行结果，两者改了都等于伪造记录。
//
// **不自动重跑**：编辑后由用户显式再次发送。自动重跑会让一次编辑意外产生
// 两条回复、两次计费，且旧回复还留在历史里（用户只想要「改个错别字」）。
func (s *AssistantChatService) EditUserMessage(owner, messageID uint, content string) (*model.AssistantMessage, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("%w: 内容不能为空", ErrAssistantInvalid)
	}
	var msg model.AssistantMessage
	if err := s.db.First(&msg, messageID).Error; err != nil {
		return nil, ErrAssistantChatNotFound
	}
	// 归属校验走会话：消息本身不带 owner。
	var conv model.AssistantConversation
	if err := s.db.First(&conv, msg.ConversationID).Error; err != nil {
		return nil, ErrAssistantChatNotFound
	}
	if conv.OwnerUserID != owner {
		// 与既有口径一致：越权与不存在共用同一错误，不泄露存在性。
		return nil, ErrAssistantChatNotFound
	}
	if msg.Role != model.AssistantMessageRoleUser {
		return nil, fmt.Errorf("%w: 只能编辑用户消息", ErrAssistantMessageNotEditable)
	}
	if msg.SupersededAt != nil {
		return nil, fmt.Errorf("%w: 该消息已被回退取代", ErrAssistantMessageNotEditable)
	}
	// 内容未变则直接返回：避免把「点了一下保存」记成一次编辑（editCount 会虚高）。
	if msg.Content == content {
		return &msg, nil
	}

	updates := map[string]any{
		"content":    content,
		"edited_at":  time.Now(),
		"edit_count": msg.EditCount + 1,
	}
	// 首次编辑才写原文：之后保持最初版本，便于回答「原本写的是什么」。
	if msg.OriginalContent == nil {
		original := msg.Content
		updates["original_content"] = original
	}
	if err := s.db.Model(&model.AssistantMessage{}).Where("id = ?", msg.ID).Updates(updates).Error; err != nil {
		return nil, fmt.Errorf("保存消息失败: %w", err)
	}
	if err := s.db.First(&msg, messageID).Error; err != nil {
		return nil, ErrAssistantChatNotFound
	}
	return &msg, nil
}

// RollbackToMessage 把会话回退到某条消息（§6.3）。
//
// 该消息**之后**的消息全部标记 superseded_at（软删）；该消息本身保留。
//
// ## 为什么有活跃 run 时拒绝
//
// 正在生成的 run 仍在追加事件与消息。若此时把中间消息标记取代，
// 事件 seq 与消息 turn 会出现「新增的消息排在已取代区间之后」的错乱，
// 恢复时无法判断哪些是有效上下文。等它结束再回退是唯一安全的时机。
func (s *AssistantChatService) RollbackToMessage(owner, conversationID, messageID uint) (int64, error) {
	var conv model.AssistantConversation
	if err := s.db.First(&conv, conversationID).Error; err != nil {
		return 0, ErrAssistantChatNotFound
	}
	if conv.OwnerUserID != owner {
		return 0, ErrAssistantChatNotFound
	}
	if conv.ActiveRunID != nil {
		return 0, ErrAssistantRollbackBlocked
	}
	var anchor model.AssistantMessage
	if err := s.db.First(&anchor, messageID).Error; err != nil {
		return 0, ErrAssistantChatNotFound
	}
	if anchor.ConversationID != conversationID {
		return 0, fmt.Errorf("%w: 该消息不属于此会话", ErrAssistantInvalid)
	}

	// 以 (turn, id) 为序标记其后全部消息：turn 是轮次序号，同 turn 内按 id 定序。
	// 只用 turn 会在「一轮内多条消息（user/assistant/tool）」时误伤同轮其它消息，
	// 因此用 turn > ? OR (turn = ? AND id > ?)。
	now := time.Now()
	res := s.db.Model(&model.AssistantMessage{}).
		Where("conversation_id = ? AND superseded_at IS NULL", conversationID).
		Where("(turn > ? OR (turn = ? AND id > ?))", anchor.Turn, anchor.Turn, anchor.ID).
		Update("superseded_at", now)
	if res.Error != nil {
		return 0, fmt.Errorf("回退会话失败: %w", res.Error)
	}
	return res.RowsAffected, nil
}
