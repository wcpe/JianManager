package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
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

func TestFleetIPCRoundTripPreservesFrozenFields(t *testing.T) {
	signalCommand := SignalActionsCommand{
		Cmd:       "signal-actions",
		RequestID: "request-signal",
		Signals: []ActionSignal{{
			SignalID: "signal-1", BotID: "bot-1", SessionID: "run-1", Generation: 3,
			ActionRunID: "action-1", StepID: "step-1", Type: "probe",
			CorrelationToken: "token-1", Payload: json.RawMessage(`{"ok":true}`), ObservedAt: 123,
		}},
	}
	payload, err := json.Marshal(signalCommand)
	require.NoError(t, err)
	var decodedSignal SignalActionsCommand
	require.NoError(t, json.Unmarshal(payload, &decodedSignal))
	require.Equal(t, signalCommand, decodedSignal)
	require.Contains(t, string(payload), `"cmd":"signal-actions"`)

	createCommand := CreateBotsCommand{
		Cmd:  "create-bots",
		Bots: []BotConfig{{ID: "bot-1", ConnectNotBefore: 456}},
	}
	payload, err = json.Marshal(createCommand)
	require.NoError(t, err)
	var rawCreate map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &rawCreate))
	var rawBots []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rawCreate["bots"], &rawBots))
	require.Contains(t, rawBots[0], "connectNotBefore")
	require.NotContains(t, rawBots[0], "connectNotBeforeUnixMs")
	var decodedCreate CreateBotsCommand
	require.NoError(t, json.Unmarshal(payload, &decodedCreate))
	require.EqualValues(t, 456, decodedCreate.Bots[0].ConnectNotBefore)

	resultPayload, err := json.Marshal(SignalItemResult{SignalID: "signal-1", Accepted: true, Status: "accepted"})
	require.NoError(t, err)
	require.Contains(t, string(resultPayload), `"status":"accepted"`)
	var decodedResult SignalItemResult
	require.NoError(t, json.Unmarshal(resultPayload, &decodedResult))
	require.Equal(t, "accepted", decodedResult.Status)
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

func TestManagerWorkerExitIsNotDroppedForBufferedSubscriber(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	mgr.capacity.WorkerEpoch = "epoch-exit"
	mgr.capacity.WorkerEpochGeneration = 7
	events, cancel := mgr.SubscribeEvents(1)
	defer cancel()
	// 先塞满订阅缓冲，模拟慢消费者；worker-exit 必须仍可达。
	mgr.handleEvent(&BotWorkerEvent{Evt: "bot-event", BotID: "bot-1"})
	require.Eventually(t, func() bool {
		return len(events) == 1
	}, time.Second, time.Millisecond)

	mgr.mu.Lock()
	exitEvent, cb := mgr.invalidateRuntimeLocked("bot-worker 进程已退出", fmt.Errorf("Bot Worker 进程已退出"))
	mgr.mu.Unlock()
	if cb != nil {
		cb(exitEvent)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Evt != "worker-exit" {
				// 允许看到 worker-exit 前残留的 runtime 事件，但不能丢退出信号。
				continue
			}
			require.Equal(t, "epoch-exit", event.WorkerEpoch)
			require.EqualValues(t, 7, event.WorkerEpochGeneration)
			return
		case <-deadline:
			t.Fatal("订阅缓冲已满时仍必须收到 worker-exit")
		}
	}
}

