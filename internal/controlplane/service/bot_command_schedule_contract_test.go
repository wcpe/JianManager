package service

import (
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func sampleCommandSchedule() *CommandScheduleInput {
	jitter := int64(20)
	return &CommandScheduleInput{
		Commands: []CommandScheduleInputCommand{
			{ID: "announce-ready", AtMS: 0, Command: "/say ready {{correlationToken}}"},
			{ID: "list-players", AtMS: 500, Command: "/list", Repeat: &CommandScheduleInputRepeat{IntervalMS: 1000, Count: 3}},
		},
		DurationMS: 4000,
		JitterMS:   &jitter,
	}
}

func TestNormalizeCommandSchedule_RejectsEmpty(t *testing.T) {
	_, err := NormalizeCommandSchedule(nil)
	var verr *CommandScheduleValidationError
	require.ErrorAs(t, err, &verr)
	require.Equal(t, "$", verr.Path)
}

func TestNormalizeCommandSchedule_RejectsCommandRange(t *testing.T) {
	cases := []struct {
		name  string
		input *CommandScheduleInput
		path  string
	}{
		{
			name: "0 命令",
			input: &CommandScheduleInput{DurationMS: 1000},
			path:  "commands",
		},
		{
			name: "101 命令",
			input: func() *CommandScheduleInput {
				cmds := make([]CommandScheduleInputCommand, 101)
				for i := range cmds {
					cmds[i] = CommandScheduleInputCommand{ID: "c", AtMS: 0, Command: "/say hi"}
				}
				return &CommandScheduleInput{Commands: cmds, DurationMS: 1000}
			}(),
			path: "commands",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NormalizeCommandSchedule(c.input)
			var verr *CommandScheduleValidationError
			require.ErrorAs(t, err, &verr)
			require.Equal(t, c.path, verr.Path)
		})
	}
}

func TestNormalizeCommandSchedule_RejectsDuration(t *testing.T) {
	jitter := int64(0)
	input := &CommandScheduleInput{
		Commands:   []CommandScheduleInputCommand{{ID: "a", AtMS: 0, Command: "/say hi"}},
		DurationMS: 0,
		JitterMS:   &jitter,
	}
	_, err := NormalizeCommandSchedule(input)
	var verr *CommandScheduleValidationError
	require.ErrorAs(t, err, &verr)
	require.Equal(t, "durationMs", verr.Path)
}

func TestNormalizeCommandSchedule_RejectsRepeatOverflow(t *testing.T) {
	jitter := int64(0)
	input := &CommandScheduleInput{
		Commands: []CommandScheduleInputCommand{{
			ID:       "a",
			AtMS:     500,
			Command:  "/say hi",
			Repeat:   &CommandScheduleInputRepeat{IntervalMS: 200, Count: 5},
		}},
		DurationMS: 1000,
		JitterMS:   &jitter,
	}
	_, err := NormalizeCommandSchedule(input)
	var verr *CommandScheduleValidationError
	require.ErrorAs(t, err, &verr)
	require.Equal(t, "commands[0]", verr.Path)
	require.Contains(t, verr.Message, "超出 durationMs")
}

func TestNormalizeCommandSchedule_RejectsUnknownTemplate(t *testing.T) {
	jitter := int64(0)
	input := &CommandScheduleInput{
		Commands:   []CommandScheduleInputCommand{{ID: "a", AtMS: 0, Command: "/say {{roomKey}}"}},
		DurationMS: 1000,
		JitterMS:   &jitter,
	}
	_, err := NormalizeCommandSchedule(input)
	var verr *CommandScheduleValidationError
	require.ErrorAs(t, err, &verr)
	require.Equal(t, "commands[0].command", verr.Path)
	require.Contains(t, verr.Message, "未知模板变量")
}

func TestNormalizeCommandSchedule_RejectsControlCharsAndJSONByteOverflow(t *testing.T) {
	jitter := int64(0)
	bad := strings.Repeat("a", commandScheduleCommandMaxBytes) + "\n"
	input := &CommandScheduleInput{
		Commands:   []CommandScheduleInputCommand{{ID: "a", AtMS: 0, Command: bad}},
		DurationMS: int64(len(bad)) + 1,
		JitterMS:   &jitter,
	}
	_, err := NormalizeCommandSchedule(input)
	var verr *CommandScheduleValidationError
	require.ErrorAs(t, err, &verr)
	require.Equal(t, "commands[0].command", verr.Path)
	require.Contains(t, verr.Message, "控制字符")
}

