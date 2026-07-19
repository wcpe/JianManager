package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

const legacyScenarioDurationMS = 3_600_000

// ConvertStressOrchestrationToScenarioV2 把旧编排转换为单 cohort 兼容 snapshot。
func ConvertStressOrchestrationToScenarioV2(raw string, seed int64) (*ScenarioV2, error) {
	orchestration, _, err := ParseStressOrchestrationYAML(raw)
	if err != nil {
		return nil, err
	}
	if orchestration == nil {
		return nil, fmt.Errorf("%w: 旧编排为空", ErrBotStressSessionInvalid)
	}
	steps := make([]ScenarioAction, 0, len(orchestration.Phases))
	for phaseIndex, phase := range orchestration.Phases {
		converted, err := convertLegacyPhase(phase, phaseIndex)
		if err != nil {
			return nil, err
		}
		steps = append(steps, converted...)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("%w: 旧编排未产生动作", ErrBotStressSessionInvalid)
	}
	steps[len(steps)-1].Base().ObservationStep = true
	scenario := newLegacyScenario(seed, steps)
	if err := validateScenarioV2(scenario, true); err != nil {
		return nil, err
	}
	return scenario, nil
}

// ConvertLegacyBehaviorToScenarioV2 把旧单 behavior 转换为兼容 snapshot。
func ConvertLegacyBehaviorToScenarioV2(behavior string, seed int64) (*ScenarioV2, error) {
	behavior = strings.TrimSpace(behavior)
	if behavior == "" {
		return nil, fmt.Errorf("%w: 旧 behavior 为空", ErrBotStressSessionInvalid)
	}
	phase := StressOrchestrationPhase{DurationSec: legacyScenarioDurationMS / 1000, Behavior: behavior}
	steps, err := convertLegacyPhase(phase, 0)
	if err != nil {
		return nil, err
	}
	steps[len(steps)-1].Base().ObservationStep = true
	scenario := newLegacyScenario(seed, steps)
	if err := validateScenarioV2(scenario, true); err != nil {
		return nil, err
	}
	return scenario, nil
}

func newLegacyScenario(seed int64, steps []ScenarioAction) *ScenarioV2 {
	return &ScenarioV2{
		Version: 2, Seed: seed, seedPresent: true,
		Cohorts: []ScenarioCohort{{Key: "legacy", Percent: 100, Steps: steps}},
	}
}

func convertLegacyPhase(phase StressOrchestrationPhase, phaseIndex int) ([]ScenarioAction, error) {
	durationMS := phase.DurationSec * 1000
	id := fmt.Sprintf("phase-%02d", phaseIndex+1)
	switch phase.Behavior {
	case "idle", "wait":
		return []ScenarioAction{newWaitScenarioAction(id, durationMS)}, nil
	case "guard":
		return []ScenarioAction{newLegacyGuardScenarioAction(id, durationMS)}, nil
	case "custom":
		return convertLegacyCustomPhase(phase, phaseIndex)
	case "follow", "patrol", "roam":
		return []ScenarioAction{newLegacyBehaviorScenarioAction(id, phase.Behavior, phase.Target, durationMS, nil)}, nil
	default:
		return nil, fmt.Errorf("%w: 无法转换 behavior %s", ErrBotStressSessionInvalid, phase.Behavior)
	}
}

func convertLegacyCustomPhase(phase StressOrchestrationPhase, phaseIndex int) ([]ScenarioAction, error) {
	remaining := phase.DurationSec * 1000
	out := make([]ScenarioAction, 0, len(phase.Steps))
	for stepIndex := range phase.Steps {
		step := phase.Steps[stepIndex]
		id := fmt.Sprintf("phase-%02d-step-%02d", phaseIndex+1, stepIndex+1)
		action, consumed, err := convertLegacyCustomStep(id, step, remaining)
		if err != nil {
			return nil, err
		}
		out = append(out, action)
		remaining = max(1, remaining-consumed)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: custom 阶段 steps 不能为空", ErrBotStressSessionInvalid)
	}
	return out, nil
}