func TestManagerFleetSnapshotResultAuthoritativelyClearsCache(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	mgr.bots["ghost-bot"] = &BotState{ID: "ghost-bot", Status: "connected"}

	mgr.handleEvent(&BotWorkerEvent{Evt: "fleet-snapshot-result", RequestID: "snapshot-empty", Bots: []BotState{}})

	require.Empty(t, mgr.FleetSnapshot(""))
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

func TestManagerMergeBotStateFencesEpochAndEventSequence(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	mgr.handleEvent(&BotWorkerEvent{Evt: "bot-state", Bots: []BotState{{
		ID: "bot-1", Status: "connected", WorkerEpochGeneration: 3, EventSeq: 10,
	}}})

	mgr.handleEvent(&BotWorkerEvent{Evt: "bot-state", Bots: []BotState{{
		ID: "bot-1", Status: "stopped", WorkerEpochGeneration: 2, EventSeq: 99,
	}}})
	state, ok := mgr.GetBot("bot-1")
	require.True(t, ok, "旧 epoch 的停止事件不能删除当前 Bot")
	require.Equal(t, "connected", state.Status)

	mgr.handleEvent(&BotWorkerEvent{Evt: "bot-state", Bots: []BotState{{
		ID: "bot-1", Status: "disconnected", WorkerEpochGeneration: 3, EventSeq: 10,
	}}})
	state, ok = mgr.GetBot("bot-1")
	require.True(t, ok)
	require.Equal(t, "connected", state.Status, "同 epoch 的重复 eventSeq 必须丢弃")

	mgr.handleEvent(&BotWorkerEvent{Evt: "bot-state", Bots: []BotState{{
		ID: "bot-1", Status: "connecting", WorkerEpochGeneration: 4, EventSeq: 1,
	}}})
	state, ok = mgr.GetBot("bot-1")
	require.True(t, ok)
	require.Equal(t, "connecting", state.Status)
	require.EqualValues(t, 4, state.WorkerEpochGeneration)
	require.EqualValues(t, 1, state.EventSeq, "更高 epoch 应允许 eventSeq 重置")
}

func TestManagerReadLoopRejectsStaleReaderEvents(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	mgr.capacity = BotCapacitySnapshot{
		Ready: true, ActiveBots: 7, ConnectingBots: 2,
		CapacityGeneration: 20, WorkerEpoch: "epoch-current", WorkerEpochGeneration: 5,
	}
	mgr.readyCh = make(chan struct{})
	mgr.bots["bot-1"] = &BotState{
		ID: "bot-1", Status: "connected", WorkerEpochGeneration: 5, EventSeq: 8,
	}
	waiter := make(chan pendingRequestResult, 1)
	mgr.pending["late-request"] = waiter

	oldEvents := strings.Join([]string{
		`{"evt":"worker-ready","workerEpoch":"epoch-old","workerEpochGeneration":4,"maxBots":1,"features":["fleet-v1"]}`,
		`{"evt":"heartbeat","activeBots":99,"connectingBots":99,"capacityGeneration":99}`,
		`{"evt":"bot-state","bots":[{"id":"bot-1","status":"disconnected","workerEpochGeneration":4,"eventSeq":99}]}`,
		`{"evt":"batch-result","requestId":"late-request"}`,
	}, "\n")
	oldScanner := bufio.NewScanner(strings.NewReader(oldEvents))
	mgr.stdout = oldScanner
	mgr.activeReaderGeneration = 5

	mgr.readLoop(oldScanner, 4)

	capacity := mgr.CapacitySnapshot()
	require.Equal(t, "epoch-current", capacity.WorkerEpoch)
	require.Equal(t, 7, capacity.ActiveBots)
	require.Equal(t, 2, capacity.ConnectingBots)
	require.EqualValues(t, 20, capacity.CapacityGeneration)
	select {
	case <-mgr.readyCh:
		t.Fatal("旧 reader 的 worker-ready 不得关闭当前 readyCh")
	default:
	}
	state, ok := mgr.GetBot("bot-1")
	require.True(t, ok)
	require.Equal(t, "connected", state.Status)
	require.Equal(t, 1, mgr.PendingRequestCount(), "旧 reader 的 batch-result 不得完成当前 pending")
	select {
	case <-waiter:
		t.Fatal("旧 reader 的回执不得唤醒当前 waiter")
	default:
	}
}

type blockingWriteCloser struct {
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{started: make(chan struct{}), release: make(chan struct{})}
}

func (w *blockingWriteCloser) Write(p []byte) (int, error) {
	w.startOnce.Do(func() { close(w.started) })
	<-w.release
	return len(p), nil
}

func (w *blockingWriteCloser) Close() error {
	w.closeOnce.Do(func() { close(w.release) })
	return nil
}

func TestManagerBlockedWriteTimeoutStartsBeforeEncode(t *testing.T) {
	writer := newBlockingWriteCloser()
	defer writer.Close()
	mgr := NewManager(ManagerConfig{RequestTimeout: 20 * time.Millisecond})
	mgr.running = true
	mgr.stdin = json.NewEncoder(writer)
	mgr.stdinPipe = writer

	startedAt := time.Now()
	_, err := mgr.ApplyBotBatch(context.Background(), "request-timeout-write", "batch-1", "key-1", []BotConfig{{ID: "bot-1"}})

	require.ErrorContains(t, err, "stdin 写入超时")
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
	require.Zero(t, mgr.PendingRequestCount())
	require.False(t, mgr.IsRunning(), "阻塞写超时后不健康 child 必须被隔离")
}

func TestManagerBlockedWriteHonorsContextWithoutHoldingStateLock(t *testing.T) {
	writer := newBlockingWriteCloser()
	defer writer.Close()
	mgr := NewManager(ManagerConfig{RequestTimeout: time.Second})
	mgr.running = true
	mgr.stdin = json.NewEncoder(writer)
	mgr.stdinPipe = writer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestDone := make(chan error, 1)
	go func() {
		_, err := mgr.ApplyBotBatch(ctx, "request-blocked", "batch-1", "key-1", []BotConfig{{ID: "bot-1"}})
		requestDone <- err
	}()
	<-writer.started

	lockAcquired := make(chan struct{})
	go func() {
		mgr.mu.Lock()
		close(lockAcquired)
		mgr.mu.Unlock()
	}()
	select {
	case <-lockAcquired:
	case <-time.After(200 * time.Millisecond):
		writer.Close()
		<-lockAcquired
		t.Fatal("阻塞 stdin 写入时 Manager 主锁仍被占用，child exit 无法归位")
	}

	cancel()
	select {
	case err := <-requestDone:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(200 * time.Millisecond):
		writer.Close()
		<-requestDone
		t.Fatal("stdin 写入阻塞后 context 取消未及时返回")
	}
	require.Zero(t, mgr.PendingRequestCount())
}
