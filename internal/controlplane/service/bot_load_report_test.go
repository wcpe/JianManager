package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestBotLoadReportService_BuildJSONAndCSV(t *testing.T) {
	db := openRunIntentDB(t)
	require.NoError(t, db.AutoMigrate(&model.BotLoadMetricSample{}))
	// 终态 completed
	sess := seedV2Session(t, db, model.BotLoadRunCompleted)
	svc := NewBotLoadReportService(db)

	rep, err := svc.BuildJSON(sess.ID)
	require.NoError(t, err)
	require.Equal(t, sess.ID, rep.RunID)
	require.Equal(t, string(model.BotLoadRunCompleted), rep.RunState)
	require.Contains(t, rep.Disclaimer, "bot.chat")

	csvBytes, err := svc.BuildCSV(sess.ID)
	require.NoError(t, err)
	require.True(t, len(csvBytes) > 10)
	require.Contains(t, string(csvBytes), "runId")
	require.Contains(t, string(csvBytes), "capacityPeakBots")

	// 非终态拒绝
	pending := seedV2Session(t, db, model.BotLoadRunRunning)
	_, err = svc.BuildJSON(pending.ID)
	require.ErrorIs(t, err, ErrBotLoadReportNotReady)
}

func TestBuildBotLoadCapacityReport_AggregatesStagesAndKeepsScaleHonest(t *testing.T) {
	passed := model.BotLoadVerdictPassed
	sess := model.BotStressSession{Verdict: &passed, MaxStableBots: intPointer(5)}
	rows := append(capacityFixtureRows(0, 0, 100), capacityFixtureRows(1, 5, 200)...)
	report := buildBotLoadCapacityReport(sess, rows)

	require.Len(t, report.Stages, 2)
	metric := report.Stages[0].Target["processRssBytes"]
	require.Equal(t, 130.0, *metric.Baseline)
	require.Equal(t, 140.0, *metric.Peak)
	require.Equal(t, 140.0, *metric.P95)
	require.Equal(t, 10.0, *metric.Delta)
	require.Equal(t, 10.0, *metric.SlopePerBot)
	require.False(t, report.TestedScale.ClaimedAs500)
	require.Equal(t, int64(300), *report.Recommended.TargetProcessRssBytes)
	require.Equal(t, int64(64*1024*1024*1024), *report.TargetHostMemory.TotalBytes)
	require.True(t, *report.TargetHostMemory.WithinReserve)

	insufficient := buildBotLoadCapacityReport(sess, capacityFixtureRows(2, 10, 300)[:3])
	require.Nil(t, insufficient.Stages[0].Target["processRssBytes"].Baseline)
	require.Contains(t, insufficient.Stages[0].Unavailable, "processRssBytes:INSUFFICIENT_SAMPLES")
}

func TestBuildBotLoadCapacityReport_DoesNotCalculateSlopeWithoutBotGrowth(t *testing.T) {
	rows := capacityFixtureRows(0, 0, 100)
	for index := range rows {
		rows[index].CountsJSON = capacityFixtureJSON(map[string]any{"connected": 1})
	}
	report := buildBotLoadCapacityReport(model.BotStressSession{}, rows)
	metric := report.Stages[0].Target["processRssBytes"]
	require.Nil(t, metric.SlopePerBot)
	require.Contains(t, report.Stages[0].Unavailable, "processRssBytes:DELTA_BOTS_NOT_POSITIVE")
}

func TestBuildBotLoadCapacityReport_ExecutorSlopeUsesExecutorBots(t *testing.T) {
	rows := capacityFixtureRows(0, 0, 100)
	for index := range rows {
		rows[index].ExecutorJSON = capacityFixtureJSON([]map[string]any{{"nodeId": 1, "activeBots": 1}})
	}
	report := buildBotLoadCapacityReport(model.BotStressSession{}, rows)
	require.NotNil(t, report.Stages[0].Target["processRssBytes"].SlopePerBot)
	require.Nil(t, report.Stages[0].Executors[0].Metrics["activeBots"].SlopePerBot)
	require.Contains(t, report.Stages[0].Executors[0].Unavailable, "activeBots:DELTA_BOTS_NOT_POSITIVE")
}

func TestBuildBotLoadCapacityReport_HidesMaxStableWithoutPassedReserve(t *testing.T) {
	failed := model.BotLoadVerdictFailed
	sess := model.BotStressSession{Verdict: &failed, MaxStableBots: intPointer(5)}
	report := buildBotLoadCapacityReport(sess, capacityFixtureRows(0, 0, 100))
	require.Nil(t, report.TestedScale.MaxStableBots)

	passed := model.BotLoadVerdictPassed
	rows := capacityFixtureRows(0, 0, 100)
	for index := range rows {
		rows[index].TargetLegacyJSON = stringPointer(capacityFixtureJSON(map[string]any{"targetResource": map[string]any{
			"hostMemUsedBytes": 60 * 1024 * 1024 * 1024, "hostMemTotalBytes": 64 * 1024 * 1024 * 1024,
		}}))
	}
	report = buildBotLoadCapacityReport(model.BotStressSession{Verdict: &passed, MaxStableBots: intPointer(5)}, rows)
	require.Nil(t, report.TestedScale.MaxStableBots)
}

func capacityFixtureRows(stage, offset int, rss int) []model.BotLoadMetricSample {
	rows := make([]model.BotLoadMetricSample, 0, 5)
	for index := 0; index < 5; index++ {
		rows = append(rows, model.BotLoadMetricSample{
			SampledAt: time.Date(2026, 7, 27, 0, offset+index, 0, 0, time.UTC), StageIndex: stage,
			CountsJSON:       capacityFixtureJSON(map[string]any{"connected": index + 1}),
			ExecutorJSON:     capacityFixtureJSON([]map[string]any{{"nodeId": 1, "activeBots": index + 1, "botWorkerRssBytes": 10 + index, "eventLoopP95Ms": 2.0, "nodeMemUsedBytes": 20 + index, "nodeMemTotalBytes": 30, "nodeCpuPercent": 3.0, "workerProcessRssBytes": 4 + index, "health": "ready"}}),
			TargetLegacyJSON: stringPointer(capacityFixtureJSON(map[string]any{"targetResource": map[string]any{"processRssBytes": rss + index*10, "heapUsedBytes": 1, "heapMaxBytes": 2, "cpuPercent": 3.0, "uptimeSeconds": 4, "hostMemUsedBytes": 40 * 1024 * 1024 * 1024, "hostMemTotalBytes": 64 * 1024 * 1024 * 1024, "tps": 20.0, "mspt": 50.0, "onlinePlayers": 0}})),
		})
	}
	return rows
}

func capacityFixtureJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func intPointer(value int) *int { return &value }

func stringPointer(value string) *string { return &value }
