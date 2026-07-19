package bot

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestManagerEventSubscribersReceiveEventsAndCallback(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	eventsA, cancelA := mgr.SubscribeEvents(2)
	defer cancelA()
	eventsB, cancelB := mgr.SubscribeEvents(2)
	defer cancelB()

	callbacks := make(chan *BotWorkerEvent, 1)
	mgr.SetEventCallback(func(event *BotWorkerEvent) {
		callbacks <- event
	})

	want := &BotWorkerEvent{Evt: "bot-event", BotID: "bot-a", Type: "chat"}
	mgr.handleEvent(want)

	assertEventReceived(t, eventsA, want)
	assertEventReceived(t, eventsB, want)
	assertEventReceived(t, callbacks, want)
}

func TestManagerEventSubscriberCancelStopsDelivery(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	events, cancel := mgr.SubscribeEvents(1)
	cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("取消后的订阅通道应关闭")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("取消订阅后通道未关闭")
	}

	mgr.handleEvent(&BotWorkerEvent{Evt: "bot-event", BotID: "bot-a"})
}

func TestManagerSlowSubscriberDoesNotBlockEventHandling(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	events, cancel := mgr.SubscribeEvents(1)
	defer cancel()

	mgr.handleEvent(&BotWorkerEvent{Evt: "bot-event", BotID: "bot-a", Type: "first"})

	done := make(chan struct{})
	go func() {
		mgr.handleEvent(&BotWorkerEvent{Evt: "bot-event", BotID: "bot-a", Type: "second"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("慢订阅者不应阻塞事件处理")
	}

	assertEventReceived(t, events, &BotWorkerEvent{Type: "first"})
}

func TestManagerActionEventsSurviveRuntimeBacklog(t *testing.T) {
	mgr := NewManager(ManagerConfig{})
	events, cancel := mgr.SubscribeEvents(1)
	defer cancel()

	startedAt := time.Now()
	for i := range 256 {
		mgr.handleEvent(&BotWorkerEvent{Evt: "bot-state", Bots: []BotState{{ID: "bot-runtime", EventSeq: int64(i + 1)}}})
	}
	const actionRuns = 64
	for i := range actionRuns {
		actionRunID := fmt.Sprintf("action-%03d", i)
		mgr.handleEvent(actionTestEvent(actionRunID, "running", json.RawMessage(`{"type":"barrier-arrived"}`)))
		mgr.handleEvent(actionTestEvent(actionRunID, "succeeded", nil))
	}
	dispatchElapsed := time.Since(startedAt)
	if dispatchElapsed >= 500*time.Millisecond {
		t.Fatalf("可靠动作事件入队不应阻塞 stdout 链路: %v", dispatchElapsed)
	}

	seen := make(map[string]map[string]bool, actionRuns)
	deadline := time.After(2 * time.Second)
	for len(seen) < actionRuns || !allActionStatusesSeen(seen) {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("动作事件全部交付前订阅被关闭")
			}
			if event.Evt != "action-event" || event.Action == nil {
				continue
			}
			statuses := seen[event.Action.ActionRunID]
			if statuses == nil {
				statuses = make(map[string]bool, 2)
				seen[event.Action.ActionRunID] = statuses
			}
			statuses[event.Action.Status] = true
		case <-deadline:
			t.Fatalf("runtime 压力下 action-event 丢失: 已收到 %d/%d 组", len(seen), actionRuns)
		}
	}
}

func actionTestEvent(actionRunID, status string, result json.RawMessage) *BotWorkerEvent {
	return &BotWorkerEvent{Evt: "action-event", Action: &ActionEvent{
		BotID: "bot-action", SessionID: "run-1", Generation: 1,
		ActionRunID: actionRunID, StepID: "barrier-1", Status: status, Result: result,
	}}
}

func allActionStatusesSeen(seen map[string]map[string]bool) bool {
	for _, statuses := range seen {
		if !statuses["running"] || !statuses["succeeded"] {
			return false
		}
	}
	return true
}

func assertEventReceived(t *testing.T, events <-chan *BotWorkerEvent, want *BotWorkerEvent) {
	t.Helper()
	select {
	case got, ok := <-events:
		if !ok {
			t.Fatal("订阅通道被提前关闭")
		}
		if want.Evt != "" && got.Evt != want.Evt {
			t.Fatalf("事件 evt 不匹配，期望 %q 实际 %q", want.Evt, got.Evt)
		}
		if want.BotID != "" && got.BotID != want.BotID {
			t.Fatalf("事件 botId 不匹配，期望 %q 实际 %q", want.BotID, got.BotID)
		}
		if want.Type != "" && got.Type != want.Type {
			t.Fatalf("事件 type 不匹配，期望 %q 实际 %q", want.Type, got.Type)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("未收到事件")
	}
}
