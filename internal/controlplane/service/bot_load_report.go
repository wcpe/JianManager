package service

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// BotLoadReportService 终态运行报告（FR-370 partial）。
type BotLoadReportService struct {
	db *gorm.DB
}

// NewBotLoadReportService 创建报告服务。
func NewBotLoadReportService(db *gorm.DB) *BotLoadReportService {
	return &BotLoadReportService{db: db}
}

// BotLoadReportJSON 最小 JSON 报告。
type BotLoadReportJSON struct {
	RunID         uint           `json:"runId"`
	RunUUID       string         `json:"runUuid"`
	SchemaVersion int            `json:"schemaVersion"`
	RunState      string         `json:"runState"`
	Verdict       string         `json:"verdict"`
	MaxStableBots *int           `json:"maxStableBots,omitempty"`
	FailureSummary map[string]int `json:"failureSummary,omitempty"`
	ReportSummary string         `json:"reportSummary,omitempty"`
	GeneratedAt   time.Time      `json:"generatedAt"`
	Disclaimer    string         `json:"disclaimer"`
}

// BuildJSON 仅 schemaVersion=2 且终态可导出。
func (s *BotLoadReportService) BuildJSON(sessionID uint) (*BotLoadReportJSON, error) {
	var sess model.BotStressSession
	if err := s.db.First(&sess, sessionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBotStressSessionNotFound
		}
		return nil, err
	}
	if sess.SchemaVersion != 2 || sess.RunState == nil {
		return nil, ErrBotLoadReportNotReady
	}
	if !IsTerminalRunState(*sess.RunState) {
		return nil, ErrBotLoadReportNotReady
	}
	rep := &BotLoadReportJSON{
		RunID: sess.ID, RunUUID: sess.UUID, SchemaVersion: sess.SchemaVersion,
		RunState: string(*sess.RunState), GeneratedAt: time.Now().UTC(),
		Disclaimer: "命令发送成功仅表示 bot.chat 未同步抛错，不证明服务器接受或业务效果。",
	}
	if sess.Verdict != nil {
		rep.Verdict = string(*sess.Verdict)
	}
	rep.MaxStableBots = sess.MaxStableBots
	if sess.ReportSummary != "" {
		rep.ReportSummary = sess.ReportSummary
	}
	if sess.FailureSummary != "" {
		_ = json.Unmarshal([]byte(sess.FailureSummary), &rep.FailureSummary)
	}
	return rep, nil
}

// BuildCSV 导出一行摘要 CSV（UTF-8 BOM）。
func (s *BotLoadReportService) BuildCSV(sessionID uint) ([]byte, error) {
	rep, err := s.BuildJSON(sessionID)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("\ufeff")
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"runId", "runUuid", "runState", "verdict", "maxStableBots", "generatedAt"})
	maxStable := ""
	if rep.MaxStableBots != nil {
		maxStable = fmt.Sprintf("%d", *rep.MaxStableBots)
	}
	_ = w.Write([]string{
		fmt.Sprintf("%d", rep.RunID), rep.RunUUID, rep.RunState, rep.Verdict,
		maxStable, rep.GeneratedAt.Format(time.RFC3339),
	})
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}
