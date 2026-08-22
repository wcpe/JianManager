package service

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	minScenarioTimeoutMS = 100
	maxScenarioTimeoutMS = 3_600_000
)

var (
	scenarioKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	allowedTemplates   = map[string]struct{}{
		"botName": {}, "botUuid": {}, "runId": {}, "cohortKey": {},
		"correlationToken": {}, "roomKey": {},
	}
	boundRuntimeTemplates = map[string]struct{}{
		"botName": {}, "botUuid": {}, "runId": {}, "cohortKey": {},
		"correlationToken": {},
	}
)

func validateScenarioV2(scenario *ScenarioV2, allowLegacy bool) error {
	if scenario == nil {
		return scenarioValidationError("$", "场景不能为空")
	}
	if scenario.Version != 2 {
		return scenarioValidationError("version", "必须为 2")
	}
	if !scenario.seedPresent {
		return scenarioValidationError("seed", "必须提供 int64 随机种子")
	}
	if len(scenario.Cohorts) < 1 || len(scenario.Cohorts) > 20 {
		return scenarioValidationError("cohorts", "数量必须在 1..20 之间")
	}
	return validateScenarioCohorts(scenario, allowLegacy)
}

func validateScenarioCohorts(scenario *ScenarioV2, allowLegacy bool) error {
	seen := make(map[string]struct{}, len(scenario.Cohorts))
	totalPercent := 0
	for cohortIndex := range scenario.Cohorts {
		cohort := &scenario.Cohorts[cohortIndex]
		path := fmt.Sprintf("cohorts[%d]", cohortIndex)
		if !scenarioKeyPattern.MatchString(cohort.Key) {
			return scenarioValidationError(path+".key", "必须匹配 [a-z][a-z0-9-]{0,63}")
		}
		if _, exists := seen[cohort.Key]; exists {
			return scenarioValidationError(path+".key", "cohort key 必须唯一")
		}
		seen[cohort.Key] = struct{}{}
		if cohort.Percent < 1 || cohort.Percent > 100 {
			return scenarioValidationError(path+".percent", "必须在 1..100 之间")
		}
		totalPercent += cohort.Percent
		if err := validateScenarioSteps(cohort, cohortIndex, allowLegacy); err != nil {
			return err
		}
	}
	if totalPercent != 100 {
		return scenarioValidationError("cohorts", "percent 合计必须恰为 100")
	}
	return nil
}

func validateScenarioSteps(cohort *ScenarioCohort, cohortIndex int, allowLegacy bool) error {
	path := fmt.Sprintf("cohorts[%d].steps", cohortIndex)
	if len(cohort.Steps) < 1 || len(cohort.Steps) > 100 {
		return scenarioValidationError(path, "数量必须在 1..100 之间")
	}
	seen := make(map[string]struct{}, len(cohort.Steps))
	observationCount := 0
	for stepIndex := range cohort.Steps {
		action := &cohort.Steps[stepIndex]
		stepPath := fmt.Sprintf("%s[%d]", path, stepIndex)
		base := action.Base()
		if base == nil {
			return scenarioValidationError(stepPath, "动作不能为空")
		}
		if !scenarioKeyPattern.MatchString(base.ID) {
			return scenarioValidationError(stepPath+".id", "必须匹配 [a-z][a-z0-9-]{0,63}")
		}
		if _, exists := seen[base.ID]; exists {
			return scenarioValidationError(stepPath+".id", "step id 必须在 cohort 内唯一")
		}
		seen[base.ID] = struct{}{}
		if err := validateScenarioAction(action, stepPath, allowLegacy); err != nil {
			return err
		}
		if base.ObservationStep {
			if !isScenarioObservationAction(action, allowLegacy) {
				return scenarioValidationError(stepPath+".observationStep", "只允许 roam_in_area 或 attack_until 标记为 observationStep")
			}
			observationCount++
		}
	}
	if observationCount != 1 {
		return scenarioValidationError(path, "必须恰有一个 observationStep=true")
	}
	return validateRespawnReferences(cohort, cohortIndex, seen)
}

func isScenarioObservationAction(action *ScenarioAction, allowLegacy bool) bool {
	if action.Type() == ScenarioActionRoamInArea || action.Type() == ScenarioActionAttackUntil {
		return true
	}
	return allowLegacy && action.Type() == ScenarioActionLegacyBehavior && isLegacyContinuousBehavior(action.LegacyBehavior.Behavior)
}

func isLegacyContinuousBehavior(behavior string) bool {
	switch behavior {
	case "follow", "patrol", "guard", "roam":
		return true
	default:
		return false
	}
}

