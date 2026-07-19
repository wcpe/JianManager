package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseScenarioV2_JSONAndYAMLReturnSameValidationPath(t *testing.T) {
	inputs := []string{
		`{"version":2,"seed":7,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"observe","type":"wait","observationStep":true,"durationMs":1000,"timeoutMs":99}]}]}`,
		"version: 2\nseed: 7\ncohorts:\n  - key: all\n    percent: 100\n    steps:\n      - id: observe\n        type: wait\n        observationStep: true\n        durationMs: 1000\n        timeoutMs: 99\n",
	}
	for _, input := range inputs {
		_, err := ParseScenarioV2([]byte(input))
		var validationErr *ScenarioValidationError
		require.ErrorAs(t, err, &validationErr)
		require.Equal(t, "cohorts[0].steps[0].timeoutMs", validationErr.Path)
		require.Equal(t, "必须在 100..3600000 之间", validationErr.Message)
	}
}

func TestParseScenarioV2_RejectsUnknownTemplateVariable(t *testing.T) {
	raw := `{
		"version":2,
		"seed":7,
		"cohorts":[{"key":"all","percent":100,"steps":[
			{"id":"send","type":"send_command","command":"/join {{unknown}}"},
			{"id":"observe","type":"wait","observationStep":true,"durationMs":1000}
		]}]
	}`

	_, err := ParseScenarioV2([]byte(raw))
	var validationErr *ScenarioValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "cohorts[0].steps[0].command", validationErr.Path)
	require.Equal(t, "包含未知模板变量 unknown", validationErr.Message)
}

func TestParseScenarioV2_RejectsNonFiniteCoordinates(t *testing.T) {
	inputs := []struct {
		name string
		raw  string
	}{
		{
			name: "JSON 溢出",
			raw:  `{"version":2,"seed":7,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"move","type":"move_to_and_wait","observationStep":true,"pos":{"x":1e400,"y":64,"z":0},"radius":2}]}]}`,
		},
		{
			name: "YAML NaN",
			raw:  "version: 2\nseed: 7\ncohorts:\n  - key: all\n    percent: 100\n    steps:\n      - id: move\n        type: move_to_and_wait\n        observationStep: true\n        pos: {x: .nan, y: 64, z: 0}\n        radius: 2\n",
		},
		{
			name: "YAML Inf",
			raw:  "version: 2\nseed: 7\ncohorts:\n  - key: all\n    percent: 100\n    steps:\n      - id: move\n        type: move_to_and_wait\n        observationStep: true\n        pos: {x: .inf, y: 64, z: 0}\n        radius: 2\n",
		},
	}
	for _, test := range inputs {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseScenarioV2([]byte(test.raw))
			var validationErr *ScenarioValidationError
			require.ErrorAs(t, err, &validationErr)
			require.Equal(t, "cohorts[0].steps[0].pos.x", validationErr.Path)
			require.Equal(t, "必须是有限数值", validationErr.Message)
		})
	}
}

func TestParseScenarioV2_RequiresTrustedAttackConditionAndCompleteEvidenceWindow(t *testing.T) {
	tests := []struct {
		name string
		stop string
		path string
	}{
		{
			name: "仅时长不算可信成功条件",
			stop: `{"durationMs":30000,"successPolicy":"all"}`,
			path: "cohorts[0].steps[0].stop",
		},
		{
			name: "时长必须覆盖完整证据窗",
			stop: `{"durationMs":29999,"successPolicy":"all","evidenceWindowMs":30000,"minDamageEventsPerWindow":1}`,
			path: "cohorts[0].steps[0].stop.durationMs",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := `{"version":2,"seed":7,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"attack","type":"attack_until","observationStep":true,"selector":{"kind":"hostile","types":["zombie"],"radius":16},"stop":` + test.stop + `,"attackIntervalMs":600,"chase":true}]}]}`
			_, err := ParseScenarioV2([]byte(raw))
			var validationErr *ScenarioValidationError
			require.ErrorAs(t, err, &validationErr)
			require.Equal(t, test.path, validationErr.Path)
		})
	}
}

