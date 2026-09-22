package service

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// AlertTrigger 一次告警触发的语义描述，由各触发源（指标评估器 / 事件驱动监听器）产生，
// 交由 AlertDispatcher 统一处理去抖聚合、静默、分级路由与落库（FR-085）。
type AlertTrigger struct {
	Rule     *model.AlertRule
	TargetID uint
	// DedupKey 去抖键：同一键在窗口内复发聚合为同一活跃事件。通常为 rule.ID + target + 触发标识。
	DedupKey string
	Value    float64
	// Direction 结构化偏离方向（up|down），仅基线触发使用；空串表示无方向语义。
	Direction string
	Message   string
	// Resolvable 标记该触发是否可恢复（metric/instance_crash/node_offline 可；日志/玩家事件为瞬时，不可恢复）。
	Resolvable bool
	// DeliverAsync 为 true 时把**外部通道投递**（webhook/IM/邮件的 HTTP/SMTP 调用）移到后台队列，
	// `Fire` 立即返回。默认 false = 内联投递（既有行为）。
	//
	// M-8：由**用户请求驱动**的触发源必须置 true。`CapacityTrendAlerter.Notify` 挂在
	// `GET /metrics/capacity/forecast` 上，而 `ChannelNotifier.client` 超时 10s——
	// 内联投递时该 GET 的最坏耗时可被外部 webhook 拖住 10s，且 gin handler 全程阻塞；
	// 前端 60s 轮询下会持续占用连接。
	//
	// 为什么**只**异步投递、而落库/去抖判定仍留在调用线程（这是「不重复发」的关键）：
	// 去抖依赖 `dueForNotify` 读上一事件 + `Fire` 写新事件这一对操作，两者必须串行；
	// 若把整个 `Fire` 丢进 goroutine，两次并发 GET 会**都**通过 dueForNotify 检查
	// （都还没写库）→ 落两条事件 + 外发两次通知。故队列只承载「已经决定要发」的投递动作，
	// 它与去抖判定是 1:1 的（一次 Fire 最多入队一次），不引入重复。
	DeliverAsync bool
}

// alertNotifyQueueDepth 异步投递队列深度。
//
// 深度取 256 的依据：队列的自然上限由去抖决定——同一 (规则, 目标, 指标) 每
// `capacityReNotifyInterval`（默认 6h）最多投递一次，而单条投递最长 10s
// （`ChannelNotifier` 的超时），故消费速率 ≥ 0.1 条/秒，而生产速率被目标数上限约束。
// 256 相当于「≥42 分钟的投递积压」才有机会填满，实际不可达；留出深度是为了在
// 突发（如全局规则 × 大量目标首轮命中）时不阻塞调用方又不丢通知。
const alertNotifyQueueDepth = 256

// notifyJob 一次待投递的外部通知。**按值持有规则副本**：调用方的 `*model.AlertRule`
// 可能指向其局部切片（如 `CapacityTrendAlerter.Notify` 里 `Find` 出来的临时 `rules`），
// 跨 goroutine 持有指针会与调用方的后续使用形成数据竞争。投递只读取规则字段，浅拷贝足够。
type notifyJob struct {
	rule model.AlertRule
	note AlertNotification
}

// AlertDispatcher 告警分发器（FR-085）。所有触发源经此统一落库 + 通知，
// 集中实现去抖聚合（dedup window）、静默窗口（silence window）、分级路由（按 rule.ChannelIDs → 通道）、
// 恢复通知与站内通知。无状态于内存（聚合状态以 DB 活跃事件为准），可被多触发源并发调用。
type AlertDispatcher struct {
	db       *gorm.DB
	notifier *ChannelNotifier
	// now 注入式时钟，便于测试静默窗口。
	now func() time.Time

	// notifyCh 异步投递队列（`AlertTrigger.DeliverAsync=true` 时使用）。
	// 懒启动：首次入队时才起 worker，未使用异步投递的分发器不占 goroutine。
	notifyCh     chan notifyJob
	notifyStart  sync.Once
	notifyStopCh chan struct{}
	notifyWG     sync.WaitGroup
}

