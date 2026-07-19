package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ScenarioActionType 是 Scenario V2 动作类型。
type ScenarioActionType string

const (
	ScenarioActionWaitSpawn        ScenarioActionType = "wait_spawn"
	ScenarioActionRoamInArea       ScenarioActionType = "roam_in_area"
	ScenarioActionSendCommand      ScenarioActionType = "send_command"
	ScenarioActionWaitProbeEvent   ScenarioActionType = "wait_probe_event"
	ScenarioActionBarrier          ScenarioActionType = "barrier"
	ScenarioActionMoveToAndWait    ScenarioActionType = "move_to_and_wait"
	ScenarioActionFindEntity       ScenarioActionType = "find_entity"
	ScenarioActionAttackUntil      ScenarioActionType = "attack_until"
	ScenarioActionWait             ScenarioActionType = "wait"
	ScenarioActionRespawnAndRejoin ScenarioActionType = "respawn_and_rejoin"

	// ScenarioActionLegacyBehavior 只允许 V1 转换后的内部 snapshot 使用。
	ScenarioActionLegacyBehavior ScenarioActionType = "legacy_behavior"
)

// ScenarioV2 是 Control Plane 内部规范场景。
type ScenarioV2 struct {
	Version int              `json:"version" yaml:"version"`
	Seed    int64            `json:"seed" yaml:"seed"`
	Cohorts []ScenarioCohort `json:"cohorts" yaml:"cohorts"`

	seedPresent bool
}

// ScenarioCohort 是按比例分配的场景分组。
type ScenarioCohort struct {
	Key     string           `json:"key" yaml:"key"`
	Percent int              `json:"percent" yaml:"percent"`
	Steps   []ScenarioAction `json:"steps" yaml:"steps"`
}

// ScenarioActionBase 是所有动作共享的稳定字段。
type ScenarioActionBase struct {
	ID              string             `json:"id" yaml:"id"`
	ActionType      ScenarioActionType `json:"type" yaml:"type"`
	ObservationStep bool               `json:"observationStep,omitempty" yaml:"observationStep,omitempty"`
	TimeoutMS       *int               `json:"timeoutMs,omitempty" yaml:"timeoutMs,omitempty"`
	MaxAttempts     *int               `json:"maxAttempts,omitempty" yaml:"maxAttempts,omitempty"`
	RetryBackoffMS  *int               `json:"retryBackoffMs,omitempty" yaml:"retryBackoffMs,omitempty"`
	ResumePolicy    string             `json:"resumePolicy,omitempty" yaml:"resumePolicy,omitempty"`
}

// ScenarioPosition 是有限三维坐标。
type ScenarioPosition struct {
	X float64 `json:"x" yaml:"x"`
	Y float64 `json:"y" yaml:"y"`
	Z float64 `json:"z" yaml:"z"`
}

// ScenarioIntRange 是确定性暂停区间。
type ScenarioIntRange struct {
	Min int `json:"min" yaml:"min"`
	Max int `json:"max" yaml:"max"`
}

// ScenarioArea 是半径或航点漫游区域。
type ScenarioArea struct {
	Type      string             `json:"type" yaml:"type"`
	Center    ScenarioPosition   `json:"center,omitempty" yaml:"center,omitempty"`
	Radius    float64            `json:"radius,omitempty" yaml:"radius,omitempty"`
	Waypoints []ScenarioPosition `json:"waypoints,omitempty" yaml:"waypoints,omitempty"`
}

// ScenarioEntitySelector 是实体筛选器。
type ScenarioEntitySelector struct {
	Kind      string   `json:"kind,omitempty" yaml:"kind,omitempty"`
	Types     []string `json:"types,omitempty" yaml:"types,omitempty"`
	NameRegex string   `json:"nameRegex,omitempty" yaml:"nameRegex,omitempty"`
	Radius    float64  `json:"radius" yaml:"radius"`
	Priority  string   `json:"priority,omitempty" yaml:"priority,omitempty"`
}

