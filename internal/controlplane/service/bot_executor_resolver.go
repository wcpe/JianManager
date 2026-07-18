package service

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"gorm.io/gorm"
)

// BotExecutorResolver 统一解析 Bot 的实际执行节点。
// 参见 ADR-074：ExecutorNodeID 非空时必须优先于目标实例所属节点。
type BotExecutorResolver struct {
	db *gorm.DB
}

// NewBotExecutorResolver 创建执行节点解析器。
func NewBotExecutorResolver(db *gorm.DB) *BotExecutorResolver {
	return &BotExecutorResolver{db: db}
}

// Resolve 返回实际执行节点与目标实例。
func (r *BotExecutorResolver) Resolve(bot *model.Bot) (*model.Node, *model.Instance, error) {
	instance, err := r.resolveInstance(bot)
	if err != nil {
		return nil, nil, err
	}
	if bot.ExecutorNodeID == nil {
		return &instance.Node, instance, nil
	}
	if bot.ExecutorNode != nil && bot.ExecutorNode.ID == *bot.ExecutorNodeID {
		return bot.ExecutorNode, instance, nil
	}
	var node model.Node
	if err := r.db.First(&node, *bot.ExecutorNodeID).Error; err != nil {
		return nil, nil, fmt.Errorf("查询 Bot 执行节点失败: %w", err)
	}
	bot.ExecutorNode = &node
	return bot.ExecutorNode, instance, nil
}

func (r *BotExecutorResolver) resolveInstance(bot *model.Bot) (*model.Instance, error) {
	if bot.Instance.ID == bot.InstanceID && bot.Instance.Node.ID == bot.Instance.NodeID && bot.Instance.Node.ID != 0 {
		return &bot.Instance, nil
	}
	var instance model.Instance
	if err := r.db.Preload("Node").First(&instance, bot.InstanceID).Error; err != nil {
		return nil, fmt.Errorf("查询 Bot 目标实例失败: %w", err)
	}
	bot.Instance = instance
	return &bot.Instance, nil
}
