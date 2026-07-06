package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// StressOrchestration 表示 Bot 压测会话的 YAML 编排。
type StressOrchestration struct {
	Loop      bool                       `json:"loop" yaml:"loop"`
	StaggerMS int                        `json:"staggerMs" yaml:"staggerMs"`
	Phases    []StressOrchestrationPhase `json:"phases" yaml:"phases"`
}

// StressOrchestrationPhase 表示一个编排阶段。
type StressOrchestrationPhase struct {
	DurationSec int                       `json:"durationSec" yaml:"durationSec"`
	Behavior    string                    `json:"behavior" yaml:"behavior"`
	Target      string                    `json:"target,omitempty" yaml:"target,omitempty"`
	Steps       []StressOrchestrationStep `json:"steps,omitempty" yaml:"steps,omitempty"`
}

// StressOrchestrationStep 表示 custom 行为中的一个动作步骤。
type StressOrchestrationStep struct {
	Type       string             `json:"type" yaml:"type"`
	Message    string             `json:"message,omitempty" yaml:"message,omitempty"`
	DurationMS int                `json:"durationMs,omitempty" yaml:"durationMs,omitempty"`
	Duration   int                `json:"duration,omitempty" yaml:"duration,omitempty"`
	Pos        map[string]float64 `json:"pos,omitempty" yaml:"pos,omitempty"`
}

// BotStressOrchestrationSummary 是编排展示摘要。
type BotStressOrchestrationSummary struct {
	Enabled     bool     `json:"enabled"`
	Loop        bool     `json:"loop"`
	StaggerMS   int      `json:"staggerMs"`
	PhaseCount  int      `json:"phaseCount"`
	DurationSec int      `json:"durationSec"`
	Behaviors   []string `json:"behaviors"`
}

type stressOrchestrationBehaviorConfig struct {
	Loop         bool                             `json:"loop"`
	StartDelayMS int                              `json:"startDelayMs"`
	Phases       []stressOrchestrationConfigPhase `json:"phases"`
}

type stressOrchestrationConfigPhase struct {
	DurationMS int                              `json:"durationMs"`
	Behavior   string                           `json:"behavior"`
	Target     string                           `json:"target,omitempty"`
	Config     *stressOrchestrationCustomConfig `json:"config,omitempty"`
}

type stressOrchestrationCustomConfig struct {
	Steps []stressOrchestrationConfigStep `json:"steps"`
}

type stressOrchestrationConfigStep struct {
	Type     string             `json:"type"`
	Message  string             `json:"message,omitempty"`
	Duration int                `json:"duration,omitempty"`
	Pos      map[string]float64 `json:"pos,omitempty"`
}

var stressOrchestrationBehaviors = map[string]struct{}{
	"idle":   {},
	"follow": {},
	"patrol": {},
	"guard":  {},
	"custom": {},
}

var stressOrchestrationStepTypes = map[string]struct{}{
	"move":     {},
	"chat":     {},
	"wait":     {},
	"attack":   {},
	"interact": {},
	"use_item": {},
}

// ParseStressOrchestrationYAML 解析并校验 Bot 压测 YAML 编排。
func ParseStressOrchestrationYAML(raw string) (*StressOrchestration, BotStressOrchestrationSummary, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, BotStressOrchestrationSummary{}, nil
	}

	var orchestration StressOrchestration
	if err := yaml.Unmarshal([]byte(raw), &orchestration); err != nil {
		return nil, BotStressOrchestrationSummary{}, fmt.Errorf("%w: YAML 语法错误", ErrBotStressSessionInvalid)
	}
	if err := orchestration.normalizeAndValidate(); err != nil {
		return nil, BotStressOrchestrationSummary{}, err
	}
	return &orchestration, orchestration.summary(), nil
}

