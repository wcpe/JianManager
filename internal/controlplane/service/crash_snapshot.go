package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// CrashSnapshotService 实例崩溃快照查询服务（FR-313）。
// 写入在 gRPC 层（Worker 上报 ReportCrashSnapshot，含滚动修剪）；此处只承担 REST 读侧。
type CrashSnapshotService struct {
	db *gorm.DB
}

// NewCrashSnapshotService 创建崩溃快照查询服务。
func NewCrashSnapshotService(db *gorm.DB) *CrashSnapshotService {
	return &CrashSnapshotService{db: db}
}

// ListByInstance 返回实例的崩溃快照，按发生时间倒序（最新在前，至多 K=5 条，由写侧修剪保证）。
// 实例不存在返回 ErrInstanceNotFound（平台管理员绕过组访问校验，存在性须由此兜底成 404）。
func (s *CrashSnapshotService) ListByInstance(instanceID uint) ([]model.InstanceCrashSnapshot, error) {
	var inst model.Instance
	if err := s.db.Select("id").First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, err
	}
	var out []model.InstanceCrashSnapshot
	err := s.db.Where("instance_id = ?", instanceID).
		Order("occurred_at DESC, id DESC").
		Find(&out).Error
	return out, err
}
