package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

type fakeBotLoadCapacityProvider struct {
	mu       sync.Mutex
	snapshot BotLoadCapacitySnapshot
	excludes []uint
}

func (p *fakeBotLoadCapacityProvider) Snapshot(_ context.Context, excludeRunID uint) (*BotLoadCapacitySnapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.excludes = append(p.excludes, excludeRunID)
	out := p.snapshot
	out.NodeCapacities = append([]BotLoadNodeCapacity(nil), p.snapshot.NodeCapacities...)
	out.ReservationLimits = cloneBotLoadCounts(p.snapshot.ReservationLimits)
	return &out, nil
}

func newBotLoadPreflightDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:bot-load-preflight-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.BotStressSession{}, &model.BotLoadBatch{}, &model.Bot{}))
	return db
}

func createBotLoadSession(t *testing.T, db *gorm.DB, suffix string, count int) model.BotStressSession {
	t.Helper()
	node := model.Node{Name: "node-" + suffix, Host: "127.0.0.1", Secret: "secret", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(&node).Error)
	instance := model.Instance{NodeID: node.ID, Name: "target-" + suffix, Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, WorkDir: "var/" + suffix, StartCommand: "java"}
	require.NoError(t, db.Create(&instance).Error)
	session := model.BotStressSession{InstanceID: instance.ID, Name: "run-" + suffix, NamePrefix: "load", BotCount: count, Status: model.BotStressSessionPending}
	require.NoError(t, db.Create(&session).Error)
	session.Instance = instance
	return session
}

func TestBotLoadPreflight_ReadyPersistsPlanAndReservesWithoutCreatingBots(t *testing.T) {
	db := newBotLoadPreflightDB(t)
	session := createBotLoadSession(t, db, "ready", 50)
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	reservations := NewBotLoadReservationStore(clock, time.Minute)
	signer, err := NewBotLoadPlanTokenSigner([]byte("test-only-plan-token-secret"), clock)
	require.NoError(t, err)
	provider := &fakeBotLoadCapacityProvider{snapshot: BotLoadCapacitySnapshot{
		NodeCapacities:    []BotLoadNodeCapacity{readyBotLoadNode(10, 50, 7)},
		ReservationLimits: map[uint]int{10: 50}, UpdatedAt: clock.Now(),
	}}
	svc := NewBotLoadPreflightService(db, provider, reservations, signer, clock)

	result, err := svc.Preflight(context.Background(), &session, BotLoadPreflightInput{
		TargetBots: 50, ConnectRatePerSecondPerNode: 5,
		Probe: BotLoadProbeStatus{Required: false, Connected: true, InstanceID: session.InstanceID, InstanceUUID: session.Instance.UUID},
	})
	require.NoError(t, err)
	require.True(t, result.Ready)
	require.Equal(t, session.ID, result.RunID)
	require.Equal(t, session.UUID, result.RunUUID)
	require.Equal(t, 50, result.TargetBots)
	require.Equal(t, 50, result.TotalAvailable)
	require.Len(t, result.Allocations, 1)
	require.NotEmpty(t, result.PlanToken)
	require.NotNil(t, result.ExpiresAt)
	require.Equal(t, 10, result.EstimatedDurationSeconds)
	require.Equal(t, 50, reservations.Snapshot(0)[10])

	var saved model.BotStressSession
	require.NoError(t, db.First(&saved, session.ID).Error)
	require.NotEmpty(t, saved.AllocationPlan)
	plan, err := DecodeBotLoadAllocationPlan(saved.AllocationPlan)
	require.NoError(t, err)
	require.Equal(t, result.Allocations, plan.Allocations)
	hash, err := BotLoadAllocationHash(plan.RunID, plan.TargetBots, plan.Allocations)
	require.NoError(t, err)
	require.NoError(t, signer.Verify(result.PlanToken, BotLoadPlanTokenExpectation{
		RunID: session.ID, AllocationHash: hash, CapacityGenerations: plan.CapacityGenerations,
	}))

	var botCount, batchCount int64
	require.NoError(t, db.Model(&model.Bot{}).Count(&botCount).Error)
	require.NoError(t, db.Model(&model.BotLoadBatch{}).Count(&batchCount).Error)
	require.Zero(t, botCount)
	require.Zero(t, batchCount)
}

