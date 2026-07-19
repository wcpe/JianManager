package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildScenarioPreset_TowerDefenseCoreProducesValidScenarioV2(t *testing.T) {
	seed := int64(20260719)
	lobbyRadius, combatRadius := 30.0, 2.0
	scenario, err := BuildScenarioPreset(TowerDefenseCorePresetKey, TowerDefenseCorePresetParams{
		Seed: &seed, JoinCommand: "/tower join {{roomKey}} {{correlationToken}}",
		LobbyCenter: &ScenarioPosition{X: 10, Y: 64, Z: -5}, LobbyRadius: &lobbyRadius,
		CombatPosition: &ScenarioPosition{X: 100, Y: 65, Z: 100}, CombatRadius: &combatRadius,
		CombatAreaID: "combat-zone-a", MonsterTypes: []string{"zombie", "skeleton"}, AttackRadius: &lobbyRadius,
	})
	require.NoError(t, err)
	require.Equal(t, 2, scenario.Version)
	require.Equal(t, seed, scenario.Seed)
	require.Len(t, scenario.Cohorts, 2)
	require.Equal(t, "lobby", scenario.Cohorts[0].Key)
	require.Equal(t, 20, scenario.Cohorts[0].Percent)
	require.Equal(t, []ScenarioActionType{ScenarioActionWaitSpawn, ScenarioActionRoamInArea}, actionTypes(scenario.Cohorts[0]))
	require.True(t, scenario.Cohorts[0].Steps[1].Base().ObservationStep)
	require.Equal(t, "combat", scenario.Cohorts[1].Key)
	require.Equal(t, 80, scenario.Cohorts[1].Percent)
	require.Equal(t, []ScenarioActionType{
		ScenarioActionWaitSpawn, ScenarioActionSendCommand, ScenarioActionWaitProbeEvent,
		ScenarioActionBarrier, ScenarioActionWaitProbeEvent, ScenarioActionMoveToAndWait,
		ScenarioActionAttackUntil,
	}, actionTypes(scenario.Cohorts[1]))
	require.True(t, scenario.Cohorts[1].Steps[6].Base().ObservationStep)
	require.Equal(t, "area_arrived", scenario.Cohorts[1].Steps[5].MoveToAndWait.RequireProbeEvent)
	require.Equal(t, []string{"zombie", "skeleton"}, scenario.Cohorts[1].Steps[6].AttackUntil.Selector.Types)

	raw, err := json.Marshal(scenario)
	require.NoError(t, err)
	parsed, err := ParseScenarioV2(raw)
	require.NoError(t, err)
	require.Equal(t, scenario.Seed, parsed.Seed)
}

func TestBuildScenarioPreset_TowerDefenseCoreRequiresBusinessParametersWithPaths(t *testing.T) {
	seed := int64(1)
	radius := 2.0
	valid := TowerDefenseCorePresetParams{
		Seed: &seed, JoinCommand: "/join", LobbyCenter: &ScenarioPosition{}, LobbyRadius: &radius,
		CombatPosition: &ScenarioPosition{}, CombatRadius: &radius, CombatAreaID: "area", MonsterTypes: []string{"zombie"}, AttackRadius: &radius,
	}
	tests := []struct {
		name string
		path string
		edit func(*TowerDefenseCorePresetParams)
	}{
		{name: "seed", path: "params.seed", edit: func(p *TowerDefenseCorePresetParams) { p.Seed = nil }},
		{name: "命令", path: "params.joinCommand", edit: func(p *TowerDefenseCorePresetParams) { p.JoinCommand = "" }},
		{name: "主城坐标", path: "params.lobbyCenter", edit: func(p *TowerDefenseCorePresetParams) { p.LobbyCenter = nil }},
		{name: "主城半径", path: "params.lobbyRadius", edit: func(p *TowerDefenseCorePresetParams) { p.LobbyRadius = nil }},
		{name: "战斗坐标", path: "params.combatPosition", edit: func(p *TowerDefenseCorePresetParams) { p.CombatPosition = nil }},
		{name: "战斗半径", path: "params.combatRadius", edit: func(p *TowerDefenseCorePresetParams) { p.CombatRadius = nil }},
		{name: "战斗区域", path: "params.combatAreaId", edit: func(p *TowerDefenseCorePresetParams) { p.CombatAreaID = "" }},
		{name: "怪物类型", path: "params.monsterTypes", edit: func(p *TowerDefenseCorePresetParams) { p.MonsterTypes = nil }},
		{name: "攻击半径", path: "params.attackRadius", edit: func(p *TowerDefenseCorePresetParams) { p.AttackRadius = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := valid
			test.edit(&params)
			_, err := BuildScenarioPreset(TowerDefenseCorePresetKey, params)
			var validationErr *ScenarioValidationError
			require.ErrorAs(t, err, &validationErr)
			require.Equal(t, test.path, validationErr.Path)
		})
	}
}

func actionTypes(cohort ScenarioCohort) []ScenarioActionType {
	types := make([]ScenarioActionType, 0, len(cohort.Steps))
	for _, action := range cohort.Steps {
		types = append(types, action.Type())
	}
	return types
}