// NewAlertDispatcher 创建告警分发器。
func NewAlertDispatcher(db *gorm.DB) *AlertDispatcher {
	return &AlertDispatcher{
		db:       db,
		notifier: NewChannelNotifier(),
		now:      time.Now,
	}
}

// Stop 关闭异步投递 worker 并等待在途投递完成（未启用异步投递时是空操作）。
//
// 必要性：worker 是长驻 goroutine，进程/测试退出前应回收；等待在途投递是为了
// 「已判定要发的通知不因关停而丢」。幂等（可重复调用）。
func (d *AlertDispatcher) Stop() {
	if d.notifyStopCh == nil {
		return // 从未启用异步投递
	}
	select {
	case <-d.notifyStopCh:
		return // 已关闭
	default:
	}
	close(d.notifyStopCh)
	d.notifyWG.Wait()
}

// startNotifyWorker 懒启动异步投递 worker。
func (d *AlertDispatcher) startNotifyWorker() {
	d.notifyStart.Do(func() {
		d.notifyCh = make(chan notifyJob, alertNotifyQueueDepth)
		d.notifyStopCh = make(chan struct{})
		d.notifyWG.Add(1)
		go func() {
			defer d.notifyWG.Done()
			for {
				select {
				case job := <-d.notifyCh:
					d.notify(&job.rule, job.note)
				case <-d.notifyStopCh:
					// 关停前把队列里的在途通知排空，避免「已落库但没外发」。
					for {
						select {
						case job := <-d.notifyCh:
							d.notify(&job.rule, job.note)
						default:
							return
						}
					}
				}
			}
		}()
	})
}

// enqueueNotify 投递到后台队列；队列满或分发器已关停时退化为内联投递。
//
// 为什么不丢：告警系统的语义是「至少投一次」，静默丢弃一条通知比多阻塞一次调用更糟。
// 两种退化情形的共同处理是内联投递——它虽会阻塞调用方，但保住了通知本身：
//   - **队列满**：要求 ≥42 分钟的投递积压（见 alertNotifyQueueDepth）才会发生，
//     此时留 Warn 日志说明发生了退化（可观测，不静默）。
//   - **已 Stop**：worker 已退出，入队的内容将永不被处理。调用方若在关停后仍触发
//     （如优雅停机的收尾路径），内联投递是唯一还能送达的方式。
func (d *AlertDispatcher) enqueueNotify(rule *model.AlertRule, note AlertNotification) {
	d.startNotifyWorker()
	job := notifyJob{rule: *rule, note: note}
	select {
	case <-d.notifyStopCh:
		slog.Warn("告警分发器已关停，退化为内联投递（不丢通知但会阻塞调用方）", "rule", rule.Name)
		d.notify(&job.rule, job.note)
		return
	default:
	}
	select {
	case d.notifyCh <- job:
	default:
		slog.Warn("告警异步投递队列已满，退化为内联投递（不丢通知但会阻塞调用方）",
			"rule", rule.Name, "queueDepth", alertNotifyQueueDepth)
		d.notify(&job.rule, job.note)
	}
}

