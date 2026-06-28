package service

import (
	"fmt"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 通知来源判别（ADR-048）。统一通知流把两类「给人看的通知」合并：
//
//	message = 站内信（定向消息，按用户归属）
//	alert   = 告警事件（系统警报，面向全体运维）
const (
	FeedSourceMessage = "message"
	FeedSourceAlert   = "alert"
)

// FeedItem 统一通知条目（ADR-048）。聚合 Notification 与 AlertEvent 的展示视图；
// source 永远在出参里供前端分组/筛选/决定可执行动作。级别统一到站内信四档枚举
// （info/success/warning/error），告警三档就近映射（warn→warning、critical→error）。
type FeedItem struct {
	Source    string    `json:"source"` // message | alert
	ID        uint      `json:"id"`     // 源表主键（同 source 内唯一）
	Level     string    `json:"level"`  // 统一枚举 info/success/warning/error
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Read      bool      `json:"read"`
	CreatedAt time.Time `json:"createdAt"` // 统一排序键（message=created_at，alert=fired_at）

	TaskID       string `json:"taskId,omitempty"`       // 仅 message
	TriggerType  string `json:"triggerType,omitempty"`  // 仅 alert
	Acknowledged bool   `json:"acknowledged,omitempty"` // 仅 alert
	Resolved     bool   `json:"resolved,omitempty"`     // 仅 alert
}

// FeedFilter 统一通知流筛选条件。
type FeedFilter struct {
	Source   string // ""=全部 / message / alert
	Unread   bool   // 仅未读
	Keyword  string // 标题/正文模糊匹配
	Page     int    // 页码，从 1 起
	PageSize int    // 每页条数，<=0 取默认 50
}

// NotificationFeedService 统一通知流聚合服务（FR-216，见 ADR-048）。
// 只读聚合：查询时把 notifications（按用户）+ alert_events（全局）合并为一条通知流；
// 不新建物理表、不双写。标记已读下推到各源既有语义（NotificationService / AlertService）。
type NotificationFeedService struct {
	db    *gorm.DB
	notif *NotificationService
	alert *AlertService
}

// NewNotificationFeedService 创建统一通知流服务。复用既有站内信/告警服务做标记已读。
func NewNotificationFeedService(db *gorm.DB, notif *NotificationService, alert *AlertService) *NotificationFeedService {
	return &NotificationFeedService{db: db, notif: notif, alert: alert}
}

// alertLevelToUnified 把告警三档级别映射到站内信四档统一枚举（ADR-048）。
// warn→warning、critical→error、info→info；未知值按 info 兜底。
func alertLevelToUnified(level string) string {
	switch level {
	case model.AlertLevelCritical:
		return string(model.NotificationLevelError)
	case model.AlertLevelWarn:
		return string(model.NotificationLevelWarning)
	default:
		return string(model.NotificationLevelInfo)
	}
}

// Feed 返回统一通知流分页（ADR-048）。
// 按 Source 决定查哪一/两源：各源取「页所需上界」候选 + 各自命中数，归并按 CreatedAt 倒序后切页；
// total = 两源命中数之和（两源天然不重叠，不去重）。keyword 同时作用两源标题/正文。
func (s *NotificationFeedService) Feed(userID uint, f FeedFilter) ([]FeedItem, int64, error) {
	page := f.Page
	if page < 1 {
		page = 1
	}
	pageSize := f.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	upper := page * pageSize // 各源最多需要的候选条数（归并后切片足够）

	wantMessage := f.Source == "" || f.Source == FeedSourceMessage
	wantAlert := f.Source == "" || f.Source == FeedSourceAlert

	var candidates []FeedItem
	var total int64

	if wantMessage {
		items, cnt, err := s.messageCandidates(userID, f, upper)
		if err != nil {
			return nil, 0, err
		}
		candidates = append(candidates, items...)
		total += cnt
	}
	if wantAlert {
		items, cnt, err := s.alertCandidates(f, upper)
		if err != nil {
			return nil, 0, err
		}
		candidates = append(candidates, items...)
		total += cnt
	}

	// 归并按发生时间倒序（同源内已倒序，跨源需整体再排）。
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.After(candidates[j].CreatedAt)
	})

	start := (page - 1) * pageSize
	if start >= len(candidates) {
		return []FeedItem{}, total, nil
	}
	end := start + pageSize
	if end > len(candidates) {
		end = len(candidates)
	}
	return candidates[start:end], total, nil
}

