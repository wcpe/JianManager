package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// BotLoadRunIntentService 将 handler intent 落到 DB 状态机（与 V1 status 同事务映射）。
type BotLoadRunIntentService struct {
	db *gorm.DB
}

// NewBotLoadRunIntentService 创建运行意图服务。
func NewBotLoadRunIntentService(db *gorm.DB) *BotLoadRunIntentService {
	return &BotLoadRunIntentService{db: db}
}

// ApplyIntent 对 schemaVersion=2 运行应用状态转换；写 run_event 并返回更新后的会话。
func (s *BotLoadRunIntentService) ApplyIntent(ctx context.Context, sessionID uint, intent BotLoadRunIntent, reason string) (*model.BotStressSession, error) {
	var out *model.BotStressSession
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sess model.BotStressSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&sess, sessionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBotStressSessionNotFound
			}
			return fmt.Errorf("查询压测会话失败: %w", err)
		}
		if sess.SchemaVersion != 2 || sess.RunState == nil {
			return fmt.Errorf("%w: 仅 schemaVersion=2 运行支持 V2 状态机", ErrBotLoadInvalidState)
		}
		from := *sess.RunState
		to, unchanged, err := TransitionRunState(from, intent)
		if err != nil {
			return err
		}
		if unchanged {
			out = &sess
			return nil
		}
		legacy := MapRunStateToLegacyStatus(to)
		updates := map[string]any{
			"run_state":  to,
			"status":     legacy,
			"updated_at": time.Now().UTC(),
		}
		// 终态时写 ended_at（若尚未写）
		if IsTerminalRunState(to) && sess.EndedAt == nil {
			now := time.Now().UTC()
			updates["ended_at"] = now
		}
		if err := tx.Model(&sess).Updates(updates).Error; err != nil {
			return fmt.Errorf("更新运行状态失败: %w", err)
		}
		// 写 append-only 事件
		payload := map[string]any{
			"runState":         to,
			"previousRunState": from,
		}
		if reason != "" {
			payload["reason"] = reason
		}
		if sess.Verdict != nil {
			payload["verdict"] = *sess.Verdict
		}
		payloadJSON, err := EncodeJSON(payload)
		if err != nil {
			return fmt.Errorf("序列化运行状态事件失败: %w", err)
		}
		ev := &model.BotLoadRunEvent{
			StressSessionID: sess.ID,
			RunUUID:         sess.UUID,
			Type:            model.BotLoadRunEventRunState,
			OccurredAt:      time.Now().UTC(),
			PayloadJSON:     payloadJSON,
		}
		if err := tx.Create(ev).Error; err != nil {
			return fmt.Errorf("写入运行事件失败: %w", err)
		}
		if err := tx.First(&sess, sessionID).Error; err != nil {
			return err
		}
		out = &sess
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MarkReady 将 pending/preflighting 置为 ready（预检成功后调用）。
func (s *BotLoadRunIntentService) MarkReady(ctx context.Context, sessionID uint) (*model.BotStressSession, error) {
	return s.ApplyIntent(ctx, sessionID, BotLoadIntentReady, "preflight_ok")
}

// AcceptStop 接受有序停止 intent。
func (s *BotLoadRunIntentService) AcceptStop(ctx context.Context, sessionID uint, reason string) (*model.BotStressSession, error) {
	return s.ApplyIntent(ctx, sessionID, BotLoadIntentStop, reason)
}

// AcceptCancel 接受尽快取消 intent。
func (s *BotLoadRunIntentService) AcceptCancel(ctx context.Context, sessionID uint, reason string) (*model.BotStressSession, error) {
	return s.ApplyIntent(ctx, sessionID, BotLoadIntentCancel, reason)
}

// PersistVerdict 在终态前写入 verdict / maxStableBots / reportSummary。
func (s *BotLoadRunIntentService) PersistVerdict(ctx context.Context, sessionID uint, verdict model.BotLoadVerdict, maxStable *int, reportSummary string, failureSummary map[string]int) error {
	failJSON, err := EncodeJSON(failureSummary)
	if err != nil {
		return err
	}
	updates := map[string]any{
		"verdict":         verdict,
		"failure_summary": failJSON,
		"updated_at":      time.Now().UTC(),
	}
	if maxStable != nil {
		updates["max_stable_bots"] = *maxStable
	}
	if reportSummary != "" {
		updates["report_summary"] = reportSummary
	}
	return s.db.WithContext(ctx).Model(&model.BotStressSession{}).Where("id = ?", sessionID).Updates(updates).Error
}

// LoadV2Session 加载 schemaVersion=2 会话。
func (s *BotLoadRunIntentService) LoadV2Session(ctx context.Context, sessionID uint) (*model.BotStressSession, error) {
	var sess model.BotStressSession
	if err := s.db.WithContext(ctx).First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, err
	}
	if sess.SchemaVersion != 2 {
		return nil, fmt.Errorf("%w: 非 V2 运行", ErrBotLoadInvalidState)
	}
	return &sess, nil
}

// DecodeSessionProfile 从会话快照解析 loadProfile。
func DecodeSessionProfile(sess *model.BotStressSession) (*BotLoadProfile, error) {
	if sess == nil || sess.LoadProfile == "" {
		return nil, fmt.Errorf("%w: loadProfile 缺失", ErrBotLoadProfileInvalid)
	}
	return NormalizeAndValidateLoadProfile(json.RawMessage(sess.LoadProfile))
}

// DecodeSessionThresholds 从会话快照解析 thresholds。
func DecodeSessionThresholds(sess *model.BotStressSession) (*BotLoadThresholds, error) {
	if sess == nil || sess.Thresholds == "" {
		t := DefaultBotLoadThresholds()
		return &t, nil
	}
	return NormalizeAndValidateThresholds(json.RawMessage(sess.Thresholds))
}
