package service

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var scenarioActionBaseFields = stringSet(
	"id", "type", "observationStep", "timeoutMs", "maxAttempts", "retryBackoffMs", "resumePolicy",
)

func decodeScenarioDocument(raw []byte, allowLegacy bool) (*ScenarioV2, error) {
	isJSON := len(raw) > 0 && (raw[0] == '{' || raw[0] == '[')
	if isJSON && !json.Valid(raw) {
		return nil, scenarioValidationError("$", "JSON 语法或字段类型无效")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		message := "YAML 语法或字段类型无效"
		if isJSON {
			message = "JSON 语法或字段类型无效"
		}
		return nil, scenarioValidationError("$", message)
	}
	if len(document.Content) != 1 {
		return nil, scenarioValidationError("$", "场景必须是对象")
	}
	root := document.Content[0]
	normalizeScenarioNumberTags(root)
	if err := validateScenarioDocumentNode(root, allowLegacy); err != nil {
		return nil, err
	}
	return decodeValidatedScenarioNode(root)
}

func decodeValidatedScenarioNode(root *yaml.Node) (*ScenarioV2, error) {
	var wire scenarioYAMLWire
	if err := root.Decode(&wire); err != nil {
		return nil, scenarioValidationError("$", "字段类型无效")
	}
	scenario := &ScenarioV2{Version: wire.Version, Cohorts: make([]ScenarioCohort, len(wire.Cohorts))}
	if wire.Seed != nil {
		scenario.Seed, scenario.seedPresent = *wire.Seed, true
	}
	for cohortIndex, cohort := range wire.Cohorts {
		scenario.Cohorts[cohortIndex] = ScenarioCohort{Key: cohort.Key, Percent: cohort.Percent, Steps: make([]ScenarioAction, len(cohort.Steps))}
		for stepIndex := range cohort.Steps {
			path := fmt.Sprintf("cohorts[%d].steps[%d]", cohortIndex, stepIndex)
			action, err := decodeScenarioYAMLAction(&cohort.Steps[stepIndex], path)
			if err != nil {
				return nil, err
			}
			scenario.Cohorts[cohortIndex].Steps[stepIndex] = action
		}
	}
	return scenario, nil
}

func validateScenarioDocumentNode(root *yaml.Node, allowLegacy bool) error {
	fields, err := scenarioMappingFields(root, "$")
	if err != nil {
		return err
	}
	if err := rejectUnknownScenarioFields(fields, stringSet("version", "seed", "cohorts"), "$"); err != nil {
		return err
	}
	if err := validateScenarioIntegerNode(fields["version"], "version"); err != nil {
		return err
	}
	if err := validateScenarioIntegerNode(fields["seed"], "seed"); err != nil {
		return err
	}
	cohorts := fields["cohorts"]
	if cohorts == nil {
		return nil
	}
	if cohorts.Kind != yaml.SequenceNode {
		return scenarioNodeTypeError("cohorts")
	}
	for index, cohort := range cohorts.Content {
		if err := validateScenarioCohortNode(cohort, index, allowLegacy); err != nil {
			return err
		}
	}
	return nil
}

func validateScenarioCohortNode(node *yaml.Node, index int, allowLegacy bool) error {
	path := fmt.Sprintf("cohorts[%d]", index)
	fields, err := scenarioMappingFields(node, path)
	if err != nil {
		return err
	}
	if err := rejectUnknownScenarioFields(fields, stringSet("key", "percent", "steps"), path); err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["key"], path+".key"); err != nil {
		return err
	}
	if err := validateScenarioIntegerNode(fields["percent"], path+".percent"); err != nil {
		return err
	}
	steps := fields["steps"]
	if steps == nil {
		return nil
	}
	if steps.Kind != yaml.SequenceNode {
		return scenarioNodeTypeError(path + ".steps")
	}
	for stepIndex, step := range steps.Content {
		stepPath := fmt.Sprintf("%s.steps[%d]", path, stepIndex)
		if err := validateScenarioActionNode(step, stepPath, allowLegacy); err != nil {
			return err
		}
	}
	return nil
}

