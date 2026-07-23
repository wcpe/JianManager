package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvaluateThresholds_AllPass(t *testing.T) {
	th := DefaultBotLoadThresholds()
	lag := 100.0
	ev := EvaluateThresholds(&th, BotLoadMetricWindow{
		HasCommandSchedule: true, HasBarrier: false,
		ExpectedBots: 100, SampleCount: 100, ExpectedSampleCount: 100,
		ConsecutiveGapSeconds: 0,
		MinOnlineRate: 0.995, MinCommandSentRate: 0.995, MinScheduleCompleteRate: 0.995,
		MinWorkerHealthRate: 1.0, ScheduleLagP95MS: &lag, ProcessCrashes: 0,
	}, nil)
	require.True(t, ev.Passed)
	require.False(t, ev.Pending)
	require.Nil(t, ev.SafetyStop)
	// 屏障 not_applicable
	var barrier *BotLoadVerdictReason
	for i := range ev.Reasons {
		if ev.Reasons[i].Key == ReasonBarrierArrivalRate {
			barrier = &ev.Reasons[i]
		}
	}
	require.NotNil(t, barrier)
	require.Equal(t, VerdictReasonNotApplicable, barrier.State)
}

func TestEvaluateThresholds_PendingNoSamples(t *testing.T) {
	th := DefaultBotLoadThresholds()
	ev := EvaluateThresholds(&th, BotLoadMetricWindow{
		HasCommandSchedule: true, SampleCount: 0, ExpectedSampleCount: 10,
	}, nil)
	require.True(t, ev.Pending)
	require.False(t, ev.Passed)
}

func TestEvaluateThresholds_FailOnlineRate(t *testing.T) {
	th := DefaultBotLoadThresholds()
	lag := 50.0
	ev := EvaluateThresholds(&th, BotLoadMetricWindow{
		HasCommandSchedule: true,
		SampleCount: 10, ExpectedSampleCount: 10,
		MinOnlineRate: 0.5, MinCommandSentRate: 1, MinScheduleCompleteRate: 1,
		MinWorkerHealthRate: 1, ScheduleLagP95MS: &lag, ProcessCrashes: 0,
	}, nil)
	require.False(t, ev.Passed)
	require.False(t, ev.Pending)
	found := false
	for _, r := range ev.Reasons {
		if r.Key == ReasonOnlineRate && r.State == VerdictReasonFail {
			found = true
		}
	}
	require.True(t, found)
}

func TestEvaluateThresholds_ConsecutiveGapFails(t *testing.T) {
	th := DefaultBotLoadThresholds()
	lag := 10.0
	ev := EvaluateThresholds(&th, BotLoadMetricWindow{
		HasCommandSchedule: true,
		SampleCount: 5, ExpectedSampleCount: 5, ConsecutiveGapSeconds: 31,
		MinOnlineRate: 1, MinCommandSentRate: 1, MinScheduleCompleteRate: 1,
		MinWorkerHealthRate: 1, ScheduleLagP95MS: &lag, ProcessCrashes: 0,
	}, nil)
	require.False(t, ev.Passed)
}

func TestEvaluateThresholds_SafetyStopRequiresSustain(t *testing.T) {
	th := DefaultBotLoadThresholds()
	lag := 10.0
	mem := 0.99
	// 未 sustain → 不触发 safety stop
	ev := EvaluateThresholds(&th, BotLoadMetricWindow{
		HasCommandSchedule: true, HasSafety: true, SafetySustainMet: false,
		SampleCount: 5, ExpectedSampleCount: 5,
		MinOnlineRate: 1, MinCommandSentRate: 1, MinScheduleCompleteRate: 1,
		MinWorkerHealthRate: 1, ScheduleLagP95MS: &lag, ProcessCrashes: 0,
		SafetyMemoryRateSustained: &mem,
	}, nil)
	require.Nil(t, ev.SafetyStop)

	// sustain → 触发
	ev2 := EvaluateThresholds(&th, BotLoadMetricWindow{
		HasCommandSchedule: true, HasSafety: true, SafetySustainMet: true,
		SampleCount: 5, ExpectedSampleCount: 5,
		MinOnlineRate: 1, MinCommandSentRate: 1, MinScheduleCompleteRate: 1,
		MinWorkerHealthRate: 1, ScheduleLagP95MS: &lag, ProcessCrashes: 0,
		SafetyMemoryRateSustained: &mem,
	}, nil)
	require.NotNil(t, ev2.SafetyStop)
	require.Equal(t, ReasonSafetyExecutorMemory, ev2.SafetyStop.Key)
	require.False(t, ev2.Passed)
}

func TestEvaluateThresholds_CrashFails(t *testing.T) {
	th := DefaultBotLoadThresholds()
	lag := 10.0
	ev := EvaluateThresholds(&th, BotLoadMetricWindow{
		HasCommandSchedule: true,
		SampleCount: 5, ExpectedSampleCount: 5,
		MinOnlineRate: 1, MinCommandSentRate: 1, MinScheduleCompleteRate: 1,
		MinWorkerHealthRate: 1, ScheduleLagP95MS: &lag, ProcessCrashes: 1,
	}, nil)
	require.False(t, ev.Passed)
}

func TestClassifyBotLoadError(t *testing.T) {
	c, leg := ClassifyBotLoadError("COMMAND_SEND_FAILED")
	require.Equal(t, BotLoadFailureScenario, c)
	require.Empty(t, leg)

	c, leg = ClassifyBotLoadError("PROBE_EVENT_TIMEOUT")
	require.Equal(t, BotLoadFailureScenario, c)
	require.Equal(t, "probe", leg)

	c, _ = ClassifyBotLoadError("WORKER_UNREACHABLE")
	require.Equal(t, BotLoadFailureExecutor, c)

	c, _ = ClassifyBotLoadError("ECONNREFUSED")
	require.Equal(t, BotLoadFailureNetwork, c)

	c, _ = ClassifyBotLoadError("SOMETHING_UNKNOWN")
	require.Equal(t, BotLoadFailureInternal, c)
}