// Fire 处理一次告警触发：去抖聚合 → 落库（新建或累计）→ 静默判定 → 分级路由通知 + 站内。
func (d *AlertDispatcher) Fire(trig AlertTrigger) {
	rule := trig.Rule
	now := d.now()

	// 1. 查活跃（未恢复）事件做去抖聚合。
	var active model.AlertEvent
	err := d.db.Where("rule_id = ? AND dedup_key = ? AND resolved = ?", rule.ID, trig.DedupKey, false).
		Order("fired_at DESC").First(&active).Error
	hasActive := err == nil

	level := ruleLevel(rule)

	if hasActive {
		// 去抖窗口内：累计计数、更新复发时间，不重复建事件。
		windowSec := rule.DedupWindowSec
		if windowSec > 0 && now.Sub(deref(active.LastFiredAt, active.FiredAt)) < time.Duration(windowSec)*time.Second {
			d.aggregateActive(&active, now, trig.Direction)
			// 窗口内复发不再通知（去抖）。
			return
		}
		// 已有活跃事件但超出去抖窗口（或未配置去抖）：对可恢复型不重复建（避免风暴），仅累计。
		if trig.Resolvable {
			d.aggregateActive(&active, now, trig.Direction)
			return
		}
		// 不可恢复型（日志/玩家事件）：超窗后视为新一轮，落新事件。
	}

	// 2. 新建告警事件。
	event := &model.AlertEvent{
		RuleID:      rule.ID,
		TargetID:    trig.TargetID,
		Level:       level,
		TriggerType: ruleTriggerType(rule),
		DedupKey:    trig.DedupKey,
		Value:       trig.Value,
		Direction:   trig.Direction,
		Message:     trig.Message,
		Count:       1,
		Resolved:    !trig.Resolvable, // 瞬时事件直接落为已解决（无需后续恢复）。
		FiredAt:     now,
		LastFiredAt: &now,
	}
	if err := d.db.Create(event).Error; err != nil {
		slog.Error("告警分发：创建事件失败", "rule", rule.Name, "error", err)
		return
	}
	slog.Warn("告警触发", "rule", rule.Name, "level", level, "target", trig.TargetID, "message", trig.Message)

	// 3. 站内通知始终落库（确认/历史以事件库为准），已读=false。
	//    站内即事件本身，无需额外表。

	// 4. 静默窗口内：不发外部通知（仍入库）。
	if inSilenceWindow(rule.SilenceStart, rule.SilenceEnd, now) {
		slog.Info("告警处于静默窗口，跳过外部通知", "rule", rule.Name)
		return
	}

	// 5. 分级路由：按 rule.ChannelIDs 投递到各通道。
	//    M-8：请求驱动的触发源（DeliverAsync）把这一步移出调用线程——去抖判定与落库已完成，
	//    投递与它们是 1:1 的，故异步不引入重复（详见 AlertTrigger.DeliverAsync 的说明）。
	note := AlertNotification{
		Event:   "alert_fired",
		RuleID:  rule.UUID,
		Title:   rule.Name,
		Message: trig.Message,
		Level:   level,
		Count:   event.Count,
		Time:    now,
	}
	if trig.DeliverAsync {
		d.enqueueNotify(rule, note)
		return
	}
	d.notify(rule, note)
}

// aggregateActive 累计一次去抖复发：计数 +1、刷新复发时间，并在方向有变化时同步最新偏离方向
// （N6：baseline 事件在窗口内由 up 翻转为 down 时，事件仍显示旧方向会误导排障）。
func (d *AlertDispatcher) aggregateActive(active *model.AlertEvent, now time.Time, direction string) {
	updates := map[string]interface{}{
		"count":         active.Count + 1,
		"last_fired_at": now,
	}
	if direction != "" && direction != active.Direction {
		updates["direction"] = direction
	}
	d.db.Model(active).Updates(updates)
}

// Resolve 标记某去抖键的活跃事件为已恢复，并按 NotifyRecover 发送恢复通知。
func (d *AlertDispatcher) Resolve(rule *model.AlertRule, dedupKey string, message string) {
	var active model.AlertEvent
	err := d.db.Where("rule_id = ? AND dedup_key = ? AND resolved = ?", rule.ID, dedupKey, false).
		Order("fired_at DESC").First(&active).Error
	if err != nil {
		return // 无活跃事件，无需恢复。
	}
	now := d.now()
	if err := d.db.Model(&active).Updates(map[string]interface{}{
		"resolved":    true,
		"resolved_at": now,
	}).Error; err != nil {
		slog.Error("告警分发：标记恢复失败", "eventId", active.ID, "error", err)
		return
	}
	slog.Info("告警恢复", "rule", rule.Name, "target", active.TargetID)

	if !rule.NotifyRecover || inSilenceWindow(rule.SilenceStart, rule.SilenceEnd, now) {
		return
	}
	d.notify(rule, AlertNotification{
		Event:   "alert_resolved",
		RuleID:  rule.UUID,
		Title:   rule.Name + "（已恢复）",
		Message: message,
		Level:   ruleLevel(rule),
		Count:   1,
		Time:    now,
	})
}

