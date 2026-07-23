package service

import (
	"fmt"
	"math"
)

// BotLoadVerdictReasonState 判定理由状态。
type BotLoadVerdictReasonState string

const (
	VerdictReasonPass          BotLoadVerdictReasonState = "pass"
	VerdictReasonFail          BotLoadVerdictReasonState = "fail"
	VerdictReasonPending       BotLoadVerdictReasonState = "pending"
	VerdictReasonNotApplicable BotLoadVerdictReasonState = "not_applicable"
)

// BotLoadVerdictReasonKey 稳定判定键。
type BotLoadVerdictReasonKey string

const (
	ReasonOnlineRate               BotLoadVerdictReasonKey = "online_rate"
	ReasonCommandSentRate          BotLoadVerdictReasonKey = "command_sent_rate"
	ReasonScheduleCompletionRate   BotLoadVerdictReasonKey = "schedule_completion_rate"
	ReasonWorkerHealthRate         BotLoadVerdictReasonKey = "worker_health_rate"
	ReasonBarrierArrivalRate       BotLoadVerdictReasonKey = "barrier_arrival_rate"
	ReasonScheduleLagP95MS         BotLoadVerdictReasonKey = "schedule_lag_p95_ms"
	ReasonProcessCrashes           BotLoadVerdictReasonKey = "process_crashes"
	ReasonSampleCoverageRate       BotLoadVerdictReasonKey = "sample_coverage_rate"
	ReasonConsecutiveSampleGapSecs BotLoadVerdictReasonKey = "consecutive_sample_gap_seconds"
	ReasonSafetyExecutorMemory     BotLoadVerdictReasonKey = "safety_executor_memory_rate"
	ReasonSafetyEventLoopP95       BotLoadVerdictReasonKey = "safety_event_loop_p95_ms"
)

// BotLoadVerdictReason 单条判定理由。
type BotLoadVerdictReason struct {
	Key        BotLoadVerdictReasonKey   `json:"key"`
	State      BotLoadVerdictReasonState `json:"state"`
	Expected   any                       `json:"expected,omitempty"`
	Actual     any                       `json:"actual,omitempty"`
	Unit       string                    `json:"unit,omitempty"`
	Message    string                    `json:"message"`
	StageIndex *int                      `json:"stageIndex,omitempty"`
}

// BotLoadEvaluation 阈值评估输出。
type BotLoadEvaluation struct {
	Passed     bool
	Pending    bool
	Reasons    []BotLoadVerdictReason
	SafetyStop *BotLoadVerdictReason
}

// BotLoadMetricWindow 观察窗内聚合输入（窗口内最小值，非累计平均）。
type BotLoadMetricWindow struct {
	// 适用性
	HasCommandSchedule bool
	HasBarrier         bool
	HasSafety          bool

	// 分母/覆盖
	ExpectedBots            int
	SampleCount             int
	ExpectedSampleCount     int
	ConsecutiveGapSeconds   int
	MinOnlineRate           float64
	MinCommandSentRate      float64
	MinScheduleCompleteRate float64
	MinWorkerHealthRate     float64
	MinBarrierArrivalRate   float64
	ScheduleLagP95MS        *float64 // nil = 无样本 → pending
	ProcessCrashes          int

	// safety 连续 sustain 秒内持续超标
	SafetyMemoryRateSustained    *float64 // 持续窗口内最大 memory rate
	SafetyEventLoopP95Sustained  *float64
	SafetySustainMet             bool // 是否已连续 sustainSeconds 超标
}