// ScenarioAttackStop 是可信攻击停止条件。
type ScenarioAttackStop struct {
	DurationMS               int    `json:"durationMs" yaml:"durationMs"`
	DamageAtLeast            int    `json:"damageAtLeast,omitempty" yaml:"damageAtLeast,omitempty"`
	KillsAtLeast             int    `json:"killsAtLeast,omitempty" yaml:"killsAtLeast,omitempty"`
	ProbeEvent               string `json:"probeEvent,omitempty" yaml:"probeEvent,omitempty"`
	EvidenceWindowMS         int    `json:"evidenceWindowMs,omitempty" yaml:"evidenceWindowMs,omitempty"`
	MinDamageEventsPerWindow int    `json:"minDamageEventsPerWindow,omitempty" yaml:"minDamageEventsPerWindow,omitempty"`
	SuccessPolicy            string `json:"successPolicy,omitempty" yaml:"successPolicy,omitempty"`
}

// ScenarioBarrierRelease 是屏障释放阈值。
type ScenarioBarrierRelease struct {
	Type  string `json:"type" yaml:"type"`
	Value int    `json:"value,omitempty" yaml:"value,omitempty"`
}

// WaitSpawnAction 等待 Bot spawn。
type WaitSpawnAction struct {
	ScenarioActionBase `yaml:",inline"`
}

// RoamInAreaAction 在声明区域内确定性漫游。
type RoamInAreaAction struct {
	ScenarioActionBase `yaml:",inline"`
	DurationMS         int              `json:"durationMs" yaml:"durationMs"`
	Area               ScenarioArea     `json:"area" yaml:"area"`
	PauseMS            ScenarioIntRange `json:"pauseMs,omitempty" yaml:"pauseMs,omitempty"`
	MaxPathFailures    int              `json:"maxPathFailures,omitempty" yaml:"maxPathFailures,omitempty"`
}

// SendCommandAction 发送聊天或命令。
type SendCommandAction struct {
	ScenarioActionBase `yaml:",inline"`
	Command            string `json:"command" yaml:"command"`
}

// WaitProbeEventAction 等待可信探针事件。
type WaitProbeEventAction struct {
	ScenarioActionBase `yaml:",inline"`
	Event              string `json:"event" yaml:"event"`
}

// BarrierAction 等待 Control Plane 统一释放屏障。
type BarrierAction struct {
	ScenarioActionBase `yaml:",inline"`
	Key                string                 `json:"key" yaml:"key"`
	Release            ScenarioBarrierRelease `json:"release" yaml:"release"`
	TimeoutPolicy      string                 `json:"timeoutPolicy,omitempty" yaml:"timeoutPolicy,omitempty"`
}

// MoveToAndWaitAction 移动并确认稳定抵达。
type MoveToAndWaitAction struct {
	ScenarioActionBase `yaml:",inline"`
	Pos                ScenarioPosition `json:"pos" yaml:"pos"`
	Radius             float64          `json:"radius" yaml:"radius"`
	AreaID             string           `json:"areaId,omitempty" yaml:"areaId,omitempty"`
	RequireProbeEvent  string           `json:"requireProbeEvent,omitempty" yaml:"requireProbeEvent,omitempty"`
}

// FindEntityAction 查找并锁定实体。
type FindEntityAction struct {
	ScenarioActionBase `yaml:",inline"`
	Selector           ScenarioEntitySelector `json:"selector" yaml:"selector"`
}

// AttackUntilAction 按可信条件持续攻击。
type AttackUntilAction struct {
	ScenarioActionBase      `yaml:",inline"`
	Selector                ScenarioEntitySelector `json:"selector" yaml:"selector"`
	Stop                    ScenarioAttackStop     `json:"stop" yaml:"stop"`
	AttackIntervalMS        int                    `json:"attackIntervalMs" yaml:"attackIntervalMs"`
	Chase                   bool                   `json:"chase,omitempty" yaml:"chase,omitempty"`
	Reacquire               bool                   `json:"reacquire,omitempty" yaml:"reacquire,omitempty"`
	TargetNotFoundTimeoutMS int                    `json:"targetNotFoundTimeoutMs,omitempty" yaml:"targetNotFoundTimeoutMs,omitempty"`
	LegacyDurationSuccess   bool                   `json:"legacyDurationSuccess,omitempty" yaml:"legacyDurationSuccess,omitempty"`
}

