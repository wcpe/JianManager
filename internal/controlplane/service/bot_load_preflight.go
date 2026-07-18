package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// BotLoadCapacityProvider 是 preflight 对容量目录的最小依赖。
type BotLoadCapacityProvider interface {
	Snapshot(ctx context.Context, excludeRunID uint) (*BotLoadCapacitySnapshot, error)
}

// BotLoadPreflightInput 是纯核心预检输入；权限、HTTP 与场景 V2 校验由上层负责。
type BotLoadPreflightInput struct {
	TargetBots                  int
	ExecutorNodeIDs             []uint
	ConnectRatePerSecondPerNode int
	Probe                       BotLoadProbeStatus
}

// BotLoadPreflightService 编排容量、确定性计划、签名与软预留，不创建 Bot/批次或下发 RPC。
type BotLoadPreflightService struct {
	db           *gorm.DB
	capacities   BotLoadCapacityProvider
	reservations *BotLoadReservationStore
	signer       *BotLoadPlanTokenSigner
	clock        BotLoadClock
	planner      BotLoadPlanner

	commitMu sync.Mutex
}

// NewBotLoadPreflightService 创建纯 Control Plane 预检核心。
func NewBotLoadPreflightService(db *gorm.DB, capacities BotLoadCapacityProvider, reservations *BotLoadReservationStore, signer *BotLoadPlanTokenSigner, clock BotLoadClock) *BotLoadPreflightService {
	return &BotLoadPreflightService{
		db: db, capacities: capacities, reservations: reservations,
		signer: signer, clock: normalizeBotLoadClock(clock), planner: BotLoadPlanner{},
	}
}