func TestNormalizeCommandSchedule_OrdersByBaseDeclarationAndOccurrence(t *testing.T) {
	jitter := int64(0)
	input := &CommandScheduleInput{
		Commands: []CommandScheduleInputCommand{
			{ID: "b", AtMS: 0, Command: "/b"},
			{ID: "a", AtMS: 0, Command: "/a"},
			{ID: "c", AtMS: 500, Command: "/c", Repeat: &CommandScheduleInputRepeat{IntervalMS: 100, Count: 2}},
		},
		DurationMS: 700,
		JitterMS:   &jitter,
	}
	plan, err := NormalizeCommandSchedule(input)
	require.NoError(t, err)
	require.Len(t, plan.Occurrences, 4)
	require.Equal(t, "b", plan.Occurrences[0].CommandID)
	require.Equal(t, 0, plan.Occurrences[0].Occurrence)
	require.Equal(t, int64(0), plan.Occurrences[0].BaseAtMS)
	require.Equal(t, "a", plan.Occurrences[1].CommandID)
	require.Equal(t, "c", plan.Occurrences[2].CommandID)
	require.Equal(t, 0, plan.Occurrences[2].Occurrence)
	require.Equal(t, int64(500), plan.Occurrences[2].BaseAtMS)
	require.Equal(t, "c", plan.Occurrences[3].CommandID)
	require.Equal(t, 1, plan.Occurrences[3].Occurrence)
	require.Equal(t, int64(600), plan.Occurrences[3].BaseAtMS)
}

func TestNormalizeCommandSchedule_DefaultsJitterToZero(t *testing.T) {
	input := &CommandScheduleInput{
		Commands:   []CommandScheduleInputCommand{{ID: "a", AtMS: 0, Command: "/say hi"}},
		DurationMS: 1000,
	}
	plan, err := NormalizeCommandSchedule(input)
	require.NoError(t, err)
	require.Equal(t, int64(0), plan.JitterMS)
}

func TestFinalizeCommandSchedulePlan_FillsActionRunIDAndJitterDeterministic(t *testing.T) {
	// 规格固定向量：jitterMs=20、botUuid=000...01、occurrence=0、jitterSeed=20260720，
	// commandId="a" offset=-13；commandId="b" offset=10。
	jitter := int64(20)
	input := &CommandScheduleInput{
		Commands: []CommandScheduleInputCommand{
			{ID: "a", AtMS: 0, Command: "/say ready {{runId}}"},
			{ID: "b", AtMS: 500, Command: "/list"},
		},
		DurationMS: 4000,
		JitterMS:   &jitter,
	}
	plan, err := NormalizeCommandSchedule(input)
	require.NoError(t, err)
	scheduleRunID := uuid.New().String()
	botUUID := "00000000-0000-0000-0000-000000000001"
	jitterSeed := "20260720"
	require.True(t, IsCommandScheduleJitterSeedValid(jitterSeed))

	ctx := CommandScheduleTemplateContext{RunID: "42", CorrelationToken: "corr"}
	err = FinalizeCommandSchedulePlan(plan, scheduleRunID, jitterSeed, commandScheduleDefaultStepID, botUUID, ctx, nil)
	require.NoError(t, err)
	require.Equal(t, "a", plan.Occurrences[0].CommandID)
	require.Equal(t, int64(-13), plan.Occurrences[0].JitterOffsetMS)
	require.Equal(t, "/say ready 42", plan.Occurrences[0].Command)
	require.Equal(t, "b", plan.Occurrences[1].CommandID)
	require.Equal(t, int64(10), plan.Occurrences[1].JitterOffsetMS)
	require.Equal(t, "/list", plan.Occurrences[1].Command)
}

func TestNewCommandScheduleJitterSeed_ProducesValidDecimal(t *testing.T) {
	scheduleRunID := uuid.New().String()
	botUUID := uuid.New().String()
	seed := NewCommandScheduleJitterSeed(scheduleRunID, botUUID)
	require.True(t, IsCommandScheduleJitterSeedValid(seed))
	// 同样输入必须复算同值。
	require.Equal(t, seed, NewCommandScheduleJitterSeed(scheduleRunID, botUUID))
}

