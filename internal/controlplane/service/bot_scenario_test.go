package service

import (
	"encoding/json"
	"strings"
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

func TestParseScenarioV2_RejectsUnboundRoomKeyWithSameJSONAndYAMLPath(t *testing.T) {
	inputs := []string{
		`{"version":2,"seed":7,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"send","type":"send_command","command":"/join {{roomKey}} {{correlationToken}}"},{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}]}]}`,
		"version: 2\nseed: 7\ncohorts:\n  - key: all\n    percent: 100\n    steps:\n      - id: send\n        type: send_command\n        command: /join {{roomKey}} {{correlationToken}}\n      - id: observe\n        type: roam_in_area\n        observationStep: true\n        durationMs: 1000\n        area:\n          type: radius\n          center: {x: 0, y: 64, z: 0}\n          radius: 2\n",
	}
	for _, input := range inputs {
		_, err := ParseScenarioV2([]byte(input))
		var validationErr *ScenarioValidationError
		require.ErrorAs(t, err, &validationErr)
		require.Equal(t, "cohorts[0].steps[0].command", validationErr.Path)
		require.Equal(t, "模板变量 roomKey 未绑定", validationErr.Message)
	}
}

func TestParseScenarioV2_AllowsBoundRuntimeCorrelationToken(t *testing.T) {
	raw := `{"version":2,"seed":7,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"send","type":"send_command","command":"/join {{correlationToken}}"},{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}]}]}`
	_, err := ParseScenarioV2([]byte(raw))
	require.NoError(t, err)
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
			raw:  `{"version":2,"seed":7,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"move","type":"move_to_and_wait","pos":{"x":1e400,"y":64,"z":0},"radius":2}]}]}`,
		},
		{
			name: "YAML NaN",
			raw:  "version: 2\nseed: 7\ncohorts:\n  - key: all\n    percent: 100\n    steps:\n      - id: move\n        type: move_to_and_wait\n        pos: {x: .nan, y: 64, z: 0}\n        radius: 2\n",
		},
		{
			name: "YAML Inf",
			raw:  "version: 2\nseed: 7\ncohorts:\n  - key: all\n    percent: 100\n    steps:\n      - id: move\n        type: move_to_and_wait\n        pos: {x: .inf, y: 64, z: 0}\n        radius: 2\n",
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
			{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}},
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
  - durationSec: 10
    behavior: guard
    target: 1,64,2
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
		ScenarioActionLegacyBehavior,
		ScenarioActionSendCommand,
		ScenarioActionWait,
		ScenarioActionMoveToAndWait,
		ScenarioActionAttackUntil,
		ScenarioActionLegacyBehavior,
	}, types)
	require.Equal(t, "guard", scenario.Cohorts[0].Steps[2].LegacyBehavior.Behavior)
	require.Equal(t, "1,64,2", scenario.Cohorts[0].Steps[2].LegacyBehavior.Target)
	require.True(t, scenario.Cohorts[0].Steps[6].AttackUntil.LegacyDurationSuccess)
	require.True(t, scenario.Cohorts[0].Steps[6].Base().ObservationStep)
	require.False(t, scenario.Cohorts[0].Steps[7].Base().ObservationStep)

	cohorts, err := ScenarioCohortJSONMap(scenario)
	require.NoError(t, err)
	require.Contains(t, cohorts["legacy"], `"legacyDurationSuccess":true`)

	snapshot, err := json.Marshal(scenario)
	require.NoError(t, err)
	require.Contains(t, string(snapshot), `"command":"hello"`)
	require.Contains(t, string(snapshot), `"behavior":"follow"`)
}

func TestConvertStressOrchestrationToScenarioV2_LegacyContinuousBehaviorsAreObservable(t *testing.T) {
	for _, behavior := range []string{"follow", "patrol", "guard"} {
		t.Run(behavior, func(t *testing.T) {
			raw := "phases:\n  - durationSec: 10\n    behavior: " + behavior + "\n    target: 0,64,0\n"
			scenario, err := ConvertStressOrchestrationToScenarioV2(raw, 7)
			require.NoError(t, err)
			require.True(t, scenario.Cohorts[0].Steps[0].Base().ObservationStep)
			require.Equal(t, behavior, scenario.Cohorts[0].Steps[0].LegacyBehavior.Behavior)
			require.Equal(t, "0,64,0", scenario.Cohorts[0].Steps[0].LegacyBehavior.Target)
		})
	}

	scenario, err := ConvertLegacyBehaviorToScenarioV2("roam", 7)
	require.NoError(t, err)
	require.True(t, scenario.Cohorts[0].Steps[0].Base().ObservationStep)
	require.Equal(t, "roam", scenario.Cohorts[0].Steps[0].LegacyBehavior.Behavior)
}

