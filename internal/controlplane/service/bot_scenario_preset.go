package service

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const TowerDefenseCorePresetKey = "tower-defense-core-v1"

const (
	towerDefenseObservationDurationMS    = 3_600_000
	towerDefenseAttackIntervalMS         = 600
	towerDefenseEvidenceWindowMS         = 30_000
	towerDefenseMinDamageEventsPerWindow = 1
)

// TowerDefenseCorePresetParams 只承载用户环境相关的业务值，不提供命令、坐标或怪物默认值。
type TowerDefenseCorePresetParams struct {
	Seed           *int64
	RoomKey        string
	JoinCommand    string
	LobbyCenter    *ScenarioPosition
	LobbyRadius    *float64
	CombatPosition *ScenarioPosition
	CombatRadius   *float64
	CombatAreaID   string
	MonsterTypes   []string
	AttackRadius   *float64
}

// BuildScenarioPreset 生成内置 Scenario V2 纯数据预设。
func BuildScenarioPreset(key string, params TowerDefenseCorePresetParams) (*ScenarioV2, error) {
	if key != TowerDefenseCorePresetKey {
		return nil, scenarioValidationError("presetKey", "未知场景预设")
	}
	if err := validateTowerDefensePresetParams(params); err != nil {
		return nil, err
	}
	params.JoinCommand = strings.ReplaceAll(params.JoinCommand, "{{roomKey}}", params.RoomKey)
	scenario := &ScenarioV2{
		Version: 2, Seed: *params.Seed, seedPresent: true,
		Cohorts: []ScenarioCohort{
			{Key: "lobby", Percent: 20, Steps: towerDefenseLobbySteps(params)},
			{Key: "combat", Percent: 80, Steps: towerDefenseCombatSteps(params)},
		},
	}
	if err := validateScenarioV2(scenario, false); err != nil {
		return nil, err
	}
	return scenario, nil
}

func towerDefenseLobbySteps(params TowerDefenseCorePresetParams) []ScenarioAction {
	return []ScenarioAction{
		{WaitSpawn: &WaitSpawnAction{ScenarioActionBase: newScenarioActionBase("lobby-spawn", ScenarioActionWaitSpawn)}},
		{RoamInArea: &RoamInAreaAction{
			ScenarioActionBase: observedScenarioActionBase("lobby-roam", ScenarioActionRoamInArea),
			DurationMS:         towerDefenseObservationDurationMS,
			Area:               ScenarioArea{Type: "radius", Center: *params.LobbyCenter, Radius: *params.LobbyRadius},
			PauseMS:            ScenarioIntRange{Min: 500, Max: 3000}, MaxPathFailures: 3,
		}},
	}
}

func towerDefenseCombatSteps(params TowerDefenseCorePresetParams) []ScenarioAction {
	return []ScenarioAction{
		{WaitSpawn: &WaitSpawnAction{ScenarioActionBase: newScenarioActionBase("combat-spawn", ScenarioActionWaitSpawn)}},
		{SendCommand: &SendCommandAction{ScenarioActionBase: newScenarioActionBase("combat-join", ScenarioActionSendCommand), Command: params.JoinCommand}},
		{WaitProbeEvent: &WaitProbeEventAction{ScenarioActionBase: newScenarioActionBase("combat-room-joined", ScenarioActionWaitProbeEvent), Event: "room_joined"}},
		{Barrier: &BarrierAction{
			ScenarioActionBase: newScenarioActionBase("combat-ready", ScenarioActionBarrier), Key: "combat-ready",
			Release: ScenarioBarrierRelease{Type: "percent", Value: 99}, TimeoutPolicy: "fail",
		}},
		{WaitProbeEvent: &WaitProbeEventAction{ScenarioActionBase: newScenarioActionBase("combat-game-started", ScenarioActionWaitProbeEvent), Event: "game_started"}},
		{MoveToAndWait: &MoveToAndWaitAction{
			ScenarioActionBase: newScenarioActionBase("combat-move", ScenarioActionMoveToAndWait),
			Pos:                *params.CombatPosition, Radius: *params.CombatRadius,
			AreaID: params.CombatAreaID, RequireProbeEvent: "area_arrived",
		}},
		{AttackUntil: &AttackUntilAction{
			ScenarioActionBase: observedScenarioActionBase("combat-attack", ScenarioActionAttackUntil),
			Selector:           ScenarioEntitySelector{Kind: "hostile", Types: append([]string(nil), params.MonsterTypes...), Radius: *params.AttackRadius, Priority: "nearest"},
			Stop: ScenarioAttackStop{
				DurationMS: towerDefenseObservationDurationMS, ProbeEvent: "damage", SuccessPolicy: "all",
				EvidenceWindowMS: towerDefenseEvidenceWindowMS, MinDamageEventsPerWindow: towerDefenseMinDamageEventsPerWindow,
			},
			AttackIntervalMS: towerDefenseAttackIntervalMS, Chase: true, Reacquire: true,
		}},
	}
}

func observedScenarioActionBase(id string, actionType ScenarioActionType) ScenarioActionBase {
	base := newScenarioActionBase(id, actionType)
	base.ObservationStep = true
	return base
}

func validTowerDefenseRoomKey(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 64 {
		return false
	}
	if strings.Contains(value, "{{") || strings.Contains(value, "}}") {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	})
}

func validateTowerDefensePresetParams(params TowerDefenseCorePresetParams) error {
	checks := []struct {
		missing bool
		path    string
		message string
	}{
		{params.Seed == nil, "params.seed", "必须填写固定随机种子"},
		{!validTowerDefenseRoomKey(params.RoomKey), "params.roomKey", "必须为 1..64 字符且不含空白、控制符或模板语法"},
		{strings.TrimSpace(params.JoinCommand) == "", "params.joinCommand", "必须填写进房命令"},
		{!strings.Contains(params.JoinCommand, "{{roomKey}}"), "params.joinCommand", "必须包含 {{roomKey}} 房间占位符"},
		{!strings.Contains(params.JoinCommand, "{{correlationToken}}"), "params.joinCommand", "必须包含 {{correlationToken}} 关联占位符"},
		{params.LobbyCenter == nil, "params.lobbyCenter", "必须填写主城坐标"},
		{params.LobbyRadius == nil, "params.lobbyRadius", "必须填写主城漫游半径"},
		{params.CombatPosition == nil, "params.combatPosition", "必须填写战斗区域坐标"},
		{params.CombatRadius == nil, "params.combatRadius", "必须填写抵达判定半径"},
		{strings.TrimSpace(params.CombatAreaID) == "", "params.combatAreaId", "必须填写战斗区域标识"},
		{len(params.MonsterTypes) == 0, "params.monsterTypes", "必须填写怪物类型"},
		{params.AttackRadius == nil, "params.attackRadius", "必须填写锁敌半径"},
	}
	for _, check := range checks {
		if check.missing {
			return scenarioValidationError(check.path, check.message)
		}
	}
	return nil
}