func TestParseScenarioV2_RejectsRespawnReferenceCycle(t *testing.T) {
	raw := `{
		"version":2,
		"seed":7,
		"cohorts":[{"key":"all","percent":100,"steps":[
			{"id":"observe","type":"wait","observationStep":true,"durationMs":1000},
			{"id":"respawn-a","type":"respawn_and_rejoin","entryStepId":"respawn-b"},
			{"id":"respawn-b","type":"respawn_and_rejoin","entryStepId":"respawn-a"}
		]}]
	}`

	_, err := ParseScenarioV2([]byte(raw))
	var validationErr *ScenarioValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "cohorts[0].steps[2].entryStepId", validationErr.Path)
	require.Equal(t, "respawn 引用形成无界递归", validationErr.Message)
}

func TestConvertStressOrchestrationToScenarioV2_MapsStructuredAndLegacyActions(t *testing.T) {
	raw := `
loop: false
phases:
  - durationSec: 10
    behavior: idle
  - durationSec: 10
    behavior: follow
    target: Steve
  - durationSec: 20
    behavior: custom
    steps:
      - type: chat
        message: hello
      - type: wait
        durationMs: 1000
      - type: move
        pos: {x: 1, y: 64, z: 2}
      - type: attack
      - type: interact
`

	scenario, err := ConvertStressOrchestrationToScenarioV2(raw, 99)
	require.NoError(t, err)
	require.Equal(t, int64(99), scenario.Seed)
	require.Len(t, scenario.Cohorts, 1)
	require.Equal(t, "legacy", scenario.Cohorts[0].Key)
	require.Equal(t, 100, scenario.Cohorts[0].Percent)

	types := make([]ScenarioActionType, 0, len(scenario.Cohorts[0].Steps))
	for _, action := range scenario.Cohorts[0].Steps {
		types = append(types, action.Type())
	}
	require.Equal(t, []ScenarioActionType{
		ScenarioActionWait,
		ScenarioActionLegacyBehavior,
		ScenarioActionSendCommand,
		ScenarioActionWait,
		ScenarioActionMoveToAndWait,
		ScenarioActionAttackUntil,
		ScenarioActionLegacyBehavior,
	}, types)

	snapshot, err := json.Marshal(scenario)
	require.NoError(t, err)
	require.Contains(t, string(snapshot), `"command":"hello"`)
	require.Contains(t, string(snapshot), `"behavior":"follow"`)
}

func TestAssignScenarioCohorts_500BotsExactAndReproducible(t *testing.T) {
	cohorts := []ScenarioCohort{
		{Key: "lobby", Percent: 20},
		{Key: "combat", Percent: 80},
	}

	first, err := AssignScenarioCohorts(20260719, 500, cohorts)
	require.NoError(t, err)
	second, err := AssignScenarioCohorts(20260719, 500, cohorts)
	require.NoError(t, err)
	other, err := AssignScenarioCohorts(20260720, 500, cohorts)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.NotEqual(t, first, other)
	require.Len(t, first, 500)

	counts := map[string]int{}
	for _, key := range first {
		counts[key]++
	}
	require.Equal(t, 100, counts["lobby"])
	require.Equal(t, 400, counts["combat"])
}

func TestAssignScenarioCohorts_DistributesRemainderByDeclarationOrder(t *testing.T) {
	cohorts := []ScenarioCohort{
		{Key: "first", Percent: 34},
		{Key: "second", Percent: 33},
		{Key: "third", Percent: 33},
	}

	assigned, err := AssignScenarioCohorts(1, 2, cohorts)
	require.NoError(t, err)
	counts := map[string]int{}
	for _, key := range assigned {
		counts[key]++
	}
	require.Equal(t, 1, counts["first"])
	require.Equal(t, 1, counts["second"])
	require.Zero(t, counts["third"])
}