// notify 把通知扇出到规则路由的所有通道（FR-085）。
// 优先用 ChannelIDs；为兼容 FR-011，未配置通道但配了 NotifyType=webhook 时回退单 webhook 直发。
func (d *AlertDispatcher) notify(rule *model.AlertRule, note AlertNotification) {
	ids := parseChannelIDs(rule.ChannelIDs)
	if len(ids) == 0 {
		// FR-011 兼容回退：单 webhook 直发。
		if rule.NotifyType == model.ChannelTypeWebhook && rule.NotifyTarget != "" {
			cfg := ChannelConfig{URL: rule.NotifyTarget}
			if err := validateChannelConfig(model.ChannelTypeWebhook, &cfg); err != nil {
				slog.Warn("告警 webhook 直发配置无效", "rule", rule.Name, "error", err)
				return
			}
			raw, err := json.Marshal(cfg)
			if err != nil {
				slog.Warn("序列化告警 webhook 配置失败", "rule", rule.Name, "error", err)
				return
			}
			if err := d.notifier.Send(model.ChannelTypeWebhook, string(raw), note); err != nil {
				slog.Warn("告警 webhook 直发失败", "rule", rule.Name, "error", err)
			}
		}
		return
	}
	var channels []model.AlertChannel
	if err := d.db.Where("id IN ? AND enabled = ?", ids, true).Find(&channels).Error; err != nil {
		slog.Warn("告警分发：查询通道失败", "rule", rule.Name, "error", err)
		return
	}
	for i := range channels {
		ch := &channels[i]
		if ch.Type == model.ChannelTypeInApp {
			// 站内通知已由事件落库承载，无需外发。
			continue
		}
		if err := d.notifier.Send(ch.Type, ch.Config, note); err != nil {
			slog.Warn("告警通道投递失败", "rule", rule.Name, "channel", ch.Name, "type", ch.Type, "error", err)
		}
	}
}

// ── 纯函数：级别 / 类型 / 静默窗口 ──

// ruleLevel 返回规则级别，空值按 warn。
func ruleLevel(rule *model.AlertRule) string {
	if rule.Level == "" {
		return model.AlertLevelWarn
	}
	return rule.Level
}

// ruleTriggerType 返回规则触发类型，空值按 metric（兼容 FR-011 存量）。
func ruleTriggerType(rule *model.AlertRule) string {
	if rule.TriggerType == "" {
		return model.AlertTriggerMetric
	}
	return rule.TriggerType
}

// inSilenceWindow 判断 t（按本地时钟的时分）是否落在 [start, end) 静默窗口内（"HH:MM"）。
// 支持跨午夜（start > end，如 23:00→07:00）。任一端为空 → 不静默。
func inSilenceWindow(start, end string, t time.Time) bool {
	sm, sok := parseHHMM(start)
	em, eok := parseHHMM(end)
	if !sok || !eok {
		return false
	}
	cur := t.Hour()*60 + t.Minute()
	if sm == em {
		return false // 零宽窗口，视为不静默。
	}
	if sm < em {
		return cur >= sm && cur < em
	}
	// 跨午夜：[start, 24:00) ∪ [00:00, end)
	return cur >= sm || cur < em
}

// parseHHMM 解析 "HH:MM" 为当日分钟数。非法返回 (0,false)。
func parseHHMM(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h := int(s[0]-'0')*10 + int(s[1]-'0')
	m := int(s[3]-'0')*10 + int(s[4]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

// deref 返回 *time.Time 的值，nil 时回退 fallback。
func deref(p *time.Time, fallback time.Time) time.Time {
	if p == nil {
		return fallback
	}
	return *p
}