func convertLegacyCustomStep(id string, step StressOrchestrationStep, remaining int) (ScenarioAction, int, error) {
	duration := legacyStepDuration(step)
	switch step.Type {
	case "chat":
		return newSendCommandScenarioAction(id, step.Message), 0, nil
	case "wait":
		if duration <= 0 {
			duration = remaining
		}
		return newWaitScenarioAction(id, duration), duration, nil
	case "move":
		position, err := legacyStepPosition(step)
		if err != nil {
			return ScenarioAction{}, 0, err
		}
		return newMoveScenarioAction(id, position, remaining), duration, nil
	case "attack":
		return newLegacyAttackScenarioAction(id, remaining), remaining, nil
	case "interact", "use_item":
		copyStep := step
		return newLegacyBehaviorScenarioAction(id, "custom", "", max(1, remaining), &copyStep), duration, nil
	default:
		return ScenarioAction{}, 0, fmt.Errorf("%w: 无法转换 custom step %s", ErrBotStressSessionInvalid, step.Type)
	}
}

func legacyStepDuration(step StressOrchestrationStep) int {
	if step.DurationMS > 0 {
		return step.DurationMS
	}
	return step.Duration
}

func legacyStepPosition(step StressOrchestrationStep) (ScenarioPosition, error) {
	x, xOK := step.Pos["x"]
	y, yOK := step.Pos["y"]
	z, zOK := step.Pos["z"]
	if !xOK || !yOK || !zOK {
		return ScenarioPosition{}, fmt.Errorf("%w: move step 缺少完整坐标", ErrBotStressSessionInvalid)
	}
	return ScenarioPosition{X: x, Y: y, Z: z}, nil
}

func newScenarioActionBase(id string, actionType ScenarioActionType) ScenarioActionBase {
	return ScenarioActionBase{ID: id, ActionType: actionType}
}

func newWaitScenarioAction(id string, durationMS int) ScenarioAction {
	value := &WaitAction{ScenarioActionBase: newScenarioActionBase(id, ScenarioActionWait), DurationMS: max(1, durationMS)}
	return ScenarioAction{Wait: value}
}

func newSendCommandScenarioAction(id, command string) ScenarioAction {
	value := &SendCommandAction{ScenarioActionBase: newScenarioActionBase(id, ScenarioActionSendCommand), Command: command}
	return ScenarioAction{SendCommand: value}
}

func newMoveScenarioAction(id string, position ScenarioPosition, timeoutMS int) ScenarioAction {
	base := newScenarioActionBase(id, ScenarioActionMoveToAndWait)
	timeoutMS = min(maxScenarioTimeoutMS, max(minScenarioTimeoutMS, timeoutMS))
	base.TimeoutMS = &timeoutMS
	value := &MoveToAndWaitAction{ScenarioActionBase: base, Pos: position, Radius: 1}
	return ScenarioAction{MoveToAndWait: value}
}

func newLegacyAttackScenarioAction(id string, durationMS int) ScenarioAction {
	value := &AttackUntilAction{
		ScenarioActionBase: newScenarioActionBase(id, ScenarioActionAttackUntil),
		Selector:           ScenarioEntitySelector{Kind: "hostile", Radius: 16, Priority: "nearest"},
		Stop:               ScenarioAttackStop{DurationMS: max(1, durationMS), SuccessPolicy: "any"},
		AttackIntervalMS:   1000, Chase: true, Reacquire: true,
	}
	return ScenarioAction{AttackUntil: value}
}

func newLegacyGuardScenarioAction(id string, durationMS int) ScenarioAction {
	value := newLegacyAttackScenarioAction(id, durationMS)
	value.AttackUntil.Chase = false
	return value
}

func newLegacyBehaviorScenarioAction(id, behavior, target string, durationMS int, step *StressOrchestrationStep) ScenarioAction {
	value := &LegacyBehaviorAction{
		ScenarioActionBase: newScenarioActionBase(id, ScenarioActionLegacyBehavior),
		Behavior:           behavior, Target: target, DurationMS: max(1, durationMS), Step: step,
	}
	return ScenarioAction{LegacyBehavior: value}
}

// CanonicalScenarioSnapshot 校验并生成数据库规范 JSON。
func CanonicalScenarioSnapshot(scenario *ScenarioV2, allowLegacy bool) (string, error) {
	if err := validateScenarioV2(scenario, allowLegacy); err != nil {
		return "", err
	}
	raw, err := json.Marshal(scenario)
	if err != nil {
		return "", fmt.Errorf("序列化 Scenario V2 失败: %w", err)
	}
	return string(raw), nil
}
