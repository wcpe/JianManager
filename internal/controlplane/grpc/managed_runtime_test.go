package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestManagedRuntimeUpdates_ClearsUnavailableBotValues(t *testing.T) {
	rss := int64(123)
	capacity := int32(50)
	updates := managedRuntimeUpdates(&workerpb.ManagedRuntimeSnapshot{
		WorkerProcessRssBytes: &rss,
		BotCapacityMax:       &capacity,
		BotAvailable:          false,
		BotUnavailableReason:  "未启动",
	})
	require.Equal(t, &rss, updates["worker_process_rss_bytes"])
	require.Nil(t, updates["bot_worker_rss_bytes"])
	require.Nil(t, updates["bot_capacity_max"])
	require.False(t, updates["bot_available"].(bool))
	require.Equal(t, "未启动", updates["bot_unavailable_reason"])
	require.Equal(t, "未启动", updates["bot_capacity_unavailable_reason"])
}

func TestManagedRuntimeUpdates_PersistsBotCapacityWhenAvailable(t *testing.T) {
	capacity := int32(50)
	updates := managedRuntimeUpdates(&workerpb.ManagedRuntimeSnapshot{
		BotAvailable:    true,
		BotCapacityMax:  &capacity,
		ObservedAtUnixMs: 1,
	})
	require.Equal(t, &capacity, updates["bot_capacity_max"])
	require.Equal(t, "", updates["bot_capacity_unavailable_reason"])
}

func TestManagedRuntimeUpdates_OldWorkerClearsCurrentSnapshot(t *testing.T) {
	updates := managedRuntimeUpdates(nil)
	require.Nil(t, updates["worker_process_rss_bytes"])
	require.Nil(t, updates["bot_active_count"])
	require.Nil(t, updates["bot_capacity_max"])
	require.False(t, updates["bot_available"].(bool))
	require.Equal(t, "Worker 未上报受管运行时快照", updates["bot_unavailable_reason"])
	require.Equal(t, "Worker 未上报受管运行时快照", updates["bot_capacity_unavailable_reason"])
}