// messageCandidates 取站内信源候选（按用户归属）+ 命中总数。
func (s *NotificationFeedService) messageCandidates(userID uint, f FeedFilter, limit int) ([]FeedItem, int64, error) {
	q := s.db.Model(&model.Notification{}).Where("user_id = ?", userID)
	if f.Unread {
		q = q.Where("read_at IS NULL")
	}
	if f.Keyword != "" {
		kw := "%" + f.Keyword + "%"
		q = q.Where("title LIKE ? OR body LIKE ?", kw, kw)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计站内信失败: %w", err)
	}
	var rows []model.Notification
	if err := q.Order("created_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("查询站内信失败: %w", err)
	}
	out := make([]FeedItem, 0, len(rows))
	for _, n := range rows {
		out = append(out, FeedItem{
			Source:    FeedSourceMessage,
			ID:        n.ID,
			Level:     string(n.Level),
			Title:     n.Title,
			Body:      n.Body,
			Read:      n.ReadAt != nil,
			CreatedAt: n.CreatedAt,
			TaskID:    n.TaskID,
		})
	}
	return out, total, nil
}

// alertCandidates 取告警源候选（全局，面向全体运维）+ 命中总数。预加载规则名作标题。
func (s *NotificationFeedService) alertCandidates(f FeedFilter, limit int) ([]FeedItem, int64, error) {
	q := s.db.Model(&model.AlertEvent{})
	if f.Unread {
		q = q.Where("read = ?", false)
	}
	if f.Keyword != "" {
		q = q.Where("message LIKE ?", "%"+f.Keyword+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("统计告警事件失败: %w", err)
	}
	var rows []model.AlertEvent
	if err := q.Preload("Rule").Order("fired_at DESC, id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, fmt.Errorf("查询告警事件失败: %w", err)
	}
	out := make([]FeedItem, 0, len(rows))
	for _, e := range rows {
		title := e.Rule.Name
		if title == "" {
			title = fmt.Sprintf("#%d", e.RuleID)
		}
		out = append(out, FeedItem{
			Source:       FeedSourceAlert,
			ID:           e.ID,
			Level:        alertLevelToUnified(e.Level),
			Title:        title,
			Body:         e.Message,
			Read:         e.Read,
			CreatedAt:    e.FiredAt,
			TriggerType:  e.TriggerType,
			Acknowledged: e.Acknowledged,
			Resolved:     e.Resolved,
		})
	}
	return out, total, nil
}

// UnreadCount 返回统一未读数（ADR-048）：当前用户未读站内信 + 全局未读告警。
func (s *NotificationFeedService) UnreadCount(userID uint) (int64, error) {
	msgUnread, err := s.notif.UnreadCount(userID)
	if err != nil {
		return 0, err
	}
	alertUnread, err := s.alert.UnreadCount()
	if err != nil {
		return 0, err
	}
	return msgUnread + alertUnread, nil
}

// MarkRead 标记单条通知已读（ADR-048）。按 source 下推到各源既有语义：
// message → NotificationService.MarkRead（按用户归属校验）；alert → AlertService.MarkRead（全局）。
func (s *NotificationFeedService) MarkRead(userID uint, source string, id uint) error {
	switch source {
	case FeedSourceMessage:
		return s.notif.MarkRead(userID, id)
	case FeedSourceAlert:
		return s.alert.MarkRead(id)
	default:
		return fmt.Errorf("非法通知来源: %s", source)
	}
}

// MarkAllRead 全部标记已读（ADR-048）：站内信按当前用户全读 + 告警全局全读。
// 返回受影响合计（站内信精确条数 + 告警以受影响视为全部未读数估算）。
func (s *NotificationFeedService) MarkAllRead(userID uint) (int64, error) {
	// 先取告警未读数（MarkRead 后无法回算），站内信由 MarkAllRead 直接返回精确条数。
	alertUnread, err := s.alert.UnreadCount()
	if err != nil {
		return 0, err
	}
	msgUpdated, err := s.notif.MarkAllRead(userID)
	if err != nil {
		return 0, err
	}
	if err := s.alert.MarkRead(0); err != nil {
		return 0, err
	}
	return msgUpdated + alertUnread, nil
}
