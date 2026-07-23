package service

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

// 命令编排相关常量与错误码，遵循 FR-369 规格（ADR-075 bot.chat 成功边界）。
const (
	commandScheduleMinCommands      = 1
	commandScheduleMaxCommands      = 100
	commandScheduleMaxOccurrences   = 1000
	commandScheduleMaxJSONBytes     = 256 * 1024
	commandScheduleMaxDurationMS    = 86_400_000
	commandScheduleMaxJitterMS      = 60_000
	commandScheduleCommandIDMaxLen  = 64
	commandScheduleCommandMaxBytes  = 1024
	commandScheduleMaxIntervalMS    = 86_400_000
	commandScheduleDefaultStepID    = "command-schedule"
	commandScheduleBotChatAttempts  = 3
	commandScheduleRetryBackoffMS   = 250
)

// 命令编排动作结果状态（与现有 BotLoadActionResultStatus 共用枚举），用于 Bot Worker 上报。
const (
	CommandResultStatusSent      = "sent"
	CommandResultStatusFailed    = "failed"
	CommandResultStatusTimedOut  = "timed_out"
	CommandResultStatusCancelled = "cancelled"
)

// errCommandArgumentInvalid 用于在 Finalize 中报告非法 skip 引用。
var errCommandArgumentInvalid = errors.New(CommandErrorArgumentInvalid)

// FR-369 错误码；与 ActionResult allowlist 合并。
const (
	CommandErrorRouteFailed         = "COMMAND_ROUTE_FAILED"
	CommandErrorIPCFailed           = "COMMAND_IPC_FAILED"
	CommandErrorArgumentInvalid     = "COMMAND_ARGUMENT_INVALID"
	CommandErrorRuntimeUnavailable  = "COMMAND_RUNTIME_UNAVAILABLE"
	CommandErrorScheduleRejected    = "COMMAND_SCHEDULE_REJECTED"
	CommandErrorDeadlineExceeded    = "COMMAND_DEADLINE_EXCEEDED"
	CommandErrorSendFailed          = "COMMAND_SEND_FAILED"
)

// CommandScheduleVariable 白名单模板变量。
var CommandScheduleVariable = map[string]struct{}{
	"botName":          {},
	"botOrdinal":       {},
	"cohortKey":        {},
	"runId":            {},
	"actionRunId":      {},
	"correlationToken": {},
}

var commandScheduleIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var commandScheduleJitterSeedPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,19})$`)

// CommandScheduleInput 是 CP 接收的命令计划原始结构，与 API 字段保持一致。
type CommandScheduleInput struct {
	Commands    []CommandScheduleInputCommand `json:"commands"`
	DurationMS  int64                         `json:"durationMs"`
	JitterMS    *int64                        `json:"jitterMs,omitempty"`
}

// CommandScheduleInputCommand 单条命令；repeat 字段可选且按规格只描述重复配置。
type CommandScheduleInputCommand struct {
	ID        string                       `json:"id"`
	AtMS      int64                        `json:"atMs"`
	Command   string                       `json:"command"`
	Repeat    *CommandScheduleInputRepeat  `json:"repeat,omitempty"`
}

// CommandScheduleInputRepeat repeat 配置。
type CommandScheduleInputRepeat struct {
	IntervalMS int64 `json:"intervalMs"`
	Count      int   `json:"count"`
}

// CommandScheduleOccurrence 是 CP 冻结的发生项（已展开变量与 actionRunId）。
type CommandScheduleOccurrence struct {
	CommandID             string `json:"commandId"`
	Occurrence            int    `json:"occurrence"`
	CommandDeclarationIdx int    `json:"commandDeclarationIndex"`
	BaseAtMS              int64  `json:"baseAtMs"`
	JitterOffsetMS        int64  `json:"jitterOffsetMs"`
	ActionRunID           string `json:"actionRunId"`
	Command               string `json:"command"`
	// RawTemplate 仅为 Normalize/Finalize 内部使用，下发前清空。
	RawTemplate string `json:"-"`
}

// CommandSchedulePlan 是规范化后的冻结计划，可直接下发 Worker/Bot Worker。
type CommandSchedulePlan struct {
	DurationMS  int64                       `json:"durationMs"`
	JitterMS    int64                       `json:"jitterMs"`
	Occurrences []CommandScheduleOccurrence `json:"occurrences"`
}

// CommandScheduleTemplateContext 提供展开模板变量所需的运行上下文。
type CommandScheduleTemplateContext struct {
	BotName          string
	BotOrdinal       string
	CohortKey        string
	RunID            string // bot_stress_sessions.id 的十进制文本
	ActionRunID      string
	CorrelationToken string
}

// CommandScheduleValidationError 用于稳定路径级错误描述。
type CommandScheduleValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Error 实现 error 接口。
func (e *CommandScheduleValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

func newCommandScheduleError(path, message string) *CommandScheduleValidationError {
	return &CommandScheduleValidationError{Path: path, Message: message}
}

// NormalizeCommandSchedule 校验、规范化并展开为 occurrences；jitter 偏移统一基于 jitterSeed 计算。
// 校验失败的任何路径级别问题均返回 *CommandScheduleValidationError；jitterMs 缺省时规范化为 0。
func NormalizeCommandSchedule(input *CommandScheduleInput) (*CommandSchedulePlan, error) {
	if input == nil {
		return nil, newCommandScheduleError("$", "命令计划不能为空")
	}
	count := len(input.Commands)
	if count < commandScheduleMinCommands || count > commandScheduleMaxCommands {
		return nil, newCommandScheduleError("commands", fmt.Sprintf("数量必须在 %d..%d 之间", commandScheduleMinCommands, commandScheduleMaxCommands))
	}
	if input.DurationMS < 1 || input.DurationMS > commandScheduleMaxDurationMS {
		return nil, newCommandScheduleError("durationMs", fmt.Sprintf("必须在 1..%d 之间", commandScheduleMaxDurationMS))
	}
	jitter := int64(0)
	if input.JitterMS != nil {
		jitter = *input.JitterMS
		if jitter < 0 || jitter > commandScheduleMaxJitterMS || jitter > input.DurationMS {
			return nil, newCommandScheduleError("jitterMs", fmt.Sprintf("必须在 0..min(%d,durationMs) 之间", commandScheduleMaxJitterMS))
		}
	}
	seen := make(map[string]struct{}, count)
	for i := range input.Commands {
		cmd := &input.Commands[i]
		path := fmt.Sprintf("commands[%d]", i)
		if !commandScheduleIDPattern.MatchString(cmd.ID) || len(cmd.ID) == 0 || len(cmd.ID) > commandScheduleCommandIDMaxLen {
			return nil, newCommandScheduleError(path+".id", "必须匹配 [A-Za-z0-9._-]+ 且长度 1..64")
		}
		if _, exists := seen[cmd.ID]; exists {
			return nil, newCommandScheduleError(path+".id", "计划内 id 必须唯一")
		}
		seen[cmd.ID] = struct{}{}
		if cmd.AtMS < 0 || cmd.AtMS > input.DurationMS {
			return nil, newCommandScheduleError(path+".atMs", fmt.Sprintf("必须在 0..%d 之间", input.DurationMS))
		}
		if !utf8.ValidString(cmd.Command) {
			return nil, newCommandScheduleError(path+".command", "必须是合法 UTF-8")
		}
		if err := validateCommandScheduleCommandText(path+".command", cmd.Command); err != nil {
			return nil, err
		}
		if err := validateCommandScheduleTemplate(path+".command", cmd.Command); err != nil {
			return nil, err
		}
		countOccurrences := 1
		interval := int64(0)
		if cmd.Repeat != nil {
			if cmd.Repeat.IntervalMS < 1 || cmd.Repeat.IntervalMS > commandScheduleMaxIntervalMS {
				return nil, newCommandScheduleError(path+".repeat.intervalMs", fmt.Sprintf("必须在 1..%d 之间", commandScheduleMaxIntervalMS))
			}
			if cmd.Repeat.Count < 1 || cmd.Repeat.Count > commandScheduleMaxOccurrences {
				return nil, newCommandScheduleError(path+".repeat.count", fmt.Sprintf("必须在 1..%d 之间", commandScheduleMaxOccurrences))
			}
			interval = cmd.Repeat.IntervalMS
			countOccurrences = cmd.Repeat.Count
		}
		if cmd.AtMS+int64(countOccurrences-1)*interval > input.DurationMS {
			return nil, newCommandScheduleError(path, "展开后超出 durationMs，整份计划拒绝")
		}
	}
	// 展开全部 occurrence 后按 (baseAtMs, declarationIndex, occurrence) 排序。
	type expanded struct {
		declarationIndex int
		occurrence        int
		baseAtMS          int64
		commandID         string
		template          string
	}
	var list []expanded
	for i := range input.Commands {
		cmd := &input.Commands[i]
		occurrences := 1
		interval := int64(0)
		if cmd.Repeat != nil {
			occurrences = cmd.Repeat.Count
			interval = cmd.Repeat.IntervalMS
		}
		for k := 0; k < occurrences; k++ {
			list = append(list, expanded{
				declarationIndex: i,
				occurrence:        k,
				baseAtMS:          cmd.AtMS + int64(k)*interval,
				commandID:         cmd.ID,
				template:          cmd.Command,
			})
		}
	}
	if len(list) < 1 || len(list) > commandScheduleMaxOccurrences {
		return nil, newCommandScheduleError("commands", fmt.Sprintf("展开后 occurrence 必须在 1..%d 之间", commandScheduleMaxOccurrences))
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].baseAtMS != list[j].baseAtMS {
			return list[i].baseAtMS < list[j].baseAtMS
		}
		if list[i].declarationIndex != list[j].declarationIndex {
			return list[i].declarationIndex < list[j].declarationIndex
		}
		return list[i].occurrence < list[j].occurrence
	})
	plan := &CommandSchedulePlan{
		DurationMS:  input.DurationMS,
		JitterMS:    jitter,
		Occurrences: make([]CommandScheduleOccurrence, len(list)),
	}
	for idx, item := range list {
		plan.Occurrences[idx] = CommandScheduleOccurrence{
			CommandID:             item.commandID,
			Occurrence:            item.occurrence,
			CommandDeclarationIdx: item.declarationIndex,
			BaseAtMS:              item.baseAtMS,
			RawTemplate:           item.template,
		}
	}
	return plan, nil
}

// FillCommandScheduleOccurrencesDeprecated 仅为兼容占位被 FinalizeCommandSchedulePlan 取代。
func FillCommandScheduleOccurrencesDeprecated() {}

// NewCommandScheduleJitterSeed 由 scheduleRunId+botUuid+stepId 确定性产生十进制 jitterSeed。
func NewCommandScheduleJitterSeed(scheduleRunID, botUUID string) string {
	h := sha256.New()
	h.Write([]byte(scheduleRunID))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(botUUID)))
	h.Write([]byte{0})
	h.Write([]byte(commandScheduleDefaultStepID))
	digest := h.Sum(nil)
	value := binary.BigEndian.Uint64(digest[:8])
	return strconv.FormatUint(value, 10)
}

// ComputeCommandOccurrenceActionRunID 计算 actionRunId（UUIDv5）。
func ComputeCommandOccurrenceActionRunID(scheduleRunID, botUUID, stepID, commandID string, occurrence int) (string, error) {
	ns, err := uuid.Parse(scheduleRunID)
	if err != nil {
		return "", fmt.Errorf("scheduleRunId 不是有效 UUID: %w", err)
	}
	payload := fmt.Sprintf("%s\u0000%s\u0000%s\u0000%d", strings.ToLower(botUUID), stepID, commandID, occurrence)
	return uuid.NewSHA1(ns, []byte(payload)).String(), nil
}

// ComputeScheduleCorrelationToken 计算 plan 级 correlationToken。
func ComputeScheduleCorrelationToken(scheduleRunID, botUUID, stepID string) (string, error) {
	if _, err := uuid.Parse(scheduleRunID); err != nil {
		return "", fmt.Errorf("scheduleRunId 不是有效 UUID: %w", err)
	}
	ns, err := uuid.Parse(scheduleRunID)
	if err != nil {
		return "", err
	}
	payload := fmt.Sprintf("correlation\u0000%s\u0000%s", strings.ToLower(botUUID), stepID)
	return uuid.NewSHA1(ns, []byte(payload)).String(), nil
}

// ExpandCommandScheduleTemplate 用白名单变量展开命令模板；遇到未知变量返回错误。
func ExpandCommandScheduleTemplate(template string, ctx CommandScheduleTemplateContext) (string, error) {
	out := strings.Builder{}
	out.Grow(len(template))
	i := 0
	for i < len(template) {
		if strings.HasPrefix(template[i:], "{{") {
			end := strings.Index(template[i+2:], "}}")
			if end < 0 {
				return "", newCommandScheduleError("template", "模板语法无效")
			}
			name := strings.TrimSpace(template[i+2 : i+2+end])
			if _, ok := CommandScheduleVariable[name]; !ok {
				return "", newCommandScheduleError("template", "包含未知模板变量 "+name)
			}
			value, err := renderCommandScheduleVariable(name, ctx)
			if err != nil {
				return "", err
			}
			out.WriteString(value)
			i += 2 + end + 2
			continue
		}
		out.WriteByte(template[i])
		i++
	}
	return out.String(), nil
}

func renderCommandScheduleVariable(name string, ctx CommandScheduleTemplateContext) (string, error) {
	switch name {
	case "botName":
		return ctx.BotName, nil
	case "botOrdinal":
		return ctx.BotOrdinal, nil
	case "cohortKey":
		return ctx.CohortKey, nil
	case "runId":
		if ctx.RunID == "" {
			return "", newCommandScheduleError("template", "runId 未绑定")
		}
		return ctx.RunID, nil
	case "actionRunId":
		if ctx.ActionRunID == "" {
			return "", newCommandScheduleError("template", "actionRunId 未绑定")
		}
		return ctx.ActionRunID, nil
	case "correlationToken":
		if ctx.CorrelationToken == "" {
			return "", newCommandScheduleError("template", "correlationToken 未绑定")
		}
		return ctx.CorrelationToken, nil
	default:
		return "", newCommandScheduleError("template", "包含未知模板变量 "+name)
	}
}

// FinalizeCommandSchedulePlan 把规范化后的 plan 展开为带 actionRunId/jitter 偏移/最终文本的冻结 occurrence plan。
// 调用方负责提供 scheduleRunID、jitterSeed 与模板上下文。skipSet 提供的 {commandId,occurrence} 不进入最终文本与偏移计算。
func FinalizeCommandSchedulePlan(plan *CommandSchedulePlan, scheduleRunID, jitterSeed, stepID, botUUID string, ctx CommandScheduleTemplateContext, skipSet map[string]struct{}) error {
	if plan == nil {
		return errors.New("计划不能为空")
	}
	if _, err := uuid.Parse(scheduleRunID); err != nil {
		return fmt.Errorf("scheduleRunId 不是有效 UUID: %w", err)
	}
	if !commandScheduleJitterSeedPattern.MatchString(jitterSeed) {
		return errors.New("jitterSeed 不是规范十进制字符串")
	}
	if stepID == "" {
		stepID = commandScheduleDefaultStepID
	}
	if botUUID == "" {
		return errors.New("botUUID 不能为空")
	}
	// 校验 skip 集引用必须在 plan 内；非法引用整份计划以 COMMAND_ARGUMENT_INVALID 拒绝。
	for key := range skipSet {
		commandID, occurrence, ok := splitCommandOccurrenceKey(key)
		if !ok {
			return fmt.Errorf("%w: skipOccurrence 引用非法 %s", errCommandArgumentInvalid, key)
		}
		found := false
		for i := range plan.Occurrences {
			if plan.Occurrences[i].CommandID == commandID && plan.Occurrences[i].Occurrence == occurrence {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: skipOccurrence 引用不存在 %s", errCommandArgumentInvalid, key)
		}
	}
	for i := range plan.Occurrences {
		occ := &plan.Occurrences[i]
		if _, skip := skipSet[commandOccurrenceKey(occ.CommandID, occ.Occurrence)]; skip {
			continue
		}
		actionRunID, err := ComputeCommandOccurrenceActionRunID(scheduleRunID, botUUID, stepID, occ.CommandID, occ.Occurrence)
		if err != nil {
			return err
		}
		occ.ActionRunID = actionRunID
		if plan.JitterMS > 0 {
			offset, err := computeCommandScheduleJitterOffset(jitterSeed, botUUID, occ.CommandID, occ.Occurrence, plan.JitterMS)
			if err != nil {
				return err
			}
			occ.JitterOffsetMS = offset
		}
		// 最终文本：CP 端展开白名单变量；空模板或缺绑定以 *CommandScheduleValidationError 抛出。
		ctxCopy := ctx
		ctxCopy.ActionRunID = actionRunID
		rendered, err := ExpandCommandScheduleTemplate(occ.RawTemplate, ctxCopy)
		if err != nil {
			return err
		}
		if err := validateCommandScheduleCommandText("command", rendered); err != nil {
			return err
		}
		occ.Command = rendered
		occ.RawTemplate = ""
	}
	return nil
}

func commandOccurrenceKey(commandID string, occurrence int) string {
	return commandID + "\x00" + strconv.Itoa(occurrence)
}

func splitCommandOccurrenceKey(key string) (string, int, bool) {
	parts := strings.SplitN(key, "\x00", 2)
	if len(parts) != 2 {
		return "", 0, false
	}
	occurrence, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, false
	}
	return parts[0], occurrence, true
}

// computeCommandScheduleJitterOffset 与 api.md §4 固定测试向量保持一致（SHA-256 前 8 字节 unsigned big-endian）。
func computeCommandScheduleJitterOffset(jitterSeed, botUUID, commandID string, occurrence int, jitterMS int64) (int64, error) {
	if jitterMS <= 0 {
		return 0, nil
	}
	h := sha256.New()
	h.Write([]byte(jitterSeed))
	h.Write([]byte{0})
	h.Write([]byte(strings.ToLower(botUUID)))
	h.Write([]byte{0})
	h.Write([]byte(commandID))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(occurrence)))
	digest := h.Sum(nil)
	u := binary.BigEndian.Uint64(digest[:8])
	span := uint64(2*jitterMS + 1)
	r := int64(u%span) - jitterMS
	return r, nil
}

func validateCommandScheduleCommandText(path, value string) error {
	if !utf8.ValidString(value) {
		return newCommandScheduleError(path, "必须是合法 UTF-8")
	}
	for _, r := range value {
		if r >= 0 && r <= 0x1f || r == 0x7f {
			return newCommandScheduleError(path, "禁止包含 U+0000..U+001F/U+007F 控制字符")
		}
	}
	if len(value) < 1 || len(value) > commandScheduleCommandMaxBytes {
		return newCommandScheduleError(path, fmt.Sprintf("长度必须在 1..%d 字节之间", commandScheduleCommandMaxBytes))
	}
	return nil
}

func validateCommandScheduleTemplate(path, template string) error {
	offset := 0
	for offset < len(template) {
		open := strings.Index(template[offset:], "{{")
		if open < 0 {
			return nil
		}
		close := strings.Index(template[offset:], "}}")
		if close >= 0 && close < open {
			return newCommandScheduleError(path, "模板语法无效")
		}
		start := offset + open + 2
		endOffset := strings.Index(template[start:], "}}")
		if endOffset < 0 {
			return newCommandScheduleError(path, "模板语法无效")
		}
		end := start + endOffset
		expression := template[start:end]
		if strings.Contains(expression, "{{") || strings.TrimSpace(expression) == "" {
			return newCommandScheduleError(path, "模板语法无效")
		}
		name := strings.TrimSpace(expression)
		if _, ok := CommandScheduleVariable[name]; !ok {
			return newCommandScheduleError(path, "包含未知模板变量 "+name)
		}
		offset = end + 2
	}
	return nil
}

// SanitizeCommandSchedulePlanForSnapshot 清空 RawTemplate 与冻结后无用的中间字段。
func SanitizeCommandSchedulePlanForSnapshot(plan *CommandSchedulePlan) *CommandSchedulePlan {
	if plan == nil {
		return nil
	}
	cloned := &CommandSchedulePlan{
		DurationMS: plan.DurationMS,
		JitterMS:   plan.JitterMS,
	}
	cloned.Occurrences = make([]CommandScheduleOccurrence, len(plan.Occurrences))
	for i, occ := range plan.Occurrences {
		cloned.Occurrences[i] = CommandScheduleOccurrence{
			CommandID:             occ.CommandID,
			Occurrence:            occ.Occurrence,
			CommandDeclarationIdx: occ.CommandDeclarationIdx,
			BaseAtMS:              occ.BaseAtMS,
			JitterOffsetMS:        occ.JitterOffsetMS,
			ActionRunID:           occ.ActionRunID,
			Command:               occ.Command,
		}
	}
	return cloned
}

// EncodeCommandScheduleJSONSize 计算规范化 JSON 字节长度（用于 ≤256KiB 校验）。
func EncodeCommandScheduleJSONSize(plan *CommandSchedulePlan) (int, error) {
	data, err := json.Marshal(SanitizeCommandSchedulePlanForSnapshot(plan))
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

// EncodeCommandScheduleSnapshotHex 返回规范化 JSON 的 SHA-256 摘要十六进制，可作为快照/校验指纹。
func EncodeCommandScheduleSnapshotHex(plan *CommandSchedulePlan) (string, error) {
	data, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// IsCommandScheduleJitterSeedValid 暴露给测试使用。
func IsCommandScheduleJitterSeedValid(value string) bool {
	return commandScheduleJitterSeedPattern.MatchString(value)
}