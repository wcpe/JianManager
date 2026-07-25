package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestBotLoadMetricSampler_ProjectStreamSnapshot(t *testing.T) {
	db := openMetricSamplerDB(t)
	sess, _ := seedMetricSession(t, db)
	now := time.Date(2026, 7, 25, 6, 0, 0, 0, time.UTC)
	sampler := NewBotLoadMetricSampler(db, botFleetTestClock{now: now})
	require.NoError(t, sampler.SampleSession(context.Background(), sess.ID))

	snap, err := sampler.ProjectStreamSnapshot(context.Background(), sess.ID, nil)
	require.NoError(t, err)
	require.Equal(t, string(model.BotLoadRunRunning), snap.RunState)
	require.False(t, snap.Terminal)
	require.Equal(t, int64(1), snap.LoadCounts["connected"])
	require.Equal(t, int64(1), snap.LoadCounts["connecting"])
	require.Equal(t, int64(1), snap.CommandTotal["sent"])
	require.NotNil(t, snap.Metric)
	require.Equal(t, float64(sess.ID), float64(snap.Run["id"].(uint)))

	// 终态
	completed := model.BotLoadRunCompleted
	verdict := model.BotLoadVerdictPassed
	require.NoError(t, db.Model(sess).Updates(map[string]any{
		"run_state": completed, "verdict": verdict, "status": model.BotStressSessionStopped,
	}).Error)
	snap2, err := sampler.ProjectStreamSnapshot(context.Background(), sess.ID, nil)
	require.NoError(t, err)
	require.True(t, snap2.Terminal)
	require.True(t, snap2.ReportReady)
	require.Equal(t, "completed", snap2.RunState)
}