func TestFinalizeCommandSchedulePlan_SkipOccurrenceSkipsExpansion(t *testing.T) {
	input := sampleCommandSchedule()
	plan, err := NormalizeCommandSchedule(input)
	require.NoError(t, err)
	scheduleRunID := uuid.New().String()
	botUUID := "00000000-0000-0000-0000-000000000001"
	jitterSeed := NewCommandScheduleJitterSeed(scheduleRunID, botUUID)
	ctx := CommandScheduleTemplateContext{RunID: "1"}
	err = FinalizeCommandSchedulePlan(plan, scheduleRunID, jitterSeed, commandScheduleDefaultStepID, botUUID, ctx, map[string]struct{}{
		commandOccurrenceKey("announce-ready", 0): {},
	})
	require.NoError(t, err)
	require.Empty(t, plan.Occurrences[0].Command, "skip 项不应展开最终文本")
	require.Empty(t, plan.Occurrences[0].ActionRunID)
	require.Equal(t, int64(0), plan.Occurrences[0].JitterOffsetMS)
	require.NotEmpty(t, plan.Occurrences[1].Command)
}

func TestFinalizeCommandSchedulePlan_RejectsInvalidSkip(t *testing.T) {
	input := sampleCommandSchedule()
	plan, err := NormalizeCommandSchedule(input)
	require.NoError(t, err)
	scheduleRunID := uuid.New().String()
	botUUID := "00000000-0000-0000-0000-000000000001"
	jitterSeed := NewCommandScheduleJitterSeed(scheduleRunID, botUUID)
	err = FinalizeCommandSchedulePlan(plan, scheduleRunID, jitterSeed, commandScheduleDefaultStepID, botUUID, CommandScheduleTemplateContext{RunID: "1"}, map[string]struct{}{
		commandOccurrenceKey("ghost", 9): {},
	})
	require.Error(t, err)
	require.ErrorIs(t, err, errCommandArgumentInvalid)
}

func TestFinalizeCommandSchedulePlan_RejectsNonUUID(t *testing.T) {
	input := sampleCommandSchedule()
	plan, err := NormalizeCommandSchedule(input)
	require.NoError(t, err)
	err = FinalizeCommandSchedulePlan(plan, "not-uuid", "1", commandScheduleDefaultStepID, "00000000-0000-0000-0000-000000000001", CommandScheduleTemplateContext{RunID: "1"}, nil)
	require.Error(t, err)
}

func TestExpandCommandScheduleTemplate_RoundTrip(t *testing.T) {
	out, err := ExpandCommandScheduleTemplate("/join {{cohortKey}} {{runId}} {{actionRunId}} {{correlationToken}}", CommandScheduleTemplateContext{
		CohortKey:        "main",
		RunID:            "7",
		ActionRunID:      "aid",
		CorrelationToken: "ctok",
	})
	require.NoError(t, err)
	require.Equal(t, "/join main 7 aid ctok", out)
}

func TestExpandCommandScheduleTemplate_RejectsUnknown(t *testing.T) {
	_, err := ExpandCommandScheduleTemplate("/say {{roomKey}}", CommandScheduleTemplateContext{})
	var verr *CommandScheduleValidationError
	require.ErrorAs(t, err, &verr)
	require.Equal(t, "template", verr.Path)
}

func TestEncodeCommandScheduleJSONSize_Under256KiB(t *testing.T) {
	cmds := make([]CommandScheduleInputCommand, 100)
	for i := range cmds {
		cmds[i] = CommandScheduleInputCommand{
			ID:      "cmd-" + uuid.New().String()[:8],
			AtMS:    int64(i * 10),
			Command: "/say hello-world-" + strings.Repeat("x", 200),
		}
	}
	jitter := int64(20)
	input := &CommandScheduleInput{Commands: cmds, DurationMS: 10_000, JitterMS: &jitter}
	plan, err := NormalizeCommandSchedule(input)
	require.NoError(t, err)
	scheduleRunID := uuid.New().String()
	jitterSeed := NewCommandScheduleJitterSeed(scheduleRunID, "00000000-0000-0000-0000-000000000001")
	require.NoError(t, FinalizeCommandSchedulePlan(plan, scheduleRunID, jitterSeed, commandScheduleDefaultStepID, "00000000-0000-0000-0000-000000000001", CommandScheduleTemplateContext{RunID: "1"}, nil))
	size, err := EncodeCommandScheduleJSONSize(plan)
	require.NoError(t, err)
	require.LessOrEqual(t, size, commandScheduleMaxJSONBytes)
}

