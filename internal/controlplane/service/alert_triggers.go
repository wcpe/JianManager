package service

import (
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// AlertEventTriggers 把「事件驱动型」告警触发源接到 AlertDispatcher（FR-085）：
//   - 实例崩溃（订阅 EventService 的 state_change → CRASHED）
//   - 日志关键字（订阅 EventService 的 stdout/stderr 命中规则关键字）
//   - 玩家事件（订阅 PlayerEventService 的 join/quit/chat/cross_server）
//   - 备份失败（由 BackupService.onBackupFailed 钩子转入）
//
// 轮询型触发（指标阈值 / 节点离线）不在此处，由 AlertEvaluator 周期评估。
// 规则匹配（按 TargetType/TargetID/Keyword/EventMatch 过滤）在内存按事件即时进行，避免每事件查全表。
type AlertEventTriggers struct {
	db         *gorm.DB
	dispatcher *AlertDispatcher
	events     *EventService
	players    *PlayerEventService
	stopFns    []func()
}

// NewAlertEventTriggers 创建事件驱动触发器。events/players 可为 nil（缺省不订阅对应源）。
func NewAlertEventTriggers(db *gorm.DB, dispatcher *AlertDispatcher, events *EventService, players *PlayerEventService) *AlertEventTriggers {
	return &AlertEventTriggers{db: db, dispatcher: dispatcher, events: events, players: players}
}

// Start 启动各事件源订阅协程。
func (t *AlertEventTriggers) Start() {
	if t.events != nil {
		ch, unsub := t.events.Subscribe()
		t.stopFns = append(t.stopFns, unsub)
		go t.consumeInstanceEvents(ch)
	}
	if t.players != nil {
		ch, unsub := t.players.Subscribe("")
		t.stopFns = append(t.stopFns, unsub)
		go t.consumePlayerEvents(ch)
	}
	slog.Info("告警事件触发器已启动")
}

// Stop 取消所有订阅。
func (t *AlertEventTriggers) Stop() {
	for _, fn := range t.stopFns {
		fn()
	}
	t.stopFns = nil
}

// consumeInstanceEvents 消费实例事件流：崩溃 + 日志关键字。
func (t *AlertEventTriggers) consumeInstanceEvents(ch <-chan InstanceEvent) {
	for evt := range ch {
		switch evt.Type {
		case "state_change":
			t.handleStateChange(evt)
		case "stdout", "stderr":
			t.handleLogLine(evt)
		}
	}
}

// handleStateChange 实例状态变更：转入 CRASHED 时触发崩溃告警，离开 CRASHED 时恢复。
func (t *AlertEventTriggers) handleStateChange(evt InstanceEvent) {
	// data 形如 "RUNNING→CRASHED"。
	parts := strings.SplitN(evt.Data, "→", 2)
	if len(parts) != 2 {
		return
	}
	newState := parts[1]
	inst, ok := t.instanceByUUID(evt.InstanceUUID)
	if !ok {
		return
	}
	rules := t.matchRules(model.AlertTriggerInstanceCrash, inst.ID)
	for _, rule := range rules {
		key := fmt.Sprintf("instance_crash:%d:%d", rule.ID, inst.ID)
		if newState == string(model.InstanceStatusCrashed) {
			t.dispatcher.Fire(AlertTrigger{
				Rule:       rule,
				TargetID:   inst.ID,
				DedupKey:   key,
				Message:    fmt.Sprintf("实例 %s 崩溃", inst.Name),
				Resolvable: true,
			})
		} else if newState == string(model.InstanceStatusRunning) {
			t.dispatcher.Resolve(rule, key, fmt.Sprintf("实例 %s 已恢复运行", inst.Name))
		}
	}
}

// handleLogLine 实例日志行：命中规则关键字时触发瞬时告警。
func (t *AlertEventTriggers) handleLogLine(evt InstanceEvent) {
	if evt.Data == "" {
		return
	}
	inst, ok := t.instanceByUUID(evt.InstanceUUID)
	if !ok {
		return
	}
	for _, rule := range t.matchRules(model.AlertTriggerLogKeyword, inst.ID) {
		if rule.Keyword == "" || !strings.Contains(evt.Data, rule.Keyword) {
			continue
		}
		line := evt.Data
		if len(line) > 200 {
			line = line[:200]
		}
		t.dispatcher.Fire(AlertTrigger{
			Rule:       rule,
			TargetID:   inst.ID,
			DedupKey:   fmt.Sprintf("log_keyword:%d:%d:%s", rule.ID, inst.ID, rule.Keyword),
			Message:    fmt.Sprintf("实例 %s 日志命中关键字 %q：%s", inst.Name, rule.Keyword, line),
			Resolvable: false,
		})
	}
}

// consumePlayerEvents 消费玩家事件流：按规则 EventMatch 触发瞬时告警。
func (t *AlertEventTriggers) consumePlayerEvents(ch <-chan PlayerEvent) {
	for evt := range ch {
		// 仅玩家可观测事件参与告警（忽略 connected/disconnected/heartbeat）。
		sub := playerEventSubtype(evt.Type)
		if sub == "" {
			continue
		}
		instID := evt.InstanceID
		for _, rule := range t.matchRules(model.AlertTriggerPlayerEvent, instID) {
			if rule.EventMatch != "" && rule.EventMatch != sub {
				continue
			}
			t.dispatcher.Fire(AlertTrigger{
				Rule:       rule,
				TargetID:   instID,
				DedupKey:   fmt.Sprintf("player_event:%d:%d:%s", rule.ID, instID, sub),
				Message:    formatPlayerEventMessage(evt, sub),
				Resolvable: false,
			})
		}
	}
}

// OnBackupFailed 备份失败告警入口（由 BackupService 钩子调用，FR-085）。
func (t *AlertEventTriggers) OnBackupFailed(backup *model.Backup, msg string) {
	for _, rule := range t.matchRules(model.AlertTriggerBackupFailed, backup.InstanceID) {
		t.dispatcher.Fire(AlertTrigger{
			Rule:       rule,
			TargetID:   backup.InstanceID,
			DedupKey:   fmt.Sprintf("backup_failed:%d:%d", rule.ID, backup.ID),
			Message:    fmt.Sprintf("实例 #%d 备份失败：%s", backup.InstanceID, msg),
			Resolvable: false,
		})
	}
}

// matchRules 返回某触发类型下、目标匹配（全局或精确实例/节点 ID）的已启用规则。
func (t *AlertEventTriggers) matchRules(triggerType string, targetID uint) []*model.AlertRule {
	var rules []model.AlertRule
	if err := t.db.Where("enabled = ? AND trigger_type = ?", true, triggerType).Find(&rules).Error; err != nil {
		return nil
	}
	out := make([]*model.AlertRule, 0, len(rules))
	for i := range rules {
		r := &rules[i]
		// TargetID nil = 全局匹配所有目标；否则需精确匹配。
		if r.TargetID != nil && *r.TargetID != targetID {
			continue
		}
		out = append(out, r)
	}
	return out
}

// instanceByUUID 按 UUID 加载实例（ID + Name），未找到返回 false。
func (t *AlertEventTriggers) instanceByUUID(uuid string) (*model.Instance, bool) {
	if uuid == "" {
		return nil, false
	}
	var inst model.Instance
	if err := t.db.Select("id", "name", "uuid").Where("uuid = ?", uuid).First(&inst).Error; err != nil {
		return nil, false
	}
	return &inst, true
}

// playerEventSubtype 把探针事件类型映射为告警可匹配的玩家事件子类型，非玩家事件返回空。
func playerEventSubtype(eventType string) string {
	switch eventType {
	case "player_join":
		return "join"
	case "player_quit":
		return "quit"
	case "chat":
		return "chat"
	case "cross_server":
		return "cross_server"
	default:
		return ""
	}
}

// formatPlayerEventMessage 生成玩家事件告警文案。
func formatPlayerEventMessage(evt PlayerEvent, sub string) string {
	switch sub {
	case "join":
		return fmt.Sprintf("玩家 %s 加入 %s", evt.PlayerName, evt.InstanceName)
	case "quit":
		return fmt.Sprintf("玩家 %s 退出 %s", evt.PlayerName, evt.InstanceName)
	case "chat":
		return fmt.Sprintf("玩家 %s 在 %s 发言：%s", evt.PlayerName, evt.InstanceName, evt.Message)
	case "cross_server":
		return fmt.Sprintf("玩家 %s 跨服 %s→%s", evt.PlayerName, evt.FromServer, evt.ToServer)
	default:
		return fmt.Sprintf("玩家事件 %s", sub)
	}
}