func TestBotLoadPreflight_NotReadyHasNoSideEffects(t *testing.T) {
	tests := []struct {
		name     string
		capacity int
		probe    BotLoadProbeStatus
		wantCode string
	}{
		{"容量不足", 20, BotLoadProbeStatus{Connected: true}, BotLoadCapacityInsufficientCode},
		{"探针未连接", 50, BotLoadProbeStatus{Required: true, Connected: false}, BotLoadProbeRequiredCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newBotLoadPreflightDB(t)
			session := createBotLoadSession(t, db, tt.name, 50)
			clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
			reservations := NewBotLoadReservationStore(clock, time.Minute)
			signer, err := NewBotLoadPlanTokenSigner([]byte("test-only-plan-token-secret"), clock)
			require.NoError(t, err)
			provider := &fakeBotLoadCapacityProvider{snapshot: BotLoadCapacitySnapshot{
				NodeCapacities:    []BotLoadNodeCapacity{readyBotLoadNode(1, tt.capacity, 1)},
				ReservationLimits: map[uint]int{1: tt.capacity}, UpdatedAt: clock.Now(),
			}}
			svc := NewBotLoadPreflightService(db, provider, reservations, signer, clock)

			result, err := svc.Preflight(context.Background(), &session, BotLoadPreflightInput{TargetBots: 50, Probe: tt.probe})
			require.NoError(t, err)
			require.False(t, result.Ready)
			require.Empty(t, result.PlanToken)
			require.Contains(t, blockerCodes(result.Blockers), tt.wantCode)
			require.Empty(t, reservations.Snapshot(0))
			var saved model.BotStressSession
			require.NoError(t, db.First(&saved, session.ID).Error)
			require.Empty(t, saved.AllocationPlan)
		})
	}
}

func TestBotLoadPreflight_ConcurrentRunsDoNotOversell(t *testing.T) {
	db := newBotLoadPreflightDB(t)
	run1 := createBotLoadSession(t, db, "race-1", 30)
	run2 := createBotLoadSession(t, db, "race-2", 30)
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	reservations := NewBotLoadReservationStore(clock, time.Minute)
	signer, err := NewBotLoadPlanTokenSigner([]byte("test-only-plan-token-secret"), clock)
	require.NoError(t, err)
	provider := &fakeBotLoadCapacityProvider{snapshot: BotLoadCapacitySnapshot{
		NodeCapacities:    []BotLoadNodeCapacity{readyBotLoadNode(1, 50, 1)},
		ReservationLimits: map[uint]int{1: 50}, UpdatedAt: clock.Now(),
	}}
	svc := NewBotLoadPreflightService(db, provider, reservations, signer, clock)

	results := make(chan *BotLoadPreflightResult, 2)
	errs := make(chan error, 2)
	for _, session := range []*model.BotStressSession{&run1, &run2} {
		go func(session *model.BotStressSession) {
			result, preflightErr := svc.Preflight(context.Background(), session, BotLoadPreflightInput{TargetBots: 30})
			results <- result
			errs <- preflightErr
		}(session)
	}
	ready := 0
	for i := 0; i < 2; i++ {
		require.NoError(t, <-errs)
		if result := <-results; result.Ready {
			ready++
		}
	}
	require.Equal(t, 1, ready)
	require.LessOrEqual(t, reservations.Snapshot(0)[1], 50)
}

func TestBotLoadPreflight_StalePendingSessionCannotOverwriteStartedPlan(t *testing.T) {
	db := newBotLoadPreflightDB(t)
	session := createBotLoadSession(t, db, "started", 20)
	originalPlan := `{"runId":1,"frozen":true}`
	require.NoError(t, db.Model(&model.BotStressSession{}).Where("id = ?", session.ID).Updates(map[string]any{
		"status": model.BotStressSessionRunning, "allocation_plan": originalPlan,
	}).Error)

	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	reservations := NewBotLoadReservationStore(clock, time.Minute)
	signer, err := NewBotLoadPlanTokenSigner([]byte("test-only-plan-token-secret"), clock)
	require.NoError(t, err)
	provider := &fakeBotLoadCapacityProvider{snapshot: BotLoadCapacitySnapshot{
		NodeCapacities:    []BotLoadNodeCapacity{readyBotLoadNode(1, 50, 3)},
		ReservationLimits: map[uint]int{1: 50}, UpdatedAt: clock.Now(),
	}}
	svc := NewBotLoadPreflightService(db, provider, reservations, signer, clock)

	_, err = svc.Preflight(context.Background(), &session, BotLoadPreflightInput{TargetBots: 20})
	require.ErrorIs(t, err, ErrBotLoadInvalidState)
	require.Empty(t, reservations.Snapshot(0))
	var saved model.BotStressSession
	require.NoError(t, db.First(&saved, session.ID).Error)
	require.Equal(t, model.BotStressSessionRunning, saved.Status)
	require.Equal(t, originalPlan, saved.AllocationPlan)
}

