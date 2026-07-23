package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizeLoadProfile_Stable(t *testing.T) {
	raw := json.RawMessage(`{"type":"stable","targetBots":100,"rampUpSeconds":30,"durationSeconds":3600}`)
	p, err := NormalizeAndValidateLoadProfile(raw)
	require.NoError(t, err)
	require.Equal(t, "stable", p.Type)
	require.Equal(t, 100, p.Stable.TargetBots)
	require.Equal(t, 100, ProfileMaxTargetBots(p))
}

func TestNormalizeLoadProfile_StepStrictlyIncreasing(t *testing.T) {
	raw := json.RawMessage(`{"type":"step","stages":[{"targetBots":100,"holdSeconds":60},{"targetBots":250,"holdSeconds":60},{"targetBots":500,"holdSeconds":120}],"stopOnThresholdFailure":true}`)
	p, err := NormalizeAndValidateLoadProfile(raw)
	require.NoError(t, err)
	require.Equal(t, 500, ProfileMaxTargetBots(p))
	require.True(t, p.Step.StopOnThresholdFailure)

	// 非递增
	bad := json.RawMessage(`{"type":"step","stages":[{"targetBots":200,"holdSeconds":60},{"targetBots":100,"holdSeconds":60}],"stopOnThresholdFailure":false}`)
	_, err = NormalizeAndValidateLoadProfile(bad)
	require.ErrorIs(t, err, ErrBotLoadProfileInvalid)
}

func TestNormalizeLoadProfile_SpikeBarrier(t *testing.T) {
	raw := json.RawMessage(`{"type":"spike","targetBots":500,"connectWindowSeconds":30,"barrier":{"key":"ready","releaseWindowMs":2000},"holdSeconds":60}`)
	p, err := NormalizeAndValidateLoadProfile(raw)
	require.NoError(t, err)
	require.True(t, ProfileHasBarrier(p))
	require.Equal(t, "ready", p.Spike.Barrier.Key)

	// 无 barrier
	raw2 := json.RawMessage(`{"type":"spike","targetBots":50,"connectWindowSeconds":10,"holdSeconds":30}`)
	p2, err := NormalizeAndValidateLoadProfile(raw2)
	require.NoError(t, err)
	require.False(t, ProfileHasBarrier(p2))
}

func TestNormalizeLoadProfile_RejectsOutOfRange(t *testing.T) {
	_, err := NormalizeAndValidateLoadProfile(json.RawMessage(`{"type":"stable","targetBots":0,"rampUpSeconds":0,"durationSeconds":10}`))
	require.ErrorIs(t, err, ErrBotLoadProfileInvalid)

	_, err = NormalizeAndValidateLoadProfile(json.RawMessage(`{"type":"stable","targetBots":10,"rampUpSeconds":0,"durationSeconds":5}`))
	require.ErrorIs(t, err, ErrBotLoadProfileInvalid)
}

func TestNormalizeThresholds_DefaultAndValidate(t *testing.T) {
	t1, err := NormalizeAndValidateThresholds(nil)
	require.NoError(t, err)
	require.Equal(t, 0.99, t1.MinOnlineRate)
	require.NotNil(t, t1.Safety)

	raw := json.RawMessage(`{"minOnlineRate":0.95,"minCommandSentRate":0.9,"minScheduleCompletionRate":0.9,"minWorkerHealthRate":0.99,"minBarrierArrivalRate":0.99,"maxScheduleLagP95Ms":500,"maxProcessCrashes":0}`)
	t2, err := NormalizeAndValidateThresholds(raw)
	require.NoError(t, err)
	require.Equal(t, 0.95, t2.MinOnlineRate)
	require.Equal(t, 500, t2.MaxScheduleLagP95MS)

	// rate 越界
	_, err = NormalizeAndValidateThresholds(json.RawMessage(`{"minOnlineRate":1.5,"minCommandSentRate":0.9,"minScheduleCompletionRate":0.9,"minWorkerHealthRate":0.99,"minBarrierArrivalRate":0.99,"maxScheduleLagP95Ms":500,"maxProcessCrashes":0}`))
	require.ErrorIs(t, err, ErrBotLoadThresholdsInvalid)
}

func TestPlanLoadProfile_StableConnectDistribution(t *testing.T) {
	p, err := NormalizeAndValidateLoadProfile(json.RawMessage(`{"type":"stable","targetBots":5,"rampUpSeconds":10,"durationSeconds":60}`))
	require.NoError(t, err)
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	plan, err := PlanLoadProfile(p, base)
	require.NoError(t, err)
	require.Equal(t, "stable", plan.Type)
	require.Len(t, plan.Stages, 1)
	require.Equal(t, 5, plan.Stages[0].DeltaBots)
	require.Len(t, plan.Stages[0].ConnectNotBefore, 5)
	// 首个=base，末个 < base+10s
	require.Equal(t, base, plan.Stages[0].ConnectNotBefore[0])
	require.True(t, plan.Stages[0].ConnectNotBefore[4].Before(base.Add(10*time.Second)) || plan.Stages[0].ConnectNotBefore[4].Equal(base.Add(8*time.Second)))
}

func TestPlanLoadProfile_StepDeltaOnly(t *testing.T) {
	p, err := NormalizeAndValidateLoadProfile(json.RawMessage(`{"type":"step","stages":[{"targetBots":100,"holdSeconds":60},{"targetBots":250,"holdSeconds":60}],"stopOnThresholdFailure":true}`))
	require.NoError(t, err)
	plan, err := PlanLoadProfile(p, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, 100, plan.Stages[0].DeltaBots)
	require.Equal(t, 150, plan.Stages[1].DeltaBots) // 250-100
	require.True(t, plan.StopOnFailure)
	require.Equal(t, 250, plan.MaxTargetBots)
}

func TestPlanLoadProfile_SpikeBarrierFlags(t *testing.T) {
	p, err := NormalizeAndValidateLoadProfile(json.RawMessage(`{"type":"spike","targetBots":10,"connectWindowSeconds":5,"barrier":{"key":"go","releaseWindowMs":1000},"holdSeconds":30}`))
	require.NoError(t, err)
	plan, err := PlanLoadProfile(p, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, plan.Stages[0].HasBarrier)
	require.Equal(t, "go", plan.Stages[0].BarrierKey)
	require.Equal(t, 1000, plan.Stages[0].ReleaseWindowMS)
}