func validateScenarioActionNode(node *yaml.Node, path string, allowLegacy bool) error {
	fields, err := scenarioMappingFields(node, path)
	if err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["type"], path+".type"); err != nil {
		return err
	}
	actionType := ScenarioActionType("")
	if fields["type"] != nil {
		actionType = ScenarioActionType(fields["type"].Value)
	}
	allowed, err := scenarioActionFields(actionType, path, allowLegacy)
	if err != nil {
		return err
	}
	if err := rejectUnknownScenarioFields(fields, allowed, path); err != nil {
		return err
	}
	if err := validateScenarioActionBaseNodes(fields, path); err != nil {
		return err
	}
	return validateScenarioActionSpecificNodes(fields, actionType, path)
}

func scenarioActionFields(actionType ScenarioActionType, path string, allowLegacy bool) (map[string]struct{}, error) {
	fields := copyStringSet(scenarioActionBaseFields)
	var extras []string
	switch actionType {
	case ScenarioActionWaitSpawn:
	case ScenarioActionRoamInArea:
		extras = []string{"durationMs", "area", "pauseMs", "maxPathFailures"}
	case ScenarioActionSendCommand:
		extras = []string{"command"}
	case ScenarioActionWaitProbeEvent:
		extras = []string{"event"}
	case ScenarioActionBarrier:
		extras = []string{"key", "release", "timeoutPolicy"}
	case ScenarioActionMoveToAndWait:
		extras = []string{"pos", "radius", "areaId", "requireProbeEvent"}
	case ScenarioActionFindEntity:
		extras = []string{"selector"}
	case ScenarioActionAttackUntil:
		extras = []string{"selector", "stop", "attackIntervalMs", "chase", "reacquire", "targetNotFoundTimeoutMs"}
	case ScenarioActionWait:
		extras = []string{"durationMs"}
	case ScenarioActionRespawnAndRejoin:
		extras = []string{"entryStepId"}
	case ScenarioActionLegacyBehavior:
		if !allowLegacy {
			return nil, scenarioValidationError(path+".type", "新建 V2 场景不允许 legacy_behavior")
		}
		extras = []string{"behavior", "target", "durationMs", "step"}
	case "":
		return fields, nil
	default:
		return nil, scenarioValidationError(path+".type", "不支持的动作类型")
	}
	for _, field := range extras {
		fields[field] = struct{}{}
	}
	return fields, nil
}

func validateScenarioActionBaseNodes(fields map[string]*yaml.Node, path string) error {
	checks := []struct {
		name string
		kind func(*yaml.Node, string) error
	}{
		{"id", validateScenarioStringNode}, {"type", validateScenarioStringNode},
		{"observationStep", validateScenarioBoolNode}, {"timeoutMs", validateScenarioIntegerNode},
		{"maxAttempts", validateScenarioIntegerNode}, {"retryBackoffMs", validateScenarioIntegerNode},
		{"resumePolicy", validateScenarioStringNode},
	}
	for _, check := range checks {
		if err := check.kind(fields[check.name], path+"."+check.name); err != nil {
			return err
		}
	}
	return nil
}

func validateScenarioActionSpecificNodes(fields map[string]*yaml.Node, actionType ScenarioActionType, path string) error {
	switch actionType {
	case ScenarioActionRoamInArea:
		return validateScenarioRoamNodes(fields, path)
	case ScenarioActionSendCommand:
		return validateScenarioStringNode(fields["command"], path+".command")
	case ScenarioActionWaitProbeEvent:
		return validateScenarioStringNode(fields["event"], path+".event")
	case ScenarioActionBarrier:
		return validateScenarioBarrierNodes(fields, path)
	case ScenarioActionMoveToAndWait:
		return validateScenarioMoveNodes(fields, path)
	case ScenarioActionFindEntity:
		return validateScenarioSelectorNode(fields["selector"], path+".selector")
	case ScenarioActionAttackUntil:
		return validateScenarioAttackNodes(fields, path)
	case ScenarioActionWait:
		return validateScenarioIntegerNode(fields["durationMs"], path+".durationMs")
	case ScenarioActionRespawnAndRejoin:
		return validateScenarioStringNode(fields["entryStepId"], path+".entryStepId")
	case ScenarioActionLegacyBehavior:
		return validateScenarioLegacyNodes(fields, path)
	default:
		return nil
	}
}

func validateScenarioRoamNodes(fields map[string]*yaml.Node, path string) error {
	if err := validateScenarioIntegerNode(fields["durationMs"], path+".durationMs"); err != nil {
		return err
	}
	if err := validateScenarioAreaNode(fields["area"], path+".area"); err != nil {
		return err
	}
	if err := validateScenarioRangeNode(fields["pauseMs"], path+".pauseMs"); err != nil {
		return err
	}
	return validateScenarioIntegerNode(fields["maxPathFailures"], path+".maxPathFailures")
}

func validateScenarioAreaNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	fields, err := scenarioMappingFields(node, path)
	if err != nil {
		return err
	}
	if err := rejectUnknownScenarioFields(fields, stringSet("type", "center", "radius", "waypoints"), path); err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["type"], path+".type"); err != nil {
		return err
	}
	if err := validateScenarioPositionNode(fields["center"], path+".center"); err != nil {
		return err
	}
	if err := validateScenarioNumberNode(fields["radius"], path+".radius"); err != nil {
		return err
	}
	return validateScenarioPositionsNode(fields["waypoints"], path+".waypoints")
}

func validateScenarioRangeNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	fields, err := scenarioMappingFields(node, path)
	if err != nil {
		return err
	}
	if err := rejectUnknownScenarioFields(fields, stringSet("min", "max"), path); err != nil {
		return err
	}
	if err := validateScenarioIntegerNode(fields["min"], path+".min"); err != nil {
		return err
	}
	return validateScenarioIntegerNode(fields["max"], path+".max")
}

func validateScenarioPositionNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	fields, err := scenarioMappingFields(node, path)
	if err != nil {
		return err
	}
	if err := rejectUnknownScenarioFields(fields, stringSet("x", "y", "z"), path); err != nil {
		return err
	}
	for _, name := range []string{"x", "y", "z"} {
		if err := validateScenarioNumberNode(fields[name], path+"."+name); err != nil {
			return err
		}
	}
	return nil
}

func validateScenarioPositionsNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		return scenarioNodeTypeError(path)
	}
	for index, item := range node.Content {
		if err := validateScenarioPositionNode(item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateScenarioBarrierNodes(fields map[string]*yaml.Node, path string) error {
	if err := validateScenarioStringNode(fields["key"], path+".key"); err != nil {
		return err
	}
	if err := validateScenarioReleaseNode(fields["release"], path+".release"); err != nil {
		return err
	}
	return validateScenarioStringNode(fields["timeoutPolicy"], path+".timeoutPolicy")
}

func validateScenarioReleaseNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	fields, err := scenarioMappingFields(node, path)
	if err != nil {
		return err
	}
	if err := rejectUnknownScenarioFields(fields, stringSet("type", "value"), path); err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["type"], path+".type"); err != nil {
		return err
	}
	return validateScenarioIntegerNode(fields["value"], path+".value")
}

func validateScenarioMoveNodes(fields map[string]*yaml.Node, path string) error {
	if err := validateScenarioPositionNode(fields["pos"], path+".pos"); err != nil {
		return err
	}
	if err := validateScenarioNumberNode(fields["radius"], path+".radius"); err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["areaId"], path+".areaId"); err != nil {
		return err
	}
	return validateScenarioStringNode(fields["requireProbeEvent"], path+".requireProbeEvent")
}

func validateScenarioSelectorNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	fields, err := scenarioMappingFields(node, path)
	if err != nil {
		return err
	}
	if err := rejectUnknownScenarioFields(fields, stringSet("kind", "types", "nameRegex", "radius", "priority"), path); err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["kind"], path+".kind"); err != nil {
		return err
	}
	if err := validateScenarioStringSequenceNode(fields["types"], path+".types"); err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["nameRegex"], path+".nameRegex"); err != nil {
		return err
	}
	if err := validateScenarioNumberNode(fields["radius"], path+".radius"); err != nil {
		return err
	}
	return validateScenarioStringNode(fields["priority"], path+".priority")
}

func validateScenarioAttackNodes(fields map[string]*yaml.Node, path string) error {
	if err := validateScenarioSelectorNode(fields["selector"], path+".selector"); err != nil {
		return err
	}
	if err := validateScenarioStopNode(fields["stop"], path+".stop"); err != nil {
		return err
	}
	for _, name := range []string{"attackIntervalMs", "targetNotFoundTimeoutMs"} {
		if err := validateScenarioIntegerNode(fields[name], path+"."+name); err != nil {
			return err
		}
	}
	for _, name := range []string{"chase", "reacquire"} {
		if err := validateScenarioBoolNode(fields[name], path+"."+name); err != nil {
			return err
		}
	}
	return nil
}

func validateScenarioStopNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	fields, err := scenarioMappingFields(node, path)
	if err != nil {
		return err
	}
	allowed := stringSet("durationMs", "damageAtLeast", "killsAtLeast", "probeEvent", "evidenceWindowMs", "minDamageEventsPerWindow", "successPolicy")
	if err := rejectUnknownScenarioFields(fields, allowed, path); err != nil {
		return err
	}
	for _, name := range []string{"durationMs", "damageAtLeast", "killsAtLeast", "evidenceWindowMs", "minDamageEventsPerWindow"} {
		if err := validateScenarioIntegerNode(fields[name], path+"."+name); err != nil {
			return err
		}
	}
	if err := validateScenarioStringNode(fields["probeEvent"], path+".probeEvent"); err != nil {
		return err
	}
	return validateScenarioStringNode(fields["successPolicy"], path+".successPolicy")
}

func validateScenarioLegacyNodes(fields map[string]*yaml.Node, path string) error {
	if err := validateScenarioStringNode(fields["behavior"], path+".behavior"); err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["target"], path+".target"); err != nil {
		return err
	}
	if err := validateScenarioIntegerNode(fields["durationMs"], path+".durationMs"); err != nil {
		return err
	}
	return validateScenarioLegacyStepNode(fields["step"], path+".step")
}

func validateScenarioLegacyStepNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	fields, err := scenarioMappingFields(node, path)
	if err != nil {
		return err
	}
	if err := rejectUnknownScenarioFields(fields, stringSet("type", "message", "durationMs", "duration", "pos"), path); err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["type"], path+".type"); err != nil {
		return err
	}
	if err := validateScenarioStringNode(fields["message"], path+".message"); err != nil {
		return err
	}
	for _, name := range []string{"durationMs", "duration"} {
		if err := validateScenarioIntegerNode(fields[name], path+"."+name); err != nil {
			return err
		}
	}
	return validateScenarioPositionNode(fields["pos"], path+".pos")
}

func normalizeScenarioNumberTags(node *yaml.Node) {
	if node == nil {
		return
	}
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" && node.Style == 0 && json.Valid([]byte(node.Value)) {
		if strings.ContainsAny(node.Value, ".eE") {
			node.Tag = "!!float"
		} else if node.Value != "true" && node.Value != "false" && node.Value != "null" {
			node.Tag = "!!int"
		}
	}
	for _, child := range node.Content {
		normalizeScenarioNumberTags(child)
	}
}

func scenarioMappingFields(node *yaml.Node, path string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, scenarioNodeTypeError(path)
	}
	fields := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return nil, scenarioValidationError(path, "字段名必须是字符串")
		}
		fieldPath := scenarioChildPath(path, key.Value)
		if _, exists := fields[key.Value]; exists {
			return nil, scenarioValidationError(fieldPath, "字段重复")
		}
		fields[key.Value] = value
	}
	return fields, nil
}

func rejectUnknownScenarioFields(fields map[string]*yaml.Node, allowed map[string]struct{}, path string) error {
	unknown := make([]string, 0)
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return scenarioValidationError(scenarioChildPath(path, unknown[0]), "未知字段")
}

func validateScenarioStringNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return scenarioNodeTypeError(path)
	}
	return nil
}

func validateScenarioIntegerNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!int" {
		return scenarioNodeTypeError(path)
	}
	var value int64
	if err := node.Decode(&value); err != nil {
		return scenarioNodeTypeError(path)
	}
	return nil
}

func validateScenarioNumberNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.ScalarNode || (node.Tag != "!!int" && node.Tag != "!!float") {
		return scenarioNodeTypeError(path)
	}
	var value float64
	if err := node.Decode(&value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return scenarioValidationError(path, "必须是有限数值")
	}
	return nil
}

func validateScenarioBoolNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!bool" {
		return scenarioNodeTypeError(path)
	}
	return nil
}

func validateScenarioStringSequenceNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		return scenarioNodeTypeError(path)
	}
	for index, item := range node.Content {
		if err := validateScenarioStringNode(item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func scenarioNodeTypeError(path string) error {
	return scenarioValidationError(path, "字段类型无效")
}

func scenarioChildPath(path, child string) string {
	if path == "" || path == "$" {
		return child
	}
	return path + "." + child
}

func stringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func copyStringSet(source map[string]struct{}) map[string]struct{} {
	copy := make(map[string]struct{}, len(source))
	for value := range source {
		copy[value] = struct{}{}
	}
	return copy
}