// WaitAction 等待固定时长。
type WaitAction struct {
	ScenarioActionBase `yaml:",inline"`
	DurationMS         int `json:"durationMs" yaml:"durationMs"`
}

// RespawnAndRejoinAction 重生后跳回声明入口。
type RespawnAndRejoinAction struct {
	ScenarioActionBase `yaml:",inline"`
	EntryStepID        string `json:"entryStepId" yaml:"entryStepId"`
}

// LegacyBehaviorAction 是 V1 转换的内部兼容动作，不允许新建 V2 场景直接提交。
type LegacyBehaviorAction struct {
	ScenarioActionBase `yaml:",inline"`
	Behavior           string                   `json:"behavior" yaml:"behavior"`
	Target             string                   `json:"target,omitempty" yaml:"target,omitempty"`
	DurationMS         int                      `json:"durationMs" yaml:"durationMs"`
	Step               *StressOrchestrationStep `json:"step,omitempty" yaml:"step,omitempty"`
}

// ScenarioAction 是显式动作联合类型。
type ScenarioAction struct {
	WaitSpawn        *WaitSpawnAction
	RoamInArea       *RoamInAreaAction
	SendCommand      *SendCommandAction
	WaitProbeEvent   *WaitProbeEventAction
	Barrier          *BarrierAction
	MoveToAndWait    *MoveToAndWaitAction
	FindEntity       *FindEntityAction
	AttackUntil      *AttackUntilAction
	Wait             *WaitAction
	RespawnAndRejoin *RespawnAndRejoinAction
	LegacyBehavior   *LegacyBehaviorAction
	Unknown          *ScenarioActionBase
}

// Type 返回动作判别类型。
func (a ScenarioAction) Type() ScenarioActionType {
	base := a.Base()
	if base == nil {
		return ""
	}
	return base.ActionType
}

// Base 返回动作公共字段。
func (a ScenarioAction) Base() *ScenarioActionBase {
	switch {
	case a.WaitSpawn != nil:
		return &a.WaitSpawn.ScenarioActionBase
	case a.RoamInArea != nil:
		return &a.RoamInArea.ScenarioActionBase
	case a.SendCommand != nil:
		return &a.SendCommand.ScenarioActionBase
	case a.WaitProbeEvent != nil:
		return &a.WaitProbeEvent.ScenarioActionBase
	case a.Barrier != nil:
		return &a.Barrier.ScenarioActionBase
	case a.MoveToAndWait != nil:
		return &a.MoveToAndWait.ScenarioActionBase
	case a.FindEntity != nil:
		return &a.FindEntity.ScenarioActionBase
	case a.AttackUntil != nil:
		return &a.AttackUntil.ScenarioActionBase
	case a.Wait != nil:
		return &a.Wait.ScenarioActionBase
	case a.RespawnAndRejoin != nil:
		return &a.RespawnAndRejoin.ScenarioActionBase
	case a.LegacyBehavior != nil:
		return &a.LegacyBehavior.ScenarioActionBase
	default:
		return a.Unknown
	}
}

// MarshalJSON 把显式联合类型还原为契约中的扁平动作对象。
func (a ScenarioAction) MarshalJSON() ([]byte, error) {
	switch {
	case a.WaitSpawn != nil:
		return json.Marshal(a.WaitSpawn)
	case a.RoamInArea != nil:
		return json.Marshal(a.RoamInArea)
	case a.SendCommand != nil:
		return json.Marshal(a.SendCommand)
	case a.WaitProbeEvent != nil:
		return json.Marshal(a.WaitProbeEvent)
	case a.Barrier != nil:
		return json.Marshal(a.Barrier)
	case a.MoveToAndWait != nil:
		return json.Marshal(a.MoveToAndWait)
	case a.FindEntity != nil:
		return json.Marshal(a.FindEntity)
	case a.AttackUntil != nil:
		return json.Marshal(a.AttackUntil)
	case a.Wait != nil:
		return json.Marshal(a.Wait)
	case a.RespawnAndRejoin != nil:
		return json.Marshal(a.RespawnAndRejoin)
	case a.LegacyBehavior != nil:
		return json.Marshal(a.LegacyBehavior)
	case a.Unknown != nil:
		return json.Marshal(a.Unknown)
	default:
		return nil, errors.New("场景动作为空")
	}
}

