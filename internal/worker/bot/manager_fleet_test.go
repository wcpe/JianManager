package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagerApplyBotBatchWaitsForMatchingResult(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	mgr := NewManager(ManagerConfig{RequestTimeout: time.Second})
	mgr.running = true
	mgr.stdin = json.NewEncoder(writer)

	resultCh := make(chan *BotWorkerEvent, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := mgr.ApplyBotBatch(context.Background(), "request-1", "batch-1", "key-1", []BotConfig{{ID: "bot-1"}})
		resultCh <- result
		errCh <- err
	}()

	var command CreateBotsCommand
	require.NoError(t, json.NewDecoder(reader).Decode(&command))
	require.Equal(t, "request-1", command.RequestID)
	require.Equal(t, "batch-1", command.BatchID)
	require.Equal(t, "key-1", command.IdempotencyKey)

	mgr.handleEvent(&BotWorkerEvent{
		Evt:            "batch-result",
		RequestID:      "request-1",
		BatchID:        "batch-1",
		IdempotencyKey: "key-1",
		Results:        []BotItemResult{{BotID: "bot-1", Accepted: true, Status: "connecting"}},
	})

	require.NoError(t, <-errCh)
	result := <-resultCh
	require.Len(t, result.Results, 1)
	require.True(t, result.Results[0].Accepted)
	require.Zero(t, mgr.PendingRequestCount())
}

func TestManagerPendingRequestTimeoutRemovesWaiter(t *testing.T) {
	mgr := NewManager(ManagerConfig{RequestTimeout: 20 * time.Millisecond})
	mgr.running = true
	mgr.stdin = json.NewEncoder(io.Discard)

	_, err := mgr.ApplyBotBatch(context.Background(), "request-timeout", "batch-1", "key-1", []BotConfig{{ID: "bot-1"}})
	require.ErrorContains(t, err, "等待 Bot Worker 回执超时")
	require.Zero(t, mgr.PendingRequestCount())

	mgr.handleEvent(&BotWorkerEvent{Evt: "batch-result", RequestID: "request-timeout"})
	require.Zero(t, mgr.PendingRequestCount())
}

func TestManagerProcessExitReleasesPendingRequest(t *testing.T) {
	mgr := NewManager(ManagerConfig{RequestTimeout: time.Second})
	mgr.running = true
	mgr.stdin = json.NewEncoder(io.Discard)

	errCh := make(chan error, 1)
	go func() {
		_, err := mgr.RequestFleetSnapshot(context.Background(), "request-exit")
		errCh <- err
	}()

	require.Eventually(t, func() bool { return mgr.PendingRequestCount() == 1 }, time.Second, time.Millisecond)
	mgr.mu.Lock()
	mgr.running = false
	mgr.failPendingLocked(fmt.Errorf("Bot Worker 进程已退出"))
	mgr.mu.Unlock()

	require.ErrorContains(t, <-errCh, "进程已退出")
	require.Zero(t, mgr.PendingRequestCount())
}

func TestManagerSignalAndSnapshotUseFrozenIPCNames(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	mgr := NewManager(ManagerConfig{RequestTimeout: time.Second})
	mgr.running = true
	mgr.stdin = json.NewEncoder(writer)
	decoder := json.NewDecoder(reader)

	signalDone := make(chan error, 1)
	go func() {
		_, err := mgr.SignalActions(context.Background(), "request-signal", []ActionSignal{{SignalID: "signal-1", BotID: "bot-1"}})
		signalDone <- err
	}()
	var signalCommand SignalActionsCommand
	require.NoError(t, decoder.Decode(&signalCommand))
	require.Equal(t, "signal-actions", signalCommand.Cmd)
	require.Equal(t, "request-signal", signalCommand.RequestID)
	mgr.handleEvent(&BotWorkerEvent{Evt: "signal-result", RequestID: "request-signal"})
	require.NoError(t, <-signalDone)

	snapshotDone := make(chan error, 1)
	go func() {
		_, err := mgr.RequestFleetSnapshot(context.Background(), "request-snapshot")
		snapshotDone <- err
	}()
	var snapshotCommand GetFleetSnapshotCommand
	require.NoError(t, decoder.Decode(&snapshotCommand))
	require.Equal(t, "get-fleet-snapshot", snapshotCommand.Cmd)
	require.Equal(t, "request-snapshot", snapshotCommand.RequestID)
	mgr.handleEvent(&BotWorkerEvent{Evt: "fleet-snapshot-result", RequestID: "request-snapshot"})
	require.NoError(t, <-snapshotDone)
}

func TestManagerFleetSnapshotTracksEpochCapacityAndBots(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	mgr.handleEvent(&BotWorkerEvent{
		Evt:                   "worker-ready",
		WorkerEpoch:           "epoch-1",
		WorkerEpochGeneration: 3,
		MaxBots:               50,
		Features:              []string{"fleet-v1", "batch-result"},
		CapacityGeneration:    7,
		BotWorkerVersion:      "0.4.0",
	})
	mgr.handleEvent(&BotWorkerEvent{
		Evt:                "heartbeat",
		ActiveBots:         2,
		ConnectingBots:     1,
		RSSBytes:           1024,
		EventLoopP95Ms:     12.5,
		CapacityGeneration: 8,
	})
	mgr.handleEvent(&BotWorkerEvent{Evt: "bot-state", Bots: []BotState{{ID: "bot-1", Status: "connected", SessionID: "run-1", Generation: 2, EventSeq: 9}}})

	capacity := mgr.CapacitySnapshot()
	require.True(t, capacity.Ready)
	require.False(t, capacity.Legacy)
	require.Equal(t, 50, capacity.MaxBots)
	require.Equal(t, 2, capacity.ActiveBots)
	require.Equal(t, 1, capacity.ConnectingBots)
	require.EqualValues(t, 8, capacity.CapacityGeneration)
	require.Equal(t, "epoch-1", capacity.WorkerEpoch)
	require.EqualValues(t, 3, capacity.WorkerEpochGeneration)

	mgr.handleEvent(&BotWorkerEvent{Evt: "heartbeat", ActiveBots: 4, ConnectingBots: 2, CapacityGeneration: 8})
	require.EqualValues(t, 8, mgr.CapacitySnapshot().CapacityGeneration, "即时利用率变化不应递增容量语义世代")

	fleet := mgr.FleetSnapshot("")
	require.Len(t, fleet, 1)
	require.Equal(t, "run-1", fleet[0].SessionID)
	require.EqualValues(t, 9, fleet[0].EventSeq)
}
