package service

import (
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// AuditService 审计日志服务。
type AuditService struct {
	db *gorm.DB
}

// NewAuditService 创建审计服务。
func NewAuditService(db *gorm.DB) *AuditService {
	return &AuditService{db: db}
}

// Record 记录一条成功操作的审计日志（既有调用点语义不变）。
func (s *AuditService) Record(userID uint, action, targetType, targetID, detail, ip string) error {
	return s.RecordResult(userID, action, targetType, targetID, detail, ip, true, "")
}

// RecordResult 记录带结果的审计日志（FR-321）：失败操作也留痕并带错误内容，
// 回答「这个操作为什么报错」（此前失败操作审计无错误内容）。
func (s *AuditService) RecordResult(userID uint, action, targetType, targetID, detail, ip string, success bool, errMsg string) error {
	if len(errMsg) > 512 {
		errMsg = errMsg[:512]
	}
	log := &model.AuditLog{
		UserID:     userID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
		IP:         ip,
		Success:    success,
		Error:      errMsg,
	}
	if err := s.db.Create(log).Error; err != nil {
		return fmt.Errorf("记录审计日志失败: %w", err)
	}
	return nil
}

// List 查询审计日志。
type AuditFilter struct {
	UserID     *uint
	Action     *string
	TargetType *string
	From       *time.Time
	To         *time.Time
	Limit      int
	Page       int
	PageSize   int
}

type AuditPage struct {
	Items    []model.AuditLog `json:"items"`
	Total    int64            `json:"total"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
}

type auditExportCursor struct {
	createdAt time.Time
	id        uint
	set       bool
}

const defaultAuditExportBatchSize = 200

func (s *AuditService) auditQuery(filter AuditFilter) *gorm.DB {
	q := s.db.Model(&model.AuditLog{}).Preload("User")
	if filter.UserID != nil {
		q = q.Where("user_id = ?", *filter.UserID)
	}
	if filter.Action != nil {
		q = q.Where("action = ?", *filter.Action)
	}
	if filter.TargetType != nil {
		q = q.Where("target_type = ?", *filter.TargetType)
	}
	if filter.From != nil {
		q = q.Where("created_at >= ?", *filter.From)
	}
	if filter.To != nil {
		q = q.Where("created_at <= ?", *filter.To)
	}
	return q
}

// List 查询审计日志列表，保留旧 limit 数组响应语义。
func (s *AuditService) List(filter AuditFilter) ([]model.AuditLog, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	var logs []model.AuditLog
	if err := s.auditQuery(filter).Order("created_at DESC, id DESC").Limit(limit).Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// ListPage 分页查询审计日志，供新版审计页使用。
func (s *AuditService) ListPage(filter AuditFilter) (AuditPage, error) {
	page, pageSize := normalizeAuditPage(filter.Page, filter.PageSize)
	var total int64
	if err := s.auditQuery(filter).Count(&total).Error; err != nil {
		return AuditPage{}, err
	}
	var logs []model.AuditLog
	err := s.auditQuery(filter).
		Order("created_at DESC, id DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&logs).Error
	if err != nil {
		return AuditPage{}, err
	}
	return AuditPage{Items: logs, Total: total, Page: page, PageSize: pageSize}, nil
}

// Export 查询全部匹配的审计日志，不受列表分页参数影响。
func (s *AuditService) Export(filter AuditFilter) ([]model.AuditLog, error) {
	var logs []model.AuditLog
	err := s.StreamExport(filter, defaultAuditExportBatchSize, func(log model.AuditLog) error {
		logs = append(logs, log)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return logs, nil
}

// StreamExport 分批遍历全部匹配的审计日志，不受列表分页参数影响。
func (s *AuditService) StreamExport(filter AuditFilter, batchSize int, handle func(model.AuditLog) error) error {
	if batchSize <= 0 {
		batchSize = defaultAuditExportBatchSize
	}
	filter.Limit = 0
	filter.Page = 0
	filter.PageSize = 0
	cursor := auditExportCursor{}
	for {
		logs, err := s.exportBatch(filter, batchSize, cursor)
		if err != nil {
			return err
		}
		if len(logs) == 0 {
			return nil
		}
		for _, log := range logs {
			if err := handle(log); err != nil {
				return err
			}
		}
		last := logs[len(logs)-1]
		cursor = auditExportCursor{createdAt: last.CreatedAt, id: last.ID, set: true}
		if len(logs) < batchSize {
			return nil
		}
	}
}

func (s *AuditService) exportBatch(filter AuditFilter, batchSize int, cursor auditExportCursor) ([]model.AuditLog, error) {
	q := s.auditQuery(filter).Order("created_at DESC, id DESC").Limit(batchSize)
	if cursor.set {
		q = q.Where("created_at < ? OR (created_at = ? AND id < ?)", cursor.createdAt, cursor.createdAt, cursor.id)
	}
	var logs []model.AuditLog
	if err := q.Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

func normalizeAuditPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}
