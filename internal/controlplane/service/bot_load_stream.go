package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

const botLoadStreamDisclaimer = "命令发送成功仅表示 bot.chat 未同步抛错，不证明服务器接受或业务效果。"

// BotLoadStreamSnapshot 会话 SSE 一帧聚合投影（FR-370/372）。
type BotLoadStreamSnapshot struct {
	Run          map[string]any
	RunState     string
	Verdict      string
	CurrentStage int
	LoadCounts   map[string]int64
	CommandTotal map[string]int64
	Barrier      map[string]int64
	Metric       *BotLoadMetricPointView
	Terminal     bool
	ReportReady  bool
	Timestamp    time.Time
}

// ProjectStreamSnapshot 为 SSE 组装 init/counts/metric/complete 所需投影。
func (s *BotLoadMetricSampler) ProjectStreamSnapshot(ctx context.Context, sessionID uint, sessionView any) (*BotLoadStreamSnapshot, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("指标采样器未初始化")
	}
	var sess model.BotStressSession
	if err := s.db.WithContext(ctx).First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, fmt.Errorf("查询会话失败: %w", err)
	}
	now := s.clock.Now().UTC()
	loadCounts, err := s.aggregateBotCounts(ctx, sessionID, sess.BotCount)
	if err != nil {
		return nil, err
	}
	// 映射到前端 loadCounts 字段名
	frontLoad := map[string]int64{
		"planned":      int64(sess.BotCount),
		"accepted":     loadCounts["total"],
		"connecting":   loadCounts[string(model.BotStatusConnecting)],
		"connected":    loadCounts[string(model.BotStatusConnected)],
		"disconnected": loadCounts[string(model.BotStatusDisconnected)],
		"failed":       loadCounts[string(model.BotStatusError)] + loadCounts["not_found"],
		"stopped":      loadCounts[string(model.BotStatusStopped)],
	}
	cmd, err := s.aggregateCommandCounts(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// timed_out → timedOut 对齐前端
	frontCmd := map[string]int64{
		"planned":   cmd["planned"],
		"sent":      cmd["sent"],
		"failed":    cmd["failed"],
		"timedOut":  cmd["timed_out"],
		"cancelled": cmd["cancelled"],
	}
	barrier := map[string]int64{"waiting": 0, "arrived": 0, "released": 0, "timedOut": 0}

	runState := ""
	if sess.RunState != nil {
		runState = string(*sess.RunState)
	} else {
		// V1 兼容：用 status 近似
		switch sess.Status {
		case model.BotStressSessionPending:
			runState = string(model.BotLoadRunPending)
		case model.BotStressSessionRunning:
			runState = string(model.BotLoadRunRunning)
		case model.BotStressSessionStopped:
			runState = string(model.BotLoadRunCompleted)
		case model.BotStressSessionError:
			runState = string(model.BotLoadRunFailed)
		}
	}
	verdict := ""
	if sess.Verdict != nil {
		verdict = string(*sess.Verdict)
	}
	stage := 0
	if sess.CurrentStage != nil {
		stage = *sess.CurrentStage
	}
	terminal := false
	if sess.RunState != nil {
		terminal = IsTerminalRunState(*sess.RunState)
	} else {
		terminal = sess.Status == model.BotStressSessionStopped || sess.Status == model.BotStressSessionError
	}

	// 最近一条 metric sample（可选）
	var metric *BotLoadMetricPointView
	var last model.BotLoadMetricSample
	if err := s.db.WithContext(ctx).
		Where("stress_session_id = ?", sessionID).
		Order("sampled_at DESC").
		First(&last).Error; err == nil {
		v := metricSampleToView(last)
		// 前端 timestamp 为 string；JSON 层 time.Time 已是 RFC3339
		metric = &v
	}

	// 拼装 run 投影：优先复用 sessionView（含 counts/allocations/batches），再叠 V2 字段
	run := map[string]any{}
	if sessionView != nil {
		raw, mErr := json.Marshal(sessionView)
		if mErr == nil {
			_ = json.Unmarshal(raw, &run)
		}
	}
	run["schemaVersion"] = sess.SchemaVersion
	if sess.SchemaVersion == 0 {
		run["schemaVersion"] = 1
	}
	run["id"] = sess.ID
	run["uuid"] = sess.UUID
	run["instanceId"] = sess.InstanceID
	run["name"] = sess.Name
	run["namePrefix"] = sess.NamePrefix
	run["count"] = sess.BotCount
	run["targetBots"] = sess.BotCount
	run["behavior"] = sess.Behavior
	run["status"] = sess.Status
	run["runState"] = runState
	run["verdict"] = verdict
	run["verdictReasons"] = []any{}
	run["currentStage"] = stage
	run["loadCounts"] = frontLoad
	run["commandCounts"] = map[string]any{"command-schedule": frontCmd}
	run["barrier"] = barrier
	if sess.MaxStableBots != nil {
		run["maxStableBots"] = *sess.MaxStableBots
	} else {
		run["maxStableBots"] = 0
	}
	if sess.FailureSummary != "" {
		var fs map[string]any
		if json.Unmarshal([]byte(sess.FailureSummary), &fs) == nil {
			run["failureSummary"] = fs
		}
	}
	if sess.TemplateID != nil {
		run["templateId"] = *sess.TemplateID
	}
	if sess.StartedAt != nil {
		run["startedAt"] = sess.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if sess.EndedAt != nil {
		run["stoppedAt"] = sess.EndedAt.UTC().Format(time.RFC3339Nano)
		run["endedAt"] = sess.EndedAt.UTC().Format(time.RFC3339Nano)
	}
	run["createdAt"] = sess.CreatedAt.UTC().Format(time.RFC3339Nano)
	run["updatedAt"] = sess.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if sess.Config != "" {
		var cfg any
		if json.Unmarshal([]byte(sess.Config), &cfg) == nil {
			run["config"] = cfg
		}
	}

	return &BotLoadStreamSnapshot{
		Run: run, RunState: runState, Verdict: verdict, CurrentStage: stage,
		LoadCounts: frontLoad, CommandTotal: frontCmd, Barrier: barrier,
		Metric: metric, Terminal: terminal, ReportReady: terminal && sess.SchemaVersion == 2,
		Timestamp: now,
	}, nil
}

// MetricPointSSEPayload 将样本点转为前端 metric 事件（timestamp 字符串）。
func MetricPointSSEPayload(p BotLoadMetricPointView) map[string]any {
	return map[string]any{
		"timestamp":    p.Timestamp.UTC().Format(time.RFC3339Nano),
		"stageIndex":   p.StageIndex,
		"counts":       p.Counts,
		"command":      p.Command,
		"barrier":      p.Barrier,
		"executor":     p.Executor,
		"latency":      p.Latency,
		"errors":       p.Errors,
		"targetLegacy": p.TargetLegacy,
	}
}
