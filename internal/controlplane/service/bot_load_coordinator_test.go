package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestBotLoadRunCoordinator_RegisterAndCancel(t *testing.T) {
	c := NewBotLoadRunCoordinator(nil)
	ctx, err := c.RegisterStarting(1)
	require.NoError(t, err)
	require.NotNil(t, ctx)
	require.True(t, c.IsActive(1))
	require.Equal(t, 1, c.ActiveCount())

	_, err = c.RegisterStarting(1)
	require.ErrorIs(t, err, ErrBotLoadInvalidState)

	c.Cancel(1)
	require.False(t, c.IsActive(1))
	require.Equal(t, 0, c.ActiveCount())
	// 上下文应已取消
	select {
	case <-ctx.Done():
	default:
		t.Fatal("期望 ctx 已取消")
	}
}

func TestBotLoadRunCoordinator_RequestStopUsesIntent(t *testing.T) {
	db := openRunIntentDB(t)
	intents := NewBotLoadRunIntentService(db)
	sess := seedV2Session(t, db, model.BotLoadRunRunning)
	coord := NewBotLoadRunCoordinator(intents)
	_, err := coord.RegisterStarting(sess.ID)
	require.NoError(t, err)

	require.NoError(t, coord.RequestStop(context.Background(), sess.ID, "test_stop"))
	require.False(t, coord.IsActive(sess.ID))

	reloaded, err := intents.LoadV2Session(context.Background(), sess.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded.RunState)
	require.Equal(t, model.BotLoadRunStopping, *reloaded.RunState)
}