func TestBotLoadPreflight_SameRunAtomicallyReplacesReservation(t *testing.T) {
	db := newBotLoadPreflightDB(t)
	session := createBotLoadSession(t, db, "replace", 40)
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	reservations := NewBotLoadReservationStore(clock, time.Minute)
	signer, err := NewBotLoadPlanTokenSigner([]byte("test-only-plan-token-secret"), clock)
	require.NoError(t, err)
	provider := &fakeBotLoadCapacityProvider{snapshot: BotLoadCapacitySnapshot{
		NodeCapacities:    []BotLoadNodeCapacity{readyBotLoadNode(1, 50, 3)},
		ReservationLimits: map[uint]int{1: 50}, UpdatedAt: clock.Now(),
	}}
	svc := NewBotLoadPreflightService(db, provider, reservations, signer, clock)

	first, err := svc.Preflight(context.Background(), &session, BotLoadPreflightInput{TargetBots: 40})
	require.NoError(t, err)
	require.True(t, first.Ready)
	second, err := svc.Preflight(context.Background(), &session, BotLoadPreflightInput{TargetBots: 20})
	require.NoError(t, err)
	require.True(t, second.Ready)
	require.Equal(t, 20, reservations.Snapshot(0)[1])
	require.Equal(t, []uint{session.ID, session.ID}, provider.excludes)

	var saved model.BotStressSession
	require.NoError(t, db.First(&saved, session.ID).Error)
	plan, err := DecodeBotLoadAllocationPlan(saved.AllocationPlan)
	require.NoError(t, err)
	require.Equal(t, 20, plan.TargetBots)
}

func TestBotLoadPreflight_PersistFailureRollsBackReservation(t *testing.T) {
	db := newBotLoadPreflightDB(t)
	session := createBotLoadSession(t, db, "rollback", 20)
	require.NoError(t, db.Delete(&session).Error)
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	reservations := NewBotLoadReservationStore(clock, time.Minute)
	signer, err := NewBotLoadPlanTokenSigner([]byte("test-only-plan-token-secret"), clock)
	require.NoError(t, err)
	provider := &fakeBotLoadCapacityProvider{snapshot: BotLoadCapacitySnapshot{
		NodeCapacities:    []BotLoadNodeCapacity{readyBotLoadNode(1, 50, 3)},
		ReservationLimits: map[uint]int{1: 50}, UpdatedAt: clock.Now(),
	}}
	svc := NewBotLoadPreflightService(db, provider, reservations, signer, clock)

	_, err = svc.Preflight(context.Background(), &session, BotLoadPreflightInput{TargetBots: 20})
	require.Error(t, err)
	require.Empty(t, reservations.Snapshot(0))
}

func TestBotLoadPlanToken_ActiveUtilizationDoesNotInvalidateGenerationContract(t *testing.T) {
	clock := &botLoadFakeClock{now: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
	signer, err := NewBotLoadPlanTokenSigner([]byte("test-only-plan-token-secret"), clock)
	require.NoError(t, err)
	generations := []BotLoadNodeGeneration{{NodeID: 1, CapacityGeneration: 5}}
	token, _, err := signer.Issue(1, "hash", generations)
	require.NoError(t, err)

	// active/connecting 属于即时利用率，不进入 token；同一容量语义世代继续有效。
	require.NoError(t, signer.Verify(token, BotLoadPlanTokenExpectation{RunID: 1, AllocationHash: "hash", CapacityGenerations: generations}))
	changed := []BotLoadNodeGeneration{{NodeID: 1, CapacityGeneration: 6}}
	err = signer.Verify(token, BotLoadPlanTokenExpectation{RunID: 1, AllocationHash: "hash", CapacityGenerations: changed})
	require.True(t, errors.Is(err, ErrBotLoadCapacityChanged))
}