func validateScenarioAction(action *ScenarioAction, path string, allowLegacy bool) error {
	base := action.Base()
	if err := validateScenarioActionBase(base, path); err != nil {
		return err
	}
	if err := validateActionTemplates(*action, path); err != nil {
		return err
	}
	switch action.Type() {
	case ScenarioActionWaitSpawn:
		return nil
	case ScenarioActionRoamInArea:
		return validateRoamAction(action.RoamInArea, path)
	case ScenarioActionSendCommand:
		return validateSendCommandAction(action.SendCommand, path)
	case ScenarioActionWaitProbeEvent:
		return validateWaitProbeAction(action.WaitProbeEvent, path)
	case ScenarioActionBarrier:
		return validateBarrierAction(action.Barrier, path)
	case ScenarioActionMoveToAndWait:
		return validateMoveAction(action.MoveToAndWait, path)
	case ScenarioActionFindEntity:
		return validateEntitySelector(action.FindEntity.Selector, path+".selector")
	case ScenarioActionAttackUntil:
		return validateAttackAction(action.AttackUntil, path, allowLegacy)
	case ScenarioActionWait:
		return validatePositiveDuration(action.Wait.DurationMS, path+".durationMs")
	case ScenarioActionRespawnAndRejoin:
		return validateRespawnAction(action.RespawnAndRejoin, path)
	case ScenarioActionLegacyBehavior:
		if allowLegacy {
			return validateLegacyAction(action.LegacyBehavior, path)
		}
		return scenarioValidationError(path+".type", "新建 V2 场景不允许 legacy_behavior")
	default:
		return scenarioValidationError(path+".type", "不支持的动作类型")
	}
}

func validateScenarioActionBase(base *ScenarioActionBase, path string) error {
	if base.TimeoutMS == nil {
		value := defaultScenarioTimeout(base.ActionType)
		base.TimeoutMS = &value
	}
	if *base.TimeoutMS < minScenarioTimeoutMS || *base.TimeoutMS > maxScenarioTimeoutMS {
		return scenarioValidationError(path+".timeoutMs", "必须在 100..3600000 之间")
	}
	if base.MaxAttempts == nil {
		value := 1
		base.MaxAttempts = &value
	}
	if *base.MaxAttempts < 1 || *base.MaxAttempts > 10 {
		return scenarioValidationError(path+".maxAttempts", "必须在 1..10 之间")
	}
	if base.RetryBackoffMS == nil {
		value := 0
		base.RetryBackoffMS = &value
	}
	if *base.RetryBackoffMS < 0 || *base.RetryBackoffMS > 60_000 {
		return scenarioValidationError(path+".retryBackoffMs", "必须在 0..60000 之间")
	}
	if base.ResumePolicy == "" {
		base.ResumePolicy = "restart_step"
	}
	if base.ResumePolicy != "restart_step" && base.ResumePolicy != "restart_scenario" && base.ResumePolicy != "fail" {
		return scenarioValidationError(path+".resumePolicy", "必须为 restart_step、restart_scenario 或 fail")
	}
	return nil
}

func defaultScenarioTimeout(actionType ScenarioActionType) int {
	switch actionType {
	case ScenarioActionSendCommand:
		return 10_000
	case ScenarioActionBarrier, ScenarioActionRespawnAndRejoin:
		return 60_000
	case ScenarioActionMoveToAndWait:
		return 45_000
	case ScenarioActionAttackUntil, ScenarioActionRoamInArea, ScenarioActionWait, ScenarioActionLegacyBehavior:
		return maxScenarioTimeoutMS
	default:
		return 30_000
	}
}

func validateRoamAction(action *RoamInAreaAction, path string) error {
	if err := validatePositiveDuration(action.DurationMS, path+".durationMs"); err != nil {
		return err
	}
	if err := validateArea(&action.Area, path+".area"); err != nil {
		return err
	}
	if action.PauseMS.Min < 0 || action.PauseMS.Max < action.PauseMS.Min {
		return scenarioValidationError(path+".pauseMs", "必须满足 0 <= min <= max")
	}
	if action.MaxPathFailures == 0 {
		action.MaxPathFailures = 3
	}
	return nil
}

func validateArea(area *ScenarioArea, path string) error {
	switch area.Type {
	case "radius":
		if err := validatePosition(area.Center, path+".center"); err != nil {
			return err
		}
		if err := validateRadius(area.Radius, path+".radius"); err != nil {
			return err
		}
	case "waypoints":
		if len(area.Waypoints) == 0 {
			return scenarioValidationError(path+".waypoints", "至少需要一个航点")
		}
		for index, position := range area.Waypoints {
			if err := validatePosition(position, fmt.Sprintf("%s.waypoints[%d]", path, index)); err != nil {
				return err
			}
		}
	default:
		return scenarioValidationError(path+".type", "必须为 radius 或 waypoints")
	}
	return nil
}

