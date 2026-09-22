package service

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// HealthAlertNotifier 把实例健康熔断告警投递为站内信（FR-459 T4「发站内信告警」）。
// 实现 grpc.HealthAlertNotifier（接口以原始类型声明，无需 service↔grpc 结构化耦合）。
type HealthAlertNotifier struct {
	db            *gorm.DB
	notifications *NotificationService
}

// NewHealthAlertNotifier 创建熔断告警投递器。
func NewHealthAlertNotifier(db *gorm.DB, notifications *NotificationService) *HealthAlertNotifier {
	return &HealthAlertNotifier{db: db, notifications: notifications}
}

// NotifyInstanceCircuitBroken 给所有启用的平台管理员发一条熔断告警站内信。
// 尽力而为：查询/写入失败仅记日志，绝不反向影响心跳链路。
func (h *HealthAlertNotifier) NotifyInstanceCircuitBroken(nodeUUID, instanceUUID, reason string) {
	if h == nil || h.db == nil || h.notifications == nil {
		return
	}
	instName, nodeName := instanceUUID, nodeUUID
	var inst model.Instance
	if err := h.db.Select("name", "node_id").Where("uuid = ?", instanceUUID).First(&inst).Error; err == nil {
		instName = inst.Name
		var node model.Node
		if err := h.db.Select("name").Where("id = ?", inst.NodeID).First(&node).Error; err == nil && node.Name != "" {
			nodeName = node.Name
		}
	}

	var adminIDs []uint
	if err := h.db.Model(&model.User{}).
		Where("role = ? AND status = ?", model.RolePlatformAdmin, model.UserStatusActive).
		Pluck("id", &adminIDs).Error; err != nil {
		slog.Warn("熔断告警：查询平台管理员失败", "error", err)
		return
	}
	if len(adminIDs) == 0 {
		slog.Warn("熔断告警：无启用的平台管理员可投递", "instanceUUID", instanceUUID)
		return
	}

	title := fmt.Sprintf("实例熔断告警：%s", instName)
	body := fmt.Sprintf(
		"实例 %s（节点 %s）持续崩溃，窗口内自动重启次数超限，已停止自动重启并置 CRASHED，等待人工确认。原因：%s",
		instName, nodeName, reason)
	for _, uid := range adminIDs {
		if err := h.notifications.Create(uid, model.NotificationLevelWarning, title, body, ""); err != nil {
			slog.Warn("熔断告警站内信投递失败", "userId", uid, "instanceUUID", instanceUUID, "error", err)
		}
	}
}
