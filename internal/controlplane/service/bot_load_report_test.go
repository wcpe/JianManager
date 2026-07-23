package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestBotLoadReportService_BuildJSONAndCSV(t *testing.T) {
	db := openRunIntentDB(t)
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

	// 非终态拒绝
	pending := seedV2Session(t, db, model.BotLoadRunRunning)
	_, err = svc.BuildJSON(pending.ID)
	require.ErrorIs(t, err, ErrBotLoadReportNotReady)
}