func validateSendCommandAction(action *SendCommandAction, path string) error {
	length := utf8.RuneCountInString(action.Command)
	if length < 1 || length > 256 {
		return scenarioValidationError(path+".command", "长度必须在 1..256 之间")
	}
	if strings.ContainsAny(action.Command, "\r\n\x00") {
		return scenarioValidationError(path+".command", "禁止包含换行或 NUL")
	}
	return nil
}

func validateWaitProbeAction(action *WaitProbeEventAction, path string) error {
	if strings.TrimSpace(action.Event) == "" {
		return scenarioValidationError(path+".event", "不能为空")
	}
	return nil
}

func validateBarrierAction(action *BarrierAction, path string) error {
	if !scenarioKeyPattern.MatchString(action.Key) {
		return scenarioValidationError(path+".key", "必须匹配 [a-z][a-z0-9-]{0,63}")
	}
	switch action.Release.Type {
	case "all":
		if action.Release.Value != 0 {
			return scenarioValidationError(path+".release.value", "all 不接受 value")
		}
	case "count":
		if action.Release.Value < 1 {
			return scenarioValidationError(path+".release.value", "count 必须大于 0")
		}
	case "percent":
		if action.Release.Value < 1 || action.Release.Value > 100 {
			return scenarioValidationError(path+".release.value", "percent 必须在 1..100 之间")
		}
	default:
		return scenarioValidationError(path+".release.type", "必须为 all、count 或 percent")
	}
	if action.TimeoutPolicy == "" {
		action.TimeoutPolicy = "fail"
	}
	if action.TimeoutPolicy != "fail" && action.TimeoutPolicy != "release-arrived" {
		return scenarioValidationError(path+".timeoutPolicy", "必须为 fail 或 release-arrived")
	}
	return nil
}

func validateMoveAction(action *MoveToAndWaitAction, path string) error {
	if err := validatePosition(action.Pos, path+".pos"); err != nil {
		return err
	}
	if err := validateRadius(action.Radius, path+".radius"); err != nil {
		return err
	}
	if action.RequireProbeEvent != "" && action.RequireProbeEvent != "area_arrived" {
		return scenarioValidationError(path+".requireProbeEvent", "仅支持 area_arrived")
	}
	if action.RequireProbeEvent != "" && strings.TrimSpace(action.AreaID) == "" {
		return scenarioValidationError(path+".areaId", "启用探针抵达确认时不能为空")
	}
	return nil
}

func validateEntitySelector(selector ScenarioEntitySelector, path string) error {
	if len(selector.Types) > 32 {
		return scenarioValidationError(path+".types", "最多允许 32 个实体类型")
	}
	if utf8.RuneCountInString(selector.NameRegex) > 128 {
		return scenarioValidationError(path+".nameRegex", "长度最多 128 个字符")
	}
	if selector.NameRegex != "" {
		if _, err := regexp.Compile(selector.NameRegex); err != nil {
			return scenarioValidationError(path+".nameRegex", "正则表达式无效")
		}
	}
	if err := validateRadius(selector.Radius, path+".radius"); err != nil {
		return err
	}
	if selector.Priority != "" && selector.Priority != "nearest" && selector.Priority != "lowest_health" && selector.Priority != "random" {
		return scenarioValidationError(path+".priority", "必须为 nearest、lowest_health 或 random")
	}
	return nil
}