// EvaluateThresholds 按冻结阈值评估当前窗口。
func EvaluateThresholds(th *BotLoadThresholds, win BotLoadMetricWindow, stageIndex *int) BotLoadEvaluation {
	if th == nil {
		def := DefaultBotLoadThresholds()
		th = &def
	}
	ev := BotLoadEvaluation{Passed: true, Pending: false, Reasons: make([]BotLoadVerdictReason, 0, 12)}

	// 样本覆盖率
	coverage := 1.0
	if win.ExpectedSampleCount > 0 {
		coverage = float64(win.SampleCount) / float64(win.ExpectedSampleCount)
	}
	covState := VerdictReasonPass
	if win.SampleCount == 0 {
		covState = VerdictReasonPending
		ev.Pending = true
		ev.Passed = false
	} else if coverage < 0.99 {
		covState = VerdictReasonFail
		ev.Passed = false
	}
	ev.Reasons = append(ev.Reasons, reason(ReasonSampleCoverageRate, covState, 0.99, coverage, "ratio",
		fmt.Sprintf("样本覆盖率 %.4f（阈值 ≥0.99）", coverage), stageIndex))

	// 连续缺样
	gapState := VerdictReasonPass
	if win.ConsecutiveGapSeconds > 30 {
		gapState = VerdictReasonFail
		ev.Passed = false
	} else if win.SampleCount == 0 {
		gapState = VerdictReasonPending
		ev.Pending = true
		ev.Passed = false
	}
	ev.Reasons = append(ev.Reasons, reason(ReasonConsecutiveSampleGapSecs, gapState, 30, win.ConsecutiveGapSeconds, "seconds",
		fmt.Sprintf("连续缺样 %d 秒（阈值 ≤30）", win.ConsecutiveGapSeconds), stageIndex))

	// 在线率
	ev.Reasons = append(ev.Reasons, rateReason(ReasonOnlineRate, th.MinOnlineRate, win.MinOnlineRate, win.SampleCount == 0, stageIndex, &ev))

	// 命令相关（commandSchedule 必须非空时始终适用）
	if win.HasCommandSchedule {
		ev.Reasons = append(ev.Reasons, rateReason(ReasonCommandSentRate, th.MinCommandSentRate, win.MinCommandSentRate, win.SampleCount == 0, stageIndex, &ev))
		ev.Reasons = append(ev.Reasons, rateReason(ReasonScheduleCompletionRate, th.MinScheduleCompletionRate, win.MinScheduleCompleteRate, win.SampleCount == 0, stageIndex, &ev))

		lagState := VerdictReasonPass
		var lagActual any
		if win.ScheduleLagP95MS == nil {
			lagState = VerdictReasonPending
			ev.Pending = true
			ev.Passed = false
			lagActual = nil
		} else {
			lagActual = *win.ScheduleLagP95MS
			if *win.ScheduleLagP95MS > float64(th.MaxScheduleLagP95MS) {
				lagState = VerdictReasonFail
				ev.Passed = false
			}
		}
		ev.Reasons = append(ev.Reasons, BotLoadVerdictReason{
			Key: ReasonScheduleLagP95MS, State: lagState, Expected: th.MaxScheduleLagP95MS,
			Actual: lagActual, Unit: "ms",
			Message:    fmt.Sprintf("schedule lag p95 阈值 ≤%d ms", th.MaxScheduleLagP95MS),
			StageIndex: stageIndex,
		})
	} else {
		ev.Reasons = append(ev.Reasons,
			naReason(ReasonCommandSentRate, stageIndex),
			naReason(ReasonScheduleCompletionRate, stageIndex),
			naReason(ReasonScheduleLagP95MS, stageIndex),
		)
	}

	// Worker 健康
	ev.Reasons = append(ev.Reasons, rateReason(ReasonWorkerHealthRate, th.MinWorkerHealthRate, win.MinWorkerHealthRate, win.SampleCount == 0, stageIndex, &ev))

	// 屏障
	if win.HasBarrier {
		ev.Reasons = append(ev.Reasons, rateReason(ReasonBarrierArrivalRate, th.MinBarrierArrivalRate, win.MinBarrierArrivalRate, win.SampleCount == 0, stageIndex, &ev))
	} else {
		ev.Reasons = append(ev.Reasons, naReason(ReasonBarrierArrivalRate, stageIndex))
	}

	// crash
	crashState := VerdictReasonPass
	if win.ProcessCrashes > th.MaxProcessCrashes {
		crashState = VerdictReasonFail
		ev.Passed = false
	}
	if win.SampleCount == 0 {
		crashState = VerdictReasonPending
		ev.Pending = true
		ev.Passed = false
	}
	ev.Reasons = append(ev.Reasons, reason(ReasonProcessCrashes, crashState, th.MaxProcessCrashes, win.ProcessCrashes, "count",
		fmt.Sprintf("非预期 crash %d（阈值 ≤%d）", win.ProcessCrashes, th.MaxProcessCrashes), stageIndex))

	// safety：需连续 sustainSeconds 才触发
	if th.Safety != nil && win.HasSafety {
		if win.SafetySustainMet && win.SafetyMemoryRateSustained != nil && *win.SafetyMemoryRateSustained > th.Safety.MaxExecutorMemoryRate {
			r := reason(ReasonSafetyExecutorMemory, VerdictReasonFail, th.Safety.MaxExecutorMemoryRate, *win.SafetyMemoryRateSustained, "ratio",
				"执行节点内存持续超标，触发安全停止", stageIndex)
			ev.Passed = false
			ev.Reasons = append(ev.Reasons, r)
			cp := r
			ev.SafetyStop = &cp
		}
		if win.SafetySustainMet && win.SafetyEventLoopP95Sustained != nil && *win.SafetyEventLoopP95Sustained > float64(th.Safety.MaxEventLoopP95MS) {
			r := reason(ReasonSafetyEventLoopP95, VerdictReasonFail, th.Safety.MaxEventLoopP95MS, *win.SafetyEventLoopP95Sustained, "ms",
				"执行节点 eventLoop p95 持续超标，触发安全停止", stageIndex)
			ev.Passed = false
			ev.Reasons = append(ev.Reasons, r)
			if ev.SafetyStop == nil {
				cp := r
				ev.SafetyStop = &cp
			}
		}
	}

	// Pending 优先：窗口未关闭且有 pending 时不记最终 fail
	if ev.Pending {
		ev.Passed = false
	}
	return ev
}

func rateReason(key BotLoadVerdictReasonKey, expected, actual float64, noSample bool, stage *int, ev *BotLoadEvaluation) BotLoadVerdictReason {
	state := VerdictReasonPass
	if noSample || math.IsNaN(actual) {
		state = VerdictReasonPending
		ev.Pending = true
		ev.Passed = false
	} else if actual < expected {
		state = VerdictReasonFail
		ev.Passed = false
	}
	return reason(key, state, expected, actual, "ratio",
		fmt.Sprintf("%s 实际 %.4f（阈值 ≥%.4f）", key, actual, expected), stage)
}

func naReason(key BotLoadVerdictReasonKey, stage *int) BotLoadVerdictReason {
	return BotLoadVerdictReason{
		Key: key, State: VerdictReasonNotApplicable,
		Message: fmt.Sprintf("%s 不适用", key), StageIndex: stage,
	}
}

func reason(key BotLoadVerdictReasonKey, state BotLoadVerdictReasonState, expected, actual any, unit, msg string, stage *int) BotLoadVerdictReason {
	return BotLoadVerdictReason{
		Key: key, State: state, Expected: expected, Actual: actual, Unit: unit,
		Message: msg, StageIndex: stage,
	}
}
