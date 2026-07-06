package bot

import (
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