func TestParseScenarioV2_LegacyAttackDurationSuccessIsInternalOnly(t *testing.T) {
	raw := scenarioJSONWithStep(`{"id":"attack","type":"attack_until","observationStep":true,"legacyDurationSuccess":true,"selector":{"kind":"hostile","radius":16},"stop":{"durationMs":1000},"attackIntervalMs":1000}`)
	_, err := ParseScenarioV2([]byte(raw))
	var validationErr *ScenarioValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "cohorts[0].steps[0].legacyDurationSuccess", validationErr.Path)
	require.Equal(t, "未知字段", validationErr.Message)

	_, err = ParseScenarioSnapshot(raw)
	require.NoError(t, err)
}

func TestParseScenarioV2_RejectsUnknownFieldsWithSameJSONAndYAMLPath(t *testing.T) {
	tests := []struct {
		name string
		json string
		yaml string
		path string
	}{
		{name: "顶层", json: `{"version":2,"seed":7,"cohorts":[],"extra":true}`, yaml: "version: 2\nseed: 7\ncohorts: []\nextra: true\n", path: "extra"},
		{name: "cohort", json: `{"version":2,"seed":7,"cohorts":[{"key":"all","percent":100,"steps":[],"extra":true}]}`, yaml: "version: 2\nseed: 7\ncohorts:\n  - key: all\n    percent: 100\n    steps: []\n    extra: true\n", path: "cohorts[0].extra"},
		{name: "wait_spawn 动作", json: scenarioJSONWithStep(`{"id":"step","type":"wait_spawn","extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: wait_spawn\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "roam_in_area 动作", json: scenarioJSONWithStep(`{"id":"step","type":"roam_in_area","durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2},"extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: roam_in_area\ndurationMs: 1000\narea:\n  type: radius\n  center: {x: 0, y: 64, z: 0}\n  radius: 2\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "send_command 动作", json: scenarioJSONWithStep(`{"id":"step","type":"send_command","command":"hi","extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: send_command\ncommand: hi\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "wait_probe_event 动作", json: scenarioJSONWithStep(`{"id":"step","type":"wait_probe_event","event":"ready","extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: wait_probe_event\nevent: ready\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "barrier 动作", json: scenarioJSONWithStep(`{"id":"step","type":"barrier","key":"ready","release":{"type":"all"},"extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: barrier\nkey: ready\nrelease: {type: all}\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "move_to_and_wait 动作", json: scenarioJSONWithStep(`{"id":"step","type":"move_to_and_wait","pos":{"x":0,"y":64,"z":0},"radius":2,"extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: move_to_and_wait\npos: {x: 0, y: 64, z: 0}\nradius: 2\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "find_entity 动作", json: scenarioJSONWithStep(`{"id":"step","type":"find_entity","selector":{"radius":2},"extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: find_entity\nselector: {radius: 2}\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "attack_until 动作", json: scenarioJSONWithStep(`{"id":"step","type":"attack_until","selector":{"radius":2},"stop":{"durationMs":1000,"damageAtLeast":1},"attackIntervalMs":600,"extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: attack_until\nselector: {radius: 2}\nstop: {durationMs: 1000, damageAtLeast: 1}\nattackIntervalMs: 600\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "wait 动作", json: scenarioJSONWithStep(`{"id":"step","type":"wait","durationMs":1000,"extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: wait\ndurationMs: 1000\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "respawn_and_rejoin 动作", json: scenarioJSONWithStep(`{"id":"step","type":"respawn_and_rejoin","entryStepId":"step","extra":true}`), yaml: scenarioYAMLWithStep("id: step\ntype: respawn_and_rejoin\nentryStepId: step\nextra: true"), path: "cohorts[0].steps[0].extra"},
		{name: "area", json: scenarioJSONWithStep(`{"id":"step","type":"roam_in_area","durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2,"extra":true}}`), yaml: scenarioYAMLWithStep("id: step\ntype: roam_in_area\ndurationMs: 1000\narea:\n  type: radius\n  center: {x: 0, y: 64, z: 0}\n  radius: 2\n  extra: true"), path: "cohorts[0].steps[0].area.extra"},
		{name: "pause", json: scenarioJSONWithStep(`{"id":"step","type":"roam_in_area","durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2},"pauseMs":{"min":0,"max":1,"extra":true}}`), yaml: scenarioYAMLWithStep("id: step\ntype: roam_in_area\ndurationMs: 1000\narea:\n  type: radius\n  center: {x: 0, y: 64, z: 0}\n  radius: 2\npauseMs: {min: 0, max: 1, extra: true}"), path: "cohorts[0].steps[0].pauseMs.extra"},
		{name: "area position", json: scenarioJSONWithStep(`{"id":"step","type":"roam_in_area","durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0,"extra":true},"radius":2}}`), yaml: scenarioYAMLWithStep("id: step\ntype: roam_in_area\ndurationMs: 1000\narea:\n  type: radius\n  center: {x: 0, y: 64, z: 0, extra: true}\n  radius: 2"), path: "cohorts[0].steps[0].area.center.extra"},
		{name: "pos", json: scenarioJSONWithStep(`{"id":"step","type":"move_to_and_wait","pos":{"x":0,"y":64,"z":0,"extra":true},"radius":2}`), yaml: scenarioYAMLWithStep("id: step\ntype: move_to_and_wait\npos: {x: 0, y: 64, z: 0, extra: true}\nradius: 2"), path: "cohorts[0].steps[0].pos.extra"},
		{name: "selector", json: scenarioJSONWithStep(`{"id":"step","type":"find_entity","selector":{"radius":2,"extra":true}}`), yaml: scenarioYAMLWithStep("id: step\ntype: find_entity\nselector: {radius: 2, extra: true}"), path: "cohorts[0].steps[0].selector.extra"},
		{name: "stop", json: scenarioJSONWithStep(`{"id":"step","type":"attack_until","selector":{"radius":2},"stop":{"durationMs":1000,"damageAtLeast":1,"extra":true},"attackIntervalMs":600}`), yaml: scenarioYAMLWithStep("id: step\ntype: attack_until\nselector: {radius: 2}\nstop: {durationMs: 1000, damageAtLeast: 1, extra: true}\nattackIntervalMs: 600"), path: "cohorts[0].steps[0].stop.extra"},
		{name: "release", json: scenarioJSONWithStep(`{"id":"step","type":"barrier","key":"ready","release":{"type":"all","extra":true}}`), yaml: scenarioYAMLWithStep("id: step\ntype: barrier\nkey: ready\nrelease: {type: all, extra: true}"), path: "cohorts[0].steps[0].release.extra"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, source := range []struct {
				name string
				raw  string
			}{{name: "JSON", raw: test.json}, {name: "YAML", raw: test.yaml}} {
				t.Run(source.name, func(t *testing.T) {
					_, err := ParseScenarioV2([]byte(source.raw))
					var validationErr *ScenarioValidationError
					require.ErrorAs(t, err, &validationErr)
					require.Equal(t, test.path, validationErr.Path)
					require.Equal(t, "未知字段", validationErr.Message)
				})
			}
		})
	}
}

func TestParseScenarioV2_TypeErrorsUseStableLeafPaths(t *testing.T) {
	tests := []struct {
		name string
		json string
		yaml string
		path string
	}{
		{name: "timeoutMs 字符串", json: scenarioJSONWithStep(`{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"timeoutMs":"1000","area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}`), yaml: scenarioYAMLWithStep("id: observe\ntype: roam_in_area\nobservationStep: true\ndurationMs: 1000\ntimeoutMs: \"1000\"\narea:\n  type: radius\n  center: {x: 0, y: 64, z: 0}\n  radius: 2"), path: "cohorts[0].steps[0].timeoutMs"},
		{name: "pos.x 对象", json: scenarioJSONWithStep(`{"id":"move","type":"move_to_and_wait","pos":{"x":{},"y":64,"z":0},"radius":2}`), yaml: scenarioYAMLWithStep("id: move\ntype: move_to_and_wait\npos:\n  x: {}\n  y: 64\n  z: 0\nradius: 2"), path: "cohorts[0].steps[0].pos.x"},
		{name: "selector.types 字符串", json: scenarioJSONWithStep(`{"id":"attack","type":"attack_until","observationStep":true,"selector":{"types":"zombie","radius":2},"stop":{"durationMs":1000,"damageAtLeast":1},"attackIntervalMs":600}`), yaml: scenarioYAMLWithStep("id: attack\ntype: attack_until\nobservationStep: true\nselector:\n  types: zombie\n  radius: 2\nstop: {durationMs: 1000, damageAtLeast: 1}\nattackIntervalMs: 600"), path: "cohorts[0].steps[0].selector.types"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, raw := range []string{test.json, test.yaml} {
				_, err := ParseScenarioV2([]byte(raw))
				var validationErr *ScenarioValidationError
				require.ErrorAs(t, err, &validationErr)
				require.Equal(t, test.path, validationErr.Path)
				require.Equal(t, "字段类型无效", validationErr.Message)
			}
		})
	}
}

func TestParseScenarioV2_RejectsMalformedTemplates(t *testing.T) {
	tests := []struct {
		name    string
		command string
		message string
	}{
		{name: "未闭合", command: "/join {{roomKey", message: "模板语法无效"},
		{name: "空变量", command: "/join {{ }}", message: "模板语法无效"},
		{name: "多余闭合", command: "/join room}}", message: "模板语法无效"},
		{name: "嵌套", command: "/join {{room{{Key}}}}", message: "模板语法无效"},
		{name: "未知变量", command: "/join {{unknown}}", message: "包含未知模板变量 unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			jsonSource := `{"version":2,"seed":7,"cohorts":[{"key":"all","percent":100,"steps":[{"id":"send","type":"send_command","command":` + mustJSONText(t, test.command) + `},{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}]}]}`
			yamlSource := "version: 2\nseed: 7\ncohorts:\n  - key: all\n    percent: 100\n    steps:\n      - id: send\n        type: send_command\n        command: " + mustJSONText(t, test.command) + "\n      - id: observe\n        type: roam_in_area\n        observationStep: true\n        durationMs: 1000\n        area:\n          type: radius\n          center: {x: 0, y: 64, z: 0}\n          radius: 2\n"
			for _, raw := range []string{jsonSource, yamlSource} {
				_, err := ParseScenarioV2([]byte(raw))
				var validationErr *ScenarioValidationError
				require.ErrorAs(t, err, &validationErr)
				require.Equal(t, "cohorts[0].steps[0].command", validationErr.Path)
				require.Equal(t, test.message, validationErr.Message)
			}
		})
	}
}

func TestParseScenarioV2_ObservationStepMustBeContinuousAction(t *testing.T) {
	raw := scenarioJSONWithStep(`{"id":"observe","type":"wait","observationStep":true,"durationMs":1000}`)
	_, err := ParseScenarioV2([]byte(raw))
	var validationErr *ScenarioValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "cohorts[0].steps[0].observationStep", validationErr.Path)
	require.Equal(t, "只允许 roam_in_area 或 attack_until 标记为 observationStep", validationErr.Message)
}

func TestParseScenarioV2_LegacyBehaviorIsInternalOnly(t *testing.T) {
	newInput := scenarioJSONWithStep(`{"id":"legacy","type":"legacy_behavior","observationStep":true,"behavior":"patrol","durationMs":1000}`)
	_, err := ParseScenarioV2([]byte(newInput))
	var validationErr *ScenarioValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, "cohorts[0].steps[0].type", validationErr.Path)
	require.Equal(t, "新建 V2 场景不允许 legacy_behavior", validationErr.Message)

	internal := `{"version":2,"seed":7,"cohorts":[{"key":"legacy","percent":100,"steps":[{"id":"legacy","type":"legacy_behavior","behavior":"patrol","durationMs":1000},{"id":"observe","type":"roam_in_area","observationStep":true,"durationMs":1000,"area":{"type":"radius","center":{"x":0,"y":64,"z":0},"radius":2}}]}]}`
	_, err = ParseScenarioSnapshot(internal)
	require.NoError(t, err)
}

func scenarioJSONWithStep(step string) string {
	return `{"version":2,"seed":7,"cohorts":[{"key":"all","percent":100,"steps":[` + step + `]}]}`
}

func scenarioYAMLWithStep(step string) string {
	lines := strings.Split(step, "\n")
	for index := range lines {
		lines[index] = "        " + lines[index]
	}
	return "version: 2\nseed: 7\ncohorts:\n  - key: all\n    percent: 100\n    steps:\n      - " + strings.TrimSpace(lines[0]) + "\n" + strings.Join(lines[1:], "\n") + "\n"
}

func mustJSONText(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
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
