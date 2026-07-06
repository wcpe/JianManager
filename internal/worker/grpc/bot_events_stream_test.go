package grpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/internal/worker/bot"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type botEventsTestStream struct {
	ctx    context.Context
	events chan *workerpb.BotEvent
}

func newBotEventsTestStream(ctx context.Context) *botEventsTestStream {
	return &botEventsTestStream{
		ctx:    ctx,
		events: make(chan *workerpb.BotEvent, 8),
	}
}

func (s *botEventsTestStream) Send(event *workerpb.BotEvent) error {
	s.events <- event
	return nil
}

func (s *botEventsTestStream) SetHeader(metadata.MD) error  { return nil }
func (s *botEventsTestStream) SendHeader(metadata.MD) error { return nil }
func (s *botEventsTestStream) SetTrailer(metadata.MD)       {}
func (s *botEventsTestStream) Context() context.Context     { return s.ctx }
func (s *botEventsTestStream) SendMsg(any) error            { return nil }
func (s *botEventsTestStream) RecvMsg(any) error            { return nil }

func TestStreamBotEventsFiltersAndCancels(t *testing.T) {
	srv := &Server{}
	srv.SetBotManager(bot.NewManager(bot.ManagerConfig{}))

	ctx, cancel := context.WithCancel(context.Background())
	stream := newBotEventsTestStream(ctx)
	done := make(chan error, 1)
	go func() {
		done <- srv.StreamBotEvents(&workerpb.StreamBotEventsRequest{BotUuid: "bot-a"}, stream)
	}()

	waitForBotEventSubscribers(t, srv, 1)

	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "bot-event", BotID: "bot-b", Type: "chat", Data: json.RawMessage(`{"message":"skip"}`)})
	assertNoBotEvent(t, stream.events)

	srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "bot-event", BotID: "bot-a", Type: "chat", Data: json.RawMessage(`{"message":"ok"}`)})
	got := assertBotEventReceived(t, stream.events)
	if got.BotUuid != "bot-a" || got.Type != "chat" || !strings.Contains(got.Data, "ok") {
		t.Fatalf("Bot 事件过滤或转发不正确: %+v", got)
	}

	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("取消订阅应返回 context canceled，实际 %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后 StreamBotEvents 未退出")
	}
	waitForBotEventSubscribers(t, srv, 0)
}

func TestDispatchBotEventSlowConsumerDoesNotBlock(t *testing.T) {
	srv := &Server{}
	ch := make(chan *bot.BotWorkerEvent, 1)
	ch <- &bot.BotWorkerEvent{Evt: "bot-event", BotID: "bot-a"}
	srv.botEventSubs = []chan *bot.BotWorkerEvent{ch}

	done := make(chan struct{})
	go func() {
		srv.dispatchBotEvent(&bot.BotWorkerEvent{Evt: "bot-event", BotID: "bot-a", Type: "chat"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("慢消费者不应阻塞 Bot 事件扇出")
	}
}

func TestStreamBotEventsWithoutBotManagerReturnsError(t *testing.T) {
	srv := &Server{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := srv.StreamBotEvents(&workerpb.StreamBotEventsRequest{}, newBotEventsTestStream(ctx))
	if err == nil || !strings.Contains(err.Error(), "未启用 Bot 能力") {
		t.Fatalf("未启用 Bot 能力时应返回明确错误，实际 %v", err)
	}
}

func waitForBotEventSubscribers(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		srv.botEventMu.Lock()
		got := len(srv.botEventSubs)
		srv.botEventMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Bot 事件订阅者数量未达到 %d", want)
}

func assertBotEventReceived(t *testing.T, events <-chan *workerpb.BotEvent) *workerpb.BotEvent {
	t.Helper()
	select {
	case got := <-events:
		return got
	case <-time.After(200 * time.Millisecond):
		t.Fatal("未收到 Bot 事件")
	}
	return nil
}

func assertNoBotEvent(t *testing.T, events <-chan *workerpb.BotEvent) {
	t.Helper()
	select {
	case got := <-events:
		t.Fatalf("不应收到 Bot 事件: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}
