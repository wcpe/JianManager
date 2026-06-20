package grpc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/wxys233/JianManager/internal/worker/process"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// fakeBridge 是 pluginBridgeBroker 的测试替身，记录下发指令并保存注入的事件回调以便手动触发。
type fakeBridge struct {
	mu       sync.Mutex
	handler  func(instanceUUID, eventType, data string, ts int64)
	sent     []sentCommand
	deliverOK bool // SendCommand 返回值（模拟有/无插件连入）
}

type sentCommand struct {
	instanceUUID, id, action string
	args                     json.RawMessage
}

func (f *fakeBridge) SendCommand(instanceUUID, id, action string, args json.RawMessage) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentCommand{instanceUUID, id, action, args})
	return f.deliverOK
}

func (f *fakeBridge) SetEventHandler(h func(instanceUUID, eventType, data string, ts int64)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = h
}

func (f *fakeBridge) fire(instanceUUID, eventType, data string, ts int64) {
	f.mu.Lock()
	h := f.handler
	f.mu.Unlock()
	if h != nil {
		h(instanceUUID, eventType, data, ts)
	}
}

func (f *fakeBridge) lastSent() (sentCommand, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return sentCommand{}, false
	}
	return f.sent[len(f.sent)-1], true
}

// fakePluginEventStream 实现 WorkerService_StreamPluginEventsServer，收集 Send 的事件。
type fakePluginEventStream struct {
	grpc.ServerStream
	ctx  context.Context
	mu   sync.Mutex
	recv []*workerpb.PluginEvent
}

func (s *fakePluginEventStream) Context() context.Context { return s.ctx }

func (s *fakePluginEventStream) Send(e *workerpb.PluginEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recv = append(s.recv, e)
	return nil
}

func (s *fakePluginEventStream) snapshot() []*workerpb.PluginEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*workerpb.PluginEvent, len(s.recv))
	copy(out, s.recv)
	return out
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return NewServer(process.NewManager(t.TempDir()), "test-node", nil, nil, nil)
}

func TestSendPluginCommand_NoBridge(t *testing.T) {
	srv := newTestServer(t)
	resp, err := srv.SendPluginCommand(context.Background(), &workerpb.SendPluginCommandRequest{
		InstanceUuid: "inst-1", Action: "kick",
	})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "未启用插件桥")
}

func TestSendPluginCommand_DelegatesToBridge(t *testing.T) {
	srv := newTestServer(t)
	fb := &fakeBridge{deliverOK: true}
	srv.SetPluginBridge(fb)

	resp, err := srv.SendPluginCommand(context.Background(), &workerpb.SendPluginCommandRequest{
		InstanceUuid: "inst-1",
		Action:       "ban",
		ArgsJson:     `{"player":"Steve"}`,
	})
	require.NoError(t, err)
	assert.True(t, resp.Success, resp.Error)

	sent, ok := fb.lastSent()
	require.True(t, ok)
	assert.Equal(t, "inst-1", sent.instanceUUID)
	assert.Equal(t, "ban", sent.action)
	assert.JSONEq(t, `{"player":"Steve"}`, string(sent.args))
}

func TestSendPluginCommand_NoPluginConnected(t *testing.T) {
	srv := newTestServer(t)
	fb := &fakeBridge{deliverOK: false} // 模拟实例无插件连入
	srv.SetPluginBridge(fb)

	resp, err := srv.SendPluginCommand(context.Background(), &workerpb.SendPluginCommandRequest{
		InstanceUuid: "inst-1", Action: "kick",
	})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "无插件连入")
}

func TestSendPluginCommand_MissingAction(t *testing.T) {
	srv := newTestServer(t)
	srv.SetPluginBridge(&fakeBridge{deliverOK: true})
	resp, err := srv.SendPluginCommand(context.Background(), &workerpb.SendPluginCommandRequest{
		InstanceUuid: "inst-1", Action: "",
	})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "缺少 action")
}

func TestStreamPluginEvents_FanOutAndFilter(t *testing.T) {
	srv := newTestServer(t)
	fb := &fakeBridge{}
	srv.SetPluginBridge(fb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 订阅 inst-1
	stream := &fakePluginEventStream{ctx: ctx}
	done := make(chan error, 1)
	go func() {
		done <- srv.StreamPluginEvents(&workerpb.StreamPluginEventsRequest{InstanceUuid: "inst-1"}, stream)
	}()

	// 等订阅登记完成
	waitFor(t, func() bool {
		srv.pluginEventMu.Lock()
		defer srv.pluginEventMu.Unlock()
		return len(srv.pluginEventSubs) == 1
	})

	// 触发事件：inst-1 应收到，inst-2 应被过滤掉
	fb.fire("inst-2", "player_join", `{"player":"X"}`, 100)
	fb.fire("inst-1", "player_join", `{"player":"Steve"}`, 200)

	var got *workerpb.PluginEvent
	waitFor(t, func() bool {
		for _, e := range stream.snapshot() {
			if e.InstanceUuid == "inst-1" {
				got = e
				return true
			}
		}
		return false
	})
	require.NotNil(t, got)
	assert.Equal(t, "player_join", got.Type)
	assert.Equal(t, int64(200), got.Timestamp)

	// inst-2 事件不应出现
	for _, e := range stream.snapshot() {
		assert.NotEqual(t, "inst-2", e.InstanceUuid, "应按 instance_uuid 过滤")
	}

	// 取消上下文，流应退出并注销订阅
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StreamPluginEvents 未在上下文取消后退出")
	}
	waitFor(t, func() bool {
		srv.pluginEventMu.Lock()
		defer srv.pluginEventMu.Unlock()
		return len(srv.pluginEventSubs) == 0
	})
}

// waitFor 轮询等待条件成立，避免对异步逻辑用固定 sleep。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}
