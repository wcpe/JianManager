package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/proto/workerpb"
)

func TestManagedRuntimeUpdates_ClearsUnavailableBotValues(t *testing.T) {
	rss := int64(123)
	updates := managedRuntimeUpdates(&workerpb.ManagedRuntimeSnapshot{
		WorkerProcessRssBytes: &rss,
		BotAvailable:          false,
		BotUnavailableReason:  "未启动",
	})
	require.Equal(t, &rss, updates["worker_process_rss_bytes"])
	require.Nil(t, updates["bot_worker_rss_bytes"])
	require.False(t, updates["bot_available"].(bool))
	require.Equal(t, "未启动", updates["bot_unavailable_reason"])
}

func TestManagedRuntimeUpdates_OldWorkerClearsCurrentSnapshot(t *testing.T) {
	updates := managedRuntimeUpdates(nil)
	require.Nil(t, updates["worker_process_rss_bytes"])
	require.Nil(t, updates["bot_active_count"])
	require.False(t, updates["bot_available"].(bool))
	require.Equal(t, "Worker 未上报受管运行时快照", updates["bot_unavailable_reason"])
}