func validateAttackAction(action *AttackUntilAction, path string, allowLegacy bool) error {
	if action.LegacyDurationSuccess && !allowLegacy {
		return scenarioValidationError(path+".legacyDurationSuccess", "新建 V2 场景不允许内部兼容字段")
	}
	if err := validateEntitySelector(action.Selector, path+".selector"); err != nil {
		return err
	}
	if action.AttackIntervalMS < 100 || action.AttackIntervalMS > 5000 {
		return scenarioValidationError(path+".attackIntervalMs", "必须在 100..5000 之间")
	}
	if action.MaxPathFailures < 0 || action.MaxPathFailures > 100 {
		return scenarioValidationError(path+".maxPathFailures", "必须在 0..100 之间")
	}
	if action.SearchArea != nil {
		if err := validateArea(action.SearchArea, path+".searchArea"); err != nil {
			return err
		}
	}
	if action.Respawn != nil {
		if action.Respawn.MaxAttempts < 1 || action.Respawn.MaxAttempts > 1000 {
			return scenarioValidationError(path+".respawn.maxAttempts", "必须在 1..1000 之间")
		}
		if action.Respawn.RetryBackoffMS < 0 || action.Respawn.RetryBackoffMS > 300000 {
			return scenarioValidationError(path+".respawn.retryBackoffMs", "必须在 0..300000 之间")
		}
		if action.Respawn.TimeoutMS < 1 || action.Respawn.TimeoutMS > 300000 {
			return scenarioValidationError(path+".respawn.timeoutMs", "必须在 1..300000 之间")
		}
	}
	stop := &action.Stop
	if err := validatePositiveDuration(stop.DurationMS, path+".stop.durationMs"); err != nil {
		return err
	}
	if stop.DamageAtLeast < 0 || stop.KillsAtLeast < 0 || stop.MinClientAttackAttempts < 0 {
		return scenarioValidationError(path+".stop", "可信计数不能为负数")
	}
	if stop.SuccessPolicy == "" {
		stop.SuccessPolicy = "any"
	}
	if stop.SuccessPolicy != "any" && stop.SuccessPolicy != "all" {
		return scenarioValidationError(path+".stop.successPolicy", "必须为 any 或 all")
	}
	if err := validateEvidenceWindow(stop, path); err != nil {
		return err
	}
	trusted := stop.DamageAtLeast > 0 || stop.KillsAtLeast > 0 || strings.TrimSpace(stop.ProbeEvent) != "" || stop.MinDamageEventsPerWindow > 0 || stop.MinClientAttackAttempts > 0
	if !trusted && !allowLegacy {
		return scenarioValidationError(path+".stop", "至少需要一个伤害、击杀、探针、证据窗或客户端攻击活跃度条件")
	}
	return nil
}

func validateEvidenceWindow(stop *ScenarioAttackStop, path string) error {
	if stop.EvidenceWindowMS == 0 && stop.MinDamageEventsPerWindow == 0 {
		return nil
	}
	if stop.EvidenceWindowMS < 1000 || stop.EvidenceWindowMS > 300_000 {
		return scenarioValidationError(path+".stop.evidenceWindowMs", "必须在 1000..300000 之间")
	}
	if stop.MinDamageEventsPerWindow < 1 || stop.MinDamageEventsPerWindow > 100_000 {
		return scenarioValidationError(path+".stop.minDamageEventsPerWindow", "必须在 1..100000 之间")
	}
	if stop.DurationMS < stop.EvidenceWindowMS {
		return scenarioValidationError(path+".stop.durationMs", "必须覆盖至少一个完整 evidence window")
	}
	return nil
}

func validateRespawnAction(action *RespawnAndRejoinAction, path string) error {
	if !scenarioKeyPattern.MatchString(action.EntryStepID) {
		return scenarioValidationError(path+".entryStepId", "必须引用合法 step id")
	}
	return nil
}

func validateLegacyAction(action *LegacyBehaviorAction, path string) error {
	if strings.TrimSpace(action.Behavior) == "" {
		return scenarioValidationError(path+".behavior", "不能为空")
	}
	return validatePositiveDuration(action.DurationMS, path+".durationMs")
}

func validatePositiveDuration(value int, path string) error {
	if value <= 0 {
		return scenarioValidationError(path, "必须大于 0")
	}
	return nil
}

func validatePosition(position ScenarioPosition, path string) error {
	values := []struct {
		name  string
		value float64
	}{{"x", position.X}, {"y", position.Y}, {"z", position.Z}}
	for _, item := range values {
		if math.IsNaN(item.value) || math.IsInf(item.value, 0) {
			return scenarioValidationError(path+"."+item.name, "必须是有限数值")
		}
	}
	return nil
}

func validateRadius(radius float64, path string) error {
	if math.IsNaN(radius) || math.IsInf(radius, 0) {
		return scenarioValidationError(path, "必须是有限数值")
	}
	if radius < 0.5 || radius > 256 {
		return scenarioValidationError(path, "必须在 0.5..256 之间")
	}
	return nil
}

func validateActionTemplates(action ScenarioAction, basePath string) error {
	for _, field := range scenarioActionTemplateFields(action, basePath) {
		if err := validateTemplateValue(field.value, field.path); err != nil {
			return err
		}
	}
	return nil
}