// Preflight 生成计划并在 ready 时保存服务端计划正文、建立同到期时间的软预留。
func (s *BotLoadPreflightService) Preflight(ctx context.Context, session *model.BotStressSession, input BotLoadPreflightInput) (*BotLoadPreflightResult, error) {
	if err := s.validatePreflight(session, &input); err != nil {
		return nil, err
	}
	capacitySnapshot, err := s.capacities.Snapshot(ctx, session.ID)
	if err != nil {
		return nil, err
	}
	probe, err := s.normalizeProbe(session, input.Probe)
	if err != nil {
		return nil, err
	}
	planning, err := s.planner.Plan(BotLoadPlanRequest{
		RunID: session.ID, RunUUID: session.UUID, TargetBots: input.TargetBots,
		NodeCapacities: capacitySnapshot.NodeCapacities, ExecutorNodeIDs: input.ExecutorNodeIDs,
		ConnectRatePerSecondPerNode: input.ConnectRatePerSecondPerNode, ConnectStartAt: s.clock.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	result := newBotLoadPreflightResult(session, input, probe, capacitySnapshot, planning)
	if probe.Required && !probe.Connected {
		result.Blockers = append(result.Blockers, BotLoadIssue{Code: BotLoadProbeRequiredCode, Message: probeBlockerMessage(probe)})
	}
	if len(result.Blockers) > 0 || !planning.Ready {
		return result, nil
	}
	return s.commitReadyPlan(result, session, planning, capacitySnapshot.ReservationLimits)
}

func (s *BotLoadPreflightService) validatePreflight(session *model.BotStressSession, input *BotLoadPreflightInput) error {
	if s == nil || s.db == nil || s.capacities == nil || s.reservations == nil || s.signer == nil {
		return fmt.Errorf("Bot 负载预检核心未完整装配")
	}
	if session == nil || session.ID == 0 || session.UUID == "" {
		return ErrBotLoadPreflightInvalid
	}
	if input.TargetBots == 0 {
		input.TargetBots = session.BotCount
	}
	if input.ConnectRatePerSecondPerNode == 0 {
		input.ConnectRatePerSecondPerNode = defaultBotLoadConnectRate
	}
	return nil
}

func (s *BotLoadPreflightService) normalizeProbe(session *model.BotStressSession, probe BotLoadProbeStatus) (BotLoadProbeStatus, error) {
	if probe.InstanceID == 0 {
		probe.InstanceID = session.InstanceID
	}
	if probe.InstanceUUID != "" {
		return probe, nil
	}
	if session.Instance.UUID != "" {
		probe.InstanceUUID = session.Instance.UUID
		return probe, nil
	}
	var instance model.Instance
	if err := s.db.Select("id", "uuid").First(&instance, probe.InstanceID).Error; err != nil {
		return BotLoadProbeStatus{}, fmt.Errorf("查询预检目标实例失败: %w", err)
	}
	probe.InstanceUUID = instance.UUID
	return probe, nil
}

func newBotLoadPreflightResult(session *model.BotStressSession, input BotLoadPreflightInput, probe BotLoadProbeStatus, snapshot *BotLoadCapacitySnapshot, planning BotLoadPlanningResult) *BotLoadPreflightResult {
	return &BotLoadPreflightResult{
		RunID: session.ID, RunUUID: session.UUID, Ready: false,
		TargetBots: input.TargetBots, TotalAvailable: planning.TotalAvailable,
		Allocations:    append([]BotLoadAllocation(nil), planning.Allocations...),
		NodeCapacities: append([]BotLoadNodeCapacity(nil), snapshot.NodeCapacities...), Probe: probe,
		EstimatedDurationSeconds: estimateBotLoadDuration(planning.Allocations, input.ConnectRatePerSecondPerNode),
		Warnings:                 []BotLoadIssue{}, Blockers: append([]BotLoadIssue(nil), planning.Blockers...),
	}
}

func (s *BotLoadPreflightService) commitReadyPlan(result *BotLoadPreflightResult, session *model.BotStressSession, planning BotLoadPlanningResult, limits map[uint]int) (*BotLoadPreflightResult, error) {
	plan := BotLoadAllocationPlan{
		RunID: session.ID, RunUUID: session.UUID, TargetBots: result.TargetBots,
		Allocations:         append([]BotLoadAllocation(nil), planning.Allocations...),
		CapacityGenerations: append([]BotLoadNodeGeneration(nil), planning.CapacityGenerations...),
	}
	hash, err := BotLoadAllocationHash(plan.RunID, plan.TargetBots, plan.Allocations)
	if err != nil {
		return nil, err
	}
	token, expiresAt, err := s.signer.Issue(plan.RunID, hash, plan.CapacityGenerations)
	if err != nil {
		return nil, err
	}
	if err := s.reserveAndPersistPlan(plan, limits, expiresAt); err != nil {
		if errors.Is(err, ErrBotLoadReservationCapacity) {
			result.Allocations = []BotLoadAllocation{}
			result.EstimatedDurationSeconds = 0
			result.Blockers = append(result.Blockers, BotLoadIssue{Code: BotLoadCapacityInsufficientCode, Message: "并发预检后可用容量已变化，请重新预检"})
			return result, nil
		}
		return nil, err
	}
	result.Ready = true
	result.PlanToken = token
	result.ExpiresAt = &expiresAt
	return result, nil
}

func (s *BotLoadPreflightService) reserveAndPersistPlan(plan BotLoadAllocationPlan, limits map[uint]int, expiresAt time.Time) error {
	desired := allocationBotLoadCounts(plan.Allocations)
	rawPlan, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("序列化 Bot 负载计划失败: %w", err)
	}
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	previous, hadPrevious := s.reservations.Lease(plan.RunID)
	lease, err := s.reservations.ReplaceUntil(plan.RunID, desired, limits, expiresAt)
	if err != nil {
		return err
	}
	if err := s.persistAllocationPlan(plan.RunID, string(rawPlan)); err != nil {
		if !hadPrevious {
			previous = BotLoadReservationLease{}
		}
		s.reservations.RestoreIfCurrent(plan.RunID, lease.Revision, previous)
		return err
	}
	return nil
}

func (s *BotLoadPreflightService) persistAllocationPlan(runID uint, rawPlan string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		allowed := []model.BotStressSessionStatus{model.BotStressSessionPending, model.BotStressSessionError}
		result := tx.Model(&model.BotStressSession{}).
			Where("id = ? AND status IN ?", runID, allowed).
			Update("allocation_plan", rawPlan)
		if result.Error != nil {
			return fmt.Errorf("保存 Bot 负载计划失败: %w", result.Error)
		}
		if result.RowsAffected > 0 {
			return nil
		}
		var session model.BotStressSession
		if err := tx.Select("id", "status").First(&session, runID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrBotStressSessionNotFound
			}
			return fmt.Errorf("检查 Bot 负载预检状态失败: %w", err)
		}
		return fmt.Errorf("%w: 当前状态为 %s", ErrBotLoadInvalidState, session.Status)
	})
}

func allocationBotLoadCounts(allocations []BotLoadAllocation) map[uint]int {
	out := make(map[uint]int)
	for _, allocation := range allocations {
		out[allocation.ExecutorNodeID] += allocation.PlannedCount
	}
	return out
}

func estimateBotLoadDuration(allocations []BotLoadAllocation, rate int) int {
	if rate < 1 {
		rate = defaultBotLoadConnectRate
	}
	counts := allocationBotLoadCounts(allocations)
	maxCount := 0
	for _, count := range counts {
		maxCount = max(maxCount, count)
	}
	return (maxCount + rate - 1) / rate
}

func probeBlockerMessage(probe BotLoadProbeStatus) string {
	if probe.Message != "" {
		return probe.Message
	}
	return "目标实例探针尚未连接"
}
