package service

import (
	"fmt"
	"time"
)

// ProfileStagePlan 描述某一级/阶段的派发计划。
type ProfileStagePlan struct {
	StageIndex       int
	TargetBots       int
	DeltaBots        int // 相对上一级需新增的 Bot 数
	HoldSeconds      int
	ConnectNotBefore []time.Time // 本级差量 Bot 的 connectNotBefore，长度=DeltaBots
	HasBarrier       bool
	BarrierKey       string
	ReleaseWindowMS  int
	ConnectWindowSec int // spike 专用
}

// ProfileRunPlan 是整次运行的 profile 展开结果。
type ProfileRunPlan struct {
	Type           string
	MaxTargetBots  int
	Stages         []ProfileStagePlan
	StopOnFailure  bool // step 专用
	DurationSecs   int  // stable 观察时长
	RampUpSecs     int  // stable 爬坡
}

// PlanLoadProfile 将规范化 profile 展开为阶段计划。
// baseTime 为 stage 0 派发起点；step 各级 connect 起点顺序递延。
func PlanLoadProfile(profile *BotLoadProfile, baseTime time.Time) (*ProfileRunPlan, error) {
	if profile == nil {
		return nil, fmt.Errorf("%w: profile 为空", ErrBotLoadProfileInvalid)
	}
	baseTime = baseTime.UTC()
	switch profile.Type {
	case "stable":
		return planStable(profile.Stable, baseTime)
	case "step":
		return planStep(profile.Step, baseTime)
	case "spike":
		return planSpike(profile.Spike, baseTime)
	default:
		return nil, fmt.Errorf("%w: 未知 type", ErrBotLoadProfileInvalid)
	}
}

func planStable(s *BotLoadProfileStable, base time.Time) (*ProfileRunPlan, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: stable 为空", ErrBotLoadProfileInvalid)
	}
	connects := distributeConnectNotBefore(base, s.TargetBots, s.RampUpSeconds)
	return &ProfileRunPlan{
		Type:          "stable",
		MaxTargetBots: s.TargetBots,
		DurationSecs:  s.DurationSeconds,
		RampUpSecs:    s.RampUpSeconds,
		Stages: []ProfileStagePlan{{
			StageIndex:       0,
			TargetBots:       s.TargetBots,
			DeltaBots:        s.TargetBots,
			HoldSeconds:      s.DurationSeconds,
			ConnectNotBefore: connects,
		}},
	}, nil
}

func planStep(s *BotLoadProfileStep, base time.Time) (*ProfileRunPlan, error) {
	if s == nil || len(s.Stages) == 0 {
		return nil, fmt.Errorf("%w: step 为空", ErrBotLoadProfileInvalid)
	}
	stages := make([]ProfileStagePlan, 0, len(s.Stages))
	prev := 0
	cursor := base
	maxTarget := 0
	for i, st := range s.Stages {
		delta := st.TargetBots - prev
		// 差量 Bot 在 1 秒窗口内均匀分布（阶梯进入连接宽限）
		connects := distributeConnectNotBefore(cursor, delta, max(1, min(delta, 10)))
		stages = append(stages, ProfileStagePlan{
			StageIndex:       i,
			TargetBots:       st.TargetBots,
			DeltaBots:        delta,
			HoldSeconds:      st.HoldSeconds,
			ConnectNotBefore: connects,
		})
		prev = st.TargetBots
		maxTarget = st.TargetBots
		// 下一级起点：当前 hold + 60s 宽限（规划用，实际 runner 以条件触发）
		cursor = cursor.Add(time.Duration(st.HoldSeconds+60) * time.Second)
	}
	return &ProfileRunPlan{
		Type:          "step",
		MaxTargetBots: maxTarget,
		Stages:        stages,
		StopOnFailure: s.StopOnThresholdFailure,
	}, nil
}

func planSpike(s *BotLoadProfileSpike, base time.Time) (*ProfileRunPlan, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: spike 为空", ErrBotLoadProfileInvalid)
	}
	connects := distributeConnectNotBefore(base, s.TargetBots, s.ConnectWindowSeconds)
	stage := ProfileStagePlan{
		StageIndex:       0,
		TargetBots:       s.TargetBots,
		DeltaBots:        s.TargetBots,
		HoldSeconds:      s.HoldSeconds,
		ConnectNotBefore: connects,
		ConnectWindowSec: s.ConnectWindowSeconds,
	}
	if s.Barrier != nil {
		stage.HasBarrier = true
		stage.BarrierKey = s.Barrier.Key
		stage.ReleaseWindowMS = s.Barrier.ReleaseWindowMS
	}
	return &ProfileRunPlan{
		Type:          "spike",
		MaxTargetBots: s.TargetBots,
		Stages:        []ProfileStagePlan{stage},
	}, nil
}

// distributeConnectNotBefore 在 windowSeconds 内均匀生成 n 个 connectNotBefore。
// windowSeconds=0 时全部等于 base。
func distributeConnectNotBefore(base time.Time, n, windowSeconds int) []time.Time {
	if n <= 0 {
		return nil
	}
	out := make([]time.Time, n)
	if windowSeconds <= 0 || n == 1 {
		for i := 0; i < n; i++ {
			out[i] = base
		}
		return out
	}
	// 均匀分片：第 i 个位于 i * window / n
	for i := 0; i < n; i++ {
		offsetMS := int64(i) * int64(windowSeconds) * 1000 / int64(n)
		out[i] = base.Add(time.Duration(offsetMS) * time.Millisecond)
	}
	return out
}