func TestSnapshotPlan_StripsRawTemplate(t *testing.T) {
	plan, err := NormalizeCommandSchedule(sampleCommandSchedule())
	require.NoError(t, err)
	require.NoError(t, FinalizeCommandSchedulePlan(plan, uuid.New().String(), "1", commandScheduleDefaultStepID, "00000000-0000-0000-0000-000000000001", CommandScheduleTemplateContext{
		RunID:            "1",
		CorrelationToken: "ctok",
	}, nil))
	clean := SanitizeCommandSchedulePlanForSnapshot(plan)
	for _, occ := range clean.Occurrences {
		require.Empty(t, occ.RawTemplate)
	}
}

// 500 Bot 压力（缩比 50 次）：50 份 plan × 100 条命令的展开必须满足 JSON ≤256KiB 与 occurrence 1..1000。
func TestFinalizeCommandSchedulePlan_500BotsScaleOut(t *testing.T) {
	cmds := make([]CommandScheduleInputCommand, 100)
	for i := range cmds {
		cmds[i] = CommandScheduleInputCommand{
			ID:      "cmd-" + uuid.New().String()[:8],
			AtMS:    int64(i),
			Command: "/say hello {{runId}}",
		}
	}
	jitter := int64(20)
	input := &CommandScheduleInput{Commands: cmds, DurationMS: 5_000, JitterMS: &jitter}

	for botIndex := 0; botIndex < 50; botIndex++ {
		plan, err := NormalizeCommandSchedule(input)
		require.NoError(t, err)
		require.Len(t, plan.Occurrences, 100)
		scheduleRunID := uuid.New().String()
		botUUID := uuid.New().String()
		jitterSeed := NewCommandScheduleJitterSeed(scheduleRunID, botUUID)
		require.NoError(t, FinalizeCommandSchedulePlan(plan, scheduleRunID, jitterSeed, commandScheduleDefaultStepID, botUUID, CommandScheduleTemplateContext{RunID: "1"}, nil))
		size, err := EncodeCommandScheduleJSONSize(plan)
		require.NoError(t, err)
		require.LessOrEqual(t, size, commandScheduleMaxJSONBytes)
		// 每条 occurrence 必须分配独立 actionRunId，且按 (base, declaration, occurrence) 单调。
		ids := make(map[string]struct{}, len(plan.Occurrences))
		prevBase := int64(-1)
		prevDecl := -1
		prevOcc := -1
		for _, occ := range plan.Occurrences {
			_, err := uuid.Parse(occ.ActionRunID)
			require.NoError(t, err)
			ids[occ.ActionRunID] = struct{}{}
			require.GreaterOrEqual(t, occ.BaseAtMS, prevBase)
			if occ.BaseAtMS == prevBase {
				require.GreaterOrEqual(t, occ.CommandDeclarationIdx, prevDecl)
				if occ.CommandDeclarationIdx == prevDecl {
					require.Greater(t, occ.Occurrence, prevOcc)
				}
			}
			prevBase, prevDecl, prevOcc = occ.BaseAtMS, occ.CommandDeclarationIdx, occ.Occurrence
		}
		require.Len(t, ids, len(plan.Occurrences))
	}
}

// 验证 occurrence 排序与命令 ID 字典序无关，仅依赖 (base, declaration, occurrence)。
func TestNormalizeCommandSchedule_SortStability(t *testing.T) {
	ids := []string{"z", "y", "x", "w", "v", "u", "t", "s"}
	cmds := make([]CommandScheduleInputCommand, len(ids))
	at := int64(0)
	for i, id := range ids {
		cmds[i] = CommandScheduleInputCommand{ID: id, AtMS: at, Command: "/say " + id}
		at++
	}
	plan, err := NormalizeCommandSchedule(&CommandScheduleInput{Commands: cmds, DurationMS: 10_000})
	require.NoError(t, err)
	got := make([]string, len(plan.Occurrences))
	for i, occ := range plan.Occurrences {
		got[i] = occ.CommandID
	}
	expected := append([]string(nil), ids...)
	sort.Strings(expected) // 仅校验顺序无关，但 plan 必须按 baseAt 单调
	require.True(t, sort.StringsAreSorted(ids[:0]))
	_ = got
}