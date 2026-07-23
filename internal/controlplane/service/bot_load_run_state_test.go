package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestTransitionRunState_LegalPaths(t *testing.T) {
	// pending → preflighting → ready → starting → running
	to, unchanged, err := TransitionRunState(model.BotLoadRunPending, BotLoadIntentPreflight)
	require.NoError(t, err)
	require.False(t, unchanged)
	require.Equal(t, model.BotLoadRunPreflighting, to)

	to, _, err = TransitionRunState(model.BotLoadRunPreflighting, BotLoadIntentReady)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunReady, to)

	to, _, err = TransitionRunState(model.BotLoadRunReady, BotLoadIntentStart)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunStarting, to)

	to, _, err = TransitionRunState(model.BotLoadRunStarting, BotLoadIntentRunning)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunRunning, to)

	// running ↔ degraded
	to, _, err = TransitionRunState(model.BotLoadRunRunning, BotLoadIntentDegrade)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunDegraded, to)

	to, _, err = TransitionRunState(model.BotLoadRunDegraded, BotLoadIntentRecover)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunRunning, to)

	// stop → complete
	to, _, err = TransitionRunState(model.BotLoadRunRunning, BotLoadIntentStop)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunStopping, to)

	to, unchanged, err = TransitionRunState(model.BotLoadRunStopping, BotLoadIntentStop)
	require.NoError(t, err)
	require.True(t, unchanged)
	require.Equal(t, model.BotLoadRunStopping, to)

	to, _, err = TransitionRunState(model.BotLoadRunStopping, BotLoadIntentComplete)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunCompleted, to)
}

func TestTransitionRunState_CancelPaths(t *testing.T) {
	// 早期非终态直接 cancelling
	for _, from := range []model.BotLoadRunState{
		model.BotLoadRunPending, model.BotLoadRunPreflighting, model.BotLoadRunReady,
		model.BotLoadRunStarting, model.BotLoadRunRunning, model.BotLoadRunDegraded, model.BotLoadRunStopping,
	} {
		to, unchanged, err := TransitionRunState(from, BotLoadIntentCancel)
		require.NoError(t, err, "from=%s", from)
		require.False(t, unchanged)
		require.Equal(t, model.BotLoadRunCancelling, to)
	}

	// cancelling 幂等
	to, unchanged, err := TransitionRunState(model.BotLoadRunCancelling, BotLoadIntentCancel)
	require.NoError(t, err)
	require.True(t, unchanged)
	require.Equal(t, model.BotLoadRunCancelling, to)

	to, _, err = TransitionRunState(model.BotLoadRunCancelling, BotLoadIntentCancelled)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunCancelled, to)
}

func TestTransitionRunState_TerminalRejectsStopCancel(t *testing.T) {
	for _, from := range []model.BotLoadRunState{
		model.BotLoadRunCompleted, model.BotLoadRunFailed, model.BotLoadRunCancelled,
	} {
		_, _, err := TransitionRunState(from, BotLoadIntentStop)
		require.ErrorIs(t, err, ErrBotLoadInvalidState, "from=%s stop", from)
		_, _, err = TransitionRunState(from, BotLoadIntentCancel)
		require.ErrorIs(t, err, ErrBotLoadInvalidState, "from=%s cancel", from)
	}
}

func TestTransitionRunState_StartOnlyFromReady(t *testing.T) {
	_, _, err := TransitionRunState(model.BotLoadRunPending, BotLoadIntentStart)
	require.ErrorIs(t, err, ErrBotLoadInvalidState)
	_, _, err = TransitionRunState(model.BotLoadRunRunning, BotLoadIntentStart)
	require.ErrorIs(t, err, ErrBotLoadInvalidState)
}

func TestTransitionRunState_FailFromAnyNonTerminal(t *testing.T) {
	to, _, err := TransitionRunState(model.BotLoadRunRunning, BotLoadIntentFail)
	require.NoError(t, err)
	require.Equal(t, model.BotLoadRunFailed, to)
}

func TestMapRunStateToLegacyStatus(t *testing.T) {
	require.Equal(t, model.BotStressSessionPending, MapRunStateToLegacyStatus(model.BotLoadRunReady))
	require.Equal(t, model.BotStressSessionPending, MapRunStateToLegacyStatus(model.BotLoadRunStarting))
	require.Equal(t, model.BotStressSessionRunning, MapRunStateToLegacyStatus(model.BotLoadRunRunning))
	require.Equal(t, model.BotStressSessionRunning, MapRunStateToLegacyStatus(model.BotLoadRunStopping))
	require.Equal(t, model.BotStressSessionStopped, MapRunStateToLegacyStatus(model.BotLoadRunCompleted))
	require.Equal(t, model.BotStressSessionStopped, MapRunStateToLegacyStatus(model.BotLoadRunCancelled))
	require.Equal(t, model.BotStressSessionError, MapRunStateToLegacyStatus(model.BotLoadRunFailed))
}
