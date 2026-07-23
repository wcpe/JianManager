package service

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// BotLoadCommandCheckpointService 负责 FR-369 命令编排 checkpoint 物化与查询。
type BotLoadCommandCheckpointService struct {
	db *gorm.DB
}

// NewBotLoadCommandCheckpointService 创建服务实例。
func NewBotLoadCommandCheckpointService(db *gorm.DB) *BotLoadCommandCheckpointService {
	return &BotLoadCommandCheckpointService{db: db}
}

// EnsureOccurrence 在 Apply 前物化 stable checkpoint 行，缺失时插入；存在则保留最近一次 attempt。
// skipOccurrences 列表中的 occurrence 不写入（默认视为已 sent）。
func (s *BotLoadCommandCheckpointService) EnsureOccurrences(ctx context.Context, sessionID uint, runUUID, botUUID, stepID, scheduleRunID, correlationToken string, generation int64, occurrences []CommandScheduleOccurrence, skip map[string]struct{}) error {
	if s == nil || s.db == nil {
		return errors.New("checkpoint 服务未初始化")
	}
	if len(occurrences) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, occ := range occurrences {
			key := commandOccurrenceKey(occ.CommandID, occ.Occurrence)
			if _, skipIt := skip[key]; skipIt {
				continue
			}
			actionRunID, err := ComputeCommandOccurrenceActionRunID(scheduleRunID, botUUID, stepID, occ.CommandID, occ.Occurrence)
			if err != nil {
				return err
			}
			row := model.BotLoadCommandCheckpoint{
				StressSessionID: sessionID,
				RunUUID:         runUUID,
				BotUUID:         botUUID,
				StepID:          stepID,
				CommandID:       occ.CommandID,
				Occurrence:      occ.Occurrence,
				Generation:      generation,
				ScheduleRunID:   scheduleRunID,
				ActionRunID:     actionRunID,
				Status:          model.BotLoadCommandCheckpointPrepared,
			}
			// OnConflict 保留当前 attempt/status，仅更新 generation/scheduleRunId/actionRunId。
			res := tx.Where("run_uuid = ? AND bot_uuid = ? AND step_id = ? AND command_id = ? AND occurrence = ?",
				runUUID, botUUID, stepID, occ.CommandID, occ.Occurrence).
				Assign(model.BotLoadCommandCheckpoint{
					Generation:     generation,
					ScheduleRunID:  scheduleRunID,
					ActionRunID:    actionRunID,
				}).FirstOrCreate(&row)
			if res.Error != nil {
				return res.Error
			}
		}
		return nil
	})
}

// MarkSent 把已 sent occurrence 状态写回 checkpoint。
func (s *BotLoadCommandCheckpointService) MarkSent(ctx context.Context, runUUID, botUUID, stepID, commandID string, occurrence, attempt int, sentAtUnixMs int64) error {
	if s == nil || s.db == nil {
		return errors.New("checkpoint 服务未初始化")
	}
	res := s.db.WithContext(ctx).Model(&model.BotLoadCommandCheckpoint{}).
		Where("run_uuid = ? AND bot_uuid = ? AND step_id = ? AND command_id = ? AND occurrence = ?",
			runUUID, botUUID, stepID, commandID, occurrence).
		Updates(map[string]interface{}{
			"status":       model.BotLoadCommandCheckpointSent,
			"attempt":      attempt,
			"sent_at_unix_ms": sentAtUnixMs,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("checkpoint 行不存在")
	}
	return nil
}

// MarkFailed 写回 failed/timed_out/cancelled 终态与错误码。
func (s *BotLoadCommandCheckpointService) MarkFailed(ctx context.Context, runUUID, botUUID, stepID, commandID string, occurrence int, status model.BotLoadCommandCheckpointStatus, attempt int, errorCode string) error {
	if s == nil || s.db == nil {
		return errors.New("checkpoint 服务未初始化")
	}
	res := s.db.WithContext(ctx).Model(&model.BotLoadCommandCheckpoint{}).
		Where("run_uuid = ? AND bot_uuid = ? AND step_id = ? AND command_id = ? AND occurrence = ?",
			runUUID, botUUID, stepID, commandID, occurrence).
		Updates(map[string]interface{}{
			"status":     status,
			"attempt":    attempt,
			"error_code": errorCode,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("checkpoint 行不存在")
	}
	return nil
}

// BotCommandScheduleSnapshot 序列化 plan 的不可变快照。
func BotCommandScheduleSnapshot(plan *CommandSchedulePlan) (string, error) {
	if plan == nil {
		return "", errors.New("plan 不能为空")
	}
	clean := SanitizeCommandSchedulePlanForSnapshot(plan)
	payload, err := json.Marshal(clean)
	if err != nil {
		return "", err
	}
	if len(payload) > commandScheduleMaxJSONBytes {
		return "", errors.New("snapshot 超出 256KiB 限制")
	}
	return string(payload), nil
}