// ScenarioValidationError 是稳定的 path/message 场景校验错误。
type ScenarioValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *ScenarioValidationError) Error() string {
	if e == nil {
		return "场景校验失败"
	}
	return fmt.Sprintf("场景校验失败: %s: %s", e.Path, e.Message)
}

func scenarioValidationError(path, message string) error {
	return &ScenarioValidationError{Path: path, Message: message}
}

type scenarioYAMLWire struct {
	Version int                  `yaml:"version"`
	Seed    *int64               `yaml:"seed"`
	Cohorts []scenarioYAMLCohort `yaml:"cohorts"`
}

type scenarioYAMLCohort struct {
	Key     string      `yaml:"key"`
	Percent int         `yaml:"percent"`
	Steps   []yaml.Node `yaml:"steps"`
}

// ParseScenarioV2 解析 JSON 或 YAML，并按新建 V2 契约校验。
func ParseScenarioV2(raw []byte) (*ScenarioV2, error) {
	return parseScenarioV2(raw, false)
}

// ParseScenarioSnapshot 解析数据库中的规范 JSON，允许 V1 转换产生的内部兼容动作。
func ParseScenarioSnapshot(raw string) (*ScenarioV2, error) {
	return parseScenarioV2([]byte(raw), true)
}

func parseScenarioV2(raw []byte, allowLegacy bool) (*ScenarioV2, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, scenarioValidationError("$", "场景不能为空")
	}
	scenario, err := decodeScenarioDocument(raw, allowLegacy)
	if err != nil {
		return nil, err
	}
	if err := validateScenarioV2(scenario, allowLegacy); err != nil {
		return nil, err
	}
	return scenario, nil
}

func decodeScenarioYAMLAction(node *yaml.Node, path string) (ScenarioAction, error) {
	var discriminator struct {
		Type ScenarioActionType `yaml:"type"`
	}
	if err := node.Decode(&discriminator); err != nil {
		return ScenarioAction{}, scenarioValidationError(path+".type", "字段类型无效")
	}
	action, target := newScenarioAction(discriminator.Type)
	if err := node.Decode(target); err != nil {
		return ScenarioAction{}, scenarioValidationError(path, "字段类型无效")
	}
	return action, nil
}

func newScenarioAction(actionType ScenarioActionType) (ScenarioAction, any) {
	switch actionType {
	case ScenarioActionWaitSpawn:
		value := &WaitSpawnAction{}
		return ScenarioAction{WaitSpawn: value}, value
	case ScenarioActionRoamInArea:
		value := &RoamInAreaAction{}
		return ScenarioAction{RoamInArea: value}, value
	case ScenarioActionSendCommand:
		value := &SendCommandAction{}
		return ScenarioAction{SendCommand: value}, value
	case ScenarioActionWaitProbeEvent:
		value := &WaitProbeEventAction{}
		return ScenarioAction{WaitProbeEvent: value}, value
	case ScenarioActionBarrier:
		value := &BarrierAction{}
		return ScenarioAction{Barrier: value}, value
	case ScenarioActionMoveToAndWait:
		value := &MoveToAndWaitAction{}
		return ScenarioAction{MoveToAndWait: value}, value
	case ScenarioActionFindEntity:
		value := &FindEntityAction{}
		return ScenarioAction{FindEntity: value}, value
	case ScenarioActionAttackUntil:
		value := &AttackUntilAction{}
		return ScenarioAction{AttackUntil: value}, value
	case ScenarioActionWait:
		value := &WaitAction{}
		return ScenarioAction{Wait: value}, value
	case ScenarioActionRespawnAndRejoin:
		value := &RespawnAndRejoinAction{}
		return ScenarioAction{RespawnAndRejoin: value}, value
	case ScenarioActionLegacyBehavior:
		value := &LegacyBehaviorAction{}
		return ScenarioAction{LegacyBehavior: value}, value
	default:
		value := &ScenarioActionBase{}
		return ScenarioAction{Unknown: value}, value
	}
}