func validateTemplateValue(value, path string) error {
	for offset := 0; offset < len(value); {
		open := strings.Index(value[offset:], "{{")
		close := strings.Index(value[offset:], "}}")
		if close >= 0 && (open < 0 || close < open) {
			return scenarioValidationError(path, "模板语法无效")
		}
		if open < 0 {
			return nil
		}
		start := offset + open + 2
		endOffset := strings.Index(value[start:], "}}")
		if endOffset < 0 {
			return scenarioValidationError(path, "模板语法无效")
		}
		end := start + endOffset
		expression := value[start:end]
		if strings.Contains(expression, "{{") || strings.TrimSpace(expression) == "" {
			return scenarioValidationError(path, "模板语法无效")
		}
		name := strings.TrimSpace(expression)
		if _, ok := allowedTemplates[name]; !ok {
			return scenarioValidationError(path, "包含未知模板变量 "+name)
		}
		if _, bound := boundRuntimeTemplates[name]; !bound {
			return scenarioValidationError(path, "模板变量 "+name+" 未绑定")
		}
		offset = end + 2
	}
	return nil
}

type scenarioTemplateField struct {
	path  string
	value string
}

func scenarioActionTemplateFields(action ScenarioAction, path string) []scenarioTemplateField {
	fields := make([]scenarioTemplateField, 0, 8)
	appendField := func(name, value string) {
		fields = append(fields, scenarioTemplateField{path: path + "." + name, value: value})
	}
	switch action.Type() {
	case ScenarioActionSendCommand:
		appendField("command", action.SendCommand.Command)
	case ScenarioActionWaitProbeEvent:
		appendField("event", action.WaitProbeEvent.Event)
	case ScenarioActionBarrier:
		appendField("key", action.Barrier.Key)
	case ScenarioActionMoveToAndWait:
		appendField("areaId", action.MoveToAndWait.AreaID)
		appendField("requireProbeEvent", action.MoveToAndWait.RequireProbeEvent)
	case ScenarioActionFindEntity:
		appendSelectorTemplateFields(&fields, path+".selector", action.FindEntity.Selector)
	case ScenarioActionAttackUntil:
		appendSelectorTemplateFields(&fields, path+".selector", action.AttackUntil.Selector)
		appendField("stop.probeEvent", action.AttackUntil.Stop.ProbeEvent)
	case ScenarioActionRespawnAndRejoin:
		appendField("entryStepId", action.RespawnAndRejoin.EntryStepID)
	case ScenarioActionLegacyBehavior:
		appendField("behavior", action.LegacyBehavior.Behavior)
		appendField("target", action.LegacyBehavior.Target)
	}
	return fields
}

func appendSelectorTemplateFields(fields *[]scenarioTemplateField, path string, selector ScenarioEntitySelector) {
	*fields = append(*fields,
		scenarioTemplateField{path: path + ".kind", value: selector.Kind},
		scenarioTemplateField{path: path + ".nameRegex", value: selector.NameRegex},
	)
	for index, entityType := range selector.Types {
		*fields = append(*fields, scenarioTemplateField{path: fmt.Sprintf("%s.types[%d]", path, index), value: entityType})
	}
}

func validateRespawnReferences(cohort *ScenarioCohort, cohortIndex int, ids map[string]struct{}) error {
	byID := make(map[string]int, len(cohort.Steps))
	for index, action := range cohort.Steps {
		byID[action.Base().ID] = index
		if action.RespawnAndRejoin != nil {
			if _, exists := ids[action.RespawnAndRejoin.EntryStepID]; !exists {
				path := fmt.Sprintf("cohorts[%d].steps[%d].entryStepId", cohortIndex, index)
				return scenarioValidationError(path, "引用的入口 step 不存在")
			}
		}
	}
	state := make(map[string]uint8, len(cohort.Steps))
	for _, action := range cohort.Steps {
		if err := visitRespawnReference(cohort, cohortIndex, byID, state, action.Base().ID); err != nil {
			return err
		}
	}
	return nil
}

func visitRespawnReference(cohort *ScenarioCohort, cohortIndex int, byID map[string]int, state map[string]uint8, stepID string) error {
	if state[stepID] == 2 {
		return nil
	}
	state[stepID] = 1
	index := byID[stepID]
	action := cohort.Steps[index]
	if action.RespawnAndRejoin != nil {
		target := action.RespawnAndRejoin.EntryStepID
		if state[target] == 1 {
			path := fmt.Sprintf("cohorts[%d].steps[%d].entryStepId", cohortIndex, index)
			return scenarioValidationError(path, "respawn 引用形成无界递归")
		}
		if err := visitRespawnReference(cohort, cohortIndex, byID, state, target); err != nil {
			return err
		}
	}
	state[stepID] = 2
	return nil
}
