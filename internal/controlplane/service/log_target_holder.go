package service

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// LogTargetHolderService manages CP metadata for current and historical log holders.
type LogTargetHolderService struct{ db *gorm.DB }

func NewLogTargetHolderService(db *gorm.DB) *LogTargetHolderService {
	return &LogTargetHolderService{db: db}
}

func (s *LogTargetHolderService) ListForInstance(instanceID uint) ([]model.LogTargetHolder, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("log holder service is not configured")
	}
	var out []model.LogTargetHolder
	if err := s.db.Where("instance_id = ?", instanceID).Order("current DESC, id ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("查询日志 holder 失败: %w", err)
	}
	return out, nil
}

// ListForInstances 批量取多个实例的 holder，按实例分组返回。
//
// 为什么需要批量（2026-10-09 生产实证）：平台级联邦查询会遍历全部实例，逐实例调用
// ListForInstance 时每次查询要发 N 条 SQL（生产约 25 实例 ⇒ 约 25 条 holder 查询），
// 而 CP 的 SQLite 连接池被刻意收敛为单连接⇒ 这些查询全部串行排队，与其它
// 请求互相阻塞（生产 SLOW SQL 记录到同一形态的 holder 查询 p90 达 2.7s、max 约 10s）。
// 批量后 N 条降为 1 条。
//
// 等价性：分组内顺序仍为 current DESC, id ASC（与 ListForInstance 逐字一致），
// 且实例之间互不影响——同一批只改变「取数方式」，不改变任何一行的内容或排序。
func (s *LogTargetHolderService) ListForInstances(instanceIDs []uint) (map[uint][]model.LogTargetHolder, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("log holder service is not configured")
	}
	out := make(map[uint][]model.LogTargetHolder, len(instanceIDs))
	if len(instanceIDs) == 0 {
		return out, nil
	}
	uniq := make([]uint, 0, len(instanceIDs))
	seen := make(map[uint]bool, len(instanceIDs))
	for _, id := range instanceIDs {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	// 分块：规避 SQL 变量数量上限，块内排序已保证跨块合并仍按实例分组有序。
	const chunk = 500
	for start := 0; start < len(uniq); start += chunk {
		end := start + chunk
		if end > len(uniq) {
			end = len(uniq)
		}
		var rows []model.LogTargetHolder
		if err := s.db.Where("instance_id IN ?", uniq[start:end]).
			Order("instance_id ASC, current DESC, id ASC").Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("查询日志 holder 失败: %w", err)
		}
		for _, h := range rows {
			out[h.InstanceID] = append(out[h.InstanceID], h)
		}
	}
	return out, nil
}

// RecordCurrent records a current holder and closes any previous current holder.
func (s *LogTargetHolderService) RecordCurrent(instanceID uint, workerUUID, generation, namespace string, now time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("log holder service is not configured")
	}
	if instanceID == 0 || workerUUID == "" || generation == "" || namespace == "" {
		return fmt.Errorf("log holder identity is incomplete")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		return s.recordCurrentTx(tx, instanceID, workerUUID, generation, namespace, now)
	})
}

func (s *LogTargetHolderService) recordCurrentTx(tx *gorm.DB, instanceID uint, workerUUID, generation, namespace string, now time.Time) error {
	if tx == nil {
		return fmt.Errorf("log holder transaction is required")
	}
	if err := tx.Model(&model.LogTargetHolder{}).Where("instance_id = ? AND current = ?", instanceID, true).
		Updates(map[string]any{"current": false, "valid_to": now}).Error; err != nil {
		return err
	}
	return tx.Create(&model.LogTargetHolder{InstanceID: instanceID, WorkerUUID: workerUUID,
		SourceGeneration: generation, StorageNamespace: namespace, Current: true, ValidFrom: now}).Error
}

func (s *LogTargetHolderService) ReconcileInstances(instances []model.Instance, nodes []model.Node, now time.Time) error {
	byID := make(map[uint]string, len(nodes))
	for _, node := range nodes {
		if node.UUID != "" {
			byID[node.ID] = node.UUID
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		for _, instance := range instances {
			workerUUID := byID[instance.NodeID]
			if workerUUID == "" {
				continue
			}
			var current model.LogTargetHolder
			err := tx.Where("instance_id = ? AND current = ?", instance.ID, true).Order("id DESC").First(&current).Error
			generation := fmt.Sprintf("instance:%s@worker:%s", instance.UUID, workerUUID)
			if err == nil && current.WorkerUUID == workerUUID && current.SourceGeneration == generation && current.StorageNamespace == fmt.Sprintf("inst:%d", instance.ID) {
				continue
			}
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err := s.recordCurrentTx(tx, instance.ID, workerUUID, generation, fmt.Sprintf("inst:%d", instance.ID), now); err != nil {
				return err
			}
		}
		return nil
	})
}

// EnsureCurrent records a holder only when the current row differs. This is
// safe to call from target resolution and captures node changes without adding
// a second lifecycle authority to the log coordinator.
func (s *LogTargetHolderService) EnsureCurrent(instanceID uint, workerUUID, generation, namespace string, now time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("log holder service is not configured")
	}
	var current model.LogTargetHolder
	err := s.db.Where("instance_id = ? AND current = ?", instanceID, true).Order("id DESC").First(&current).Error
	if err == nil && current.WorkerUUID == workerUUID && current.SourceGeneration == generation && current.StorageNamespace == namespace {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.RecordCurrent(instanceID, workerUUID, generation, namespace, now)
}