// BehaviorConfigForBot 生成下发给 bot-worker 的单 Bot 编排配置。
func (o *StressOrchestration) BehaviorConfigForBot(index int) (json.RawMessage, error) {
	if o == nil {
		return nil, fmt.Errorf("%w: 编排为空", ErrBotStressSessionInvalid)
	}
	if err := o.normalizeAndValidate(); err != nil {
		return nil, err
	}

	startDelay := 0
	if index > 1 {
		startDelay = (index - 1) * o.StaggerMS
	}
	config := stressOrchestrationBehaviorConfig{
		Loop:         o.Loop,
		StartDelayMS: startDelay,
		Phases:       make([]stressOrchestrationConfigPhase, 0, len(o.Phases)),
	}
	for _, phase := range o.Phases {
		item := stressOrchestrationConfigPhase{
			DurationMS: phase.DurationSec * 1000,
			Behavior:   phase.Behavior,
			Target:     strings.TrimSpace(phase.Target),
		}
		if phase.Behavior == "custom" {
			item.Config = &stressOrchestrationCustomConfig{Steps: customStepsForBehaviorConfig(phase.Steps)}
		}
		config.Phases = append(config.Phases, item)
	}

	raw, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("序列化编排配置失败: %w", err)
	}
	return json.RawMessage(raw), nil
}

func (o *StressOrchestration) normalizeAndValidate() error {
	if o.StaggerMS < 0 {
		return fmt.Errorf("%w: staggerMs 不能为负数", ErrBotStressSessionInvalid)
	}
	if len(o.Phases) == 0 {
		return fmt.Errorf("%w: phases 不能为空", ErrBotStressSessionInvalid)
	}
	for i := range o.Phases {
		phase := &o.Phases[i]
		phase.Behavior = strings.TrimSpace(phase.Behavior)
		phase.Target = strings.TrimSpace(phase.Target)
		if phase.DurationSec <= 0 {
			return fmt.Errorf("%w: durationSec 必须大于 0", ErrBotStressSessionInvalid)
		}
		if _, ok := stressOrchestrationBehaviors[phase.Behavior]; !ok {
			return fmt.Errorf("%w: 不支持的 behavior %s", ErrBotStressSessionInvalid, phase.Behavior)
		}
		if phase.Behavior != "custom" && len(phase.Steps) > 0 {
			return fmt.Errorf("%w: 仅 custom 阶段支持 steps", ErrBotStressSessionInvalid)
		}
		if err := validateStressOrchestrationSteps(phase.Steps); err != nil {
			return err
		}
	}
	return nil
}

func validateStressOrchestrationSteps(steps []StressOrchestrationStep) error {
	for i := range steps {
		step := &steps[i]
		step.Type = strings.TrimSpace(step.Type)
		if _, ok := stressOrchestrationStepTypes[step.Type]; !ok {
			return fmt.Errorf("%w: 不支持的 custom step %s", ErrBotStressSessionInvalid, step.Type)
		}
		if step.DurationMS < 0 || step.Duration < 0 {
			return fmt.Errorf("%w: step duration 不能为负数", ErrBotStressSessionInvalid)
		}
	}
	return nil
}

func (o StressOrchestration) summary() BotStressOrchestrationSummary {
	summary := BotStressOrchestrationSummary{
		Enabled:    true,
		Loop:       o.Loop,
		StaggerMS:  o.StaggerMS,
		PhaseCount: len(o.Phases),
	}
	seen := make(map[string]bool, len(o.Phases))
	for _, phase := range o.Phases {
		summary.DurationSec += phase.DurationSec
		if !seen[phase.Behavior] {
			seen[phase.Behavior] = true
			summary.Behaviors = append(summary.Behaviors, phase.Behavior)
		}
	}
	return summary
}

func customStepsForBehaviorConfig(steps []StressOrchestrationStep) []stressOrchestrationConfigStep {
	out := make([]stressOrchestrationConfigStep, 0, len(steps))
	for _, step := range steps {
		duration := step.Duration
		if step.DurationMS > 0 {
			duration = step.DurationMS
		}
		out = append(out, stressOrchestrationConfigStep{
			Type:     step.Type,
			Message:  step.Message,
			Duration: duration,
			Pos:      step.Pos,
		})
	}
	return out
}
