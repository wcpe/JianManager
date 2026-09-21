package grpc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// waitNoInflight 轮询等待某节点重推单飞标记清空。
func waitNoInflight(t *testing.T, d *ResyncDeduper, node string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		d.mu.Lock()
		_, busy := d.inflight[node]
		d.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("节点 %s 的单飞标记未在时限内清空", node)
}

// TestResyncDeduper_SingleFlight 在飞期间重复触发被去重（幂等），完成后可再次触发。
func TestResyncDeduper_SingleFlight(t *testing.T) {
	d := NewResyncDeduper(0)
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var calls int32
	fn := func(string) {
		atomic.AddInt32(&calls, 1)
		started <- struct{}{}
		<-release
	}

	require.True(t, d.Trigger("n1", fn))
	<-started // 第一发确已进入在飞
	require.False(t, d.Trigger("n1", fn), "在飞期间重复触发应被去重")

	close(release)
	waitNoInflight(t, d, "n1")
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))

	// 完成后（冷却=0）可再次触发。
	release2 := make(chan struct{})
	close(release2)
	require.True(t, d.Trigger("n1", func(string) {}))
	waitNoInflight(t, d, "n1")
}

// TestResyncDeduper_Cooldown 冷却窗口内重复触发被节流；越过窗口后恢复。
func TestResyncDeduper_Cooldown(t *testing.T) {
	base := time.Now()
	cur := base
	d := NewResyncDeduper(time.Minute)
	d.now = func() time.Time { return cur }

	done := make(chan struct{}, 4)
	var calls int32
	fn := func(string) {
		atomic.AddInt32(&calls, 1)
		done <- struct{}{}
	}

	require.True(t, d.Trigger("n", fn))
	<-done
	waitNoInflight(t, d, "n")
	require.False(t, d.Trigger("n", fn), "冷却窗口内应节流")
	require.Equal(t, int32(1), atomic.LoadInt32(&calls))

	cur = base.Add(2 * time.Minute)
	require.True(t, d.Trigger("n", fn), "越过冷却窗口应可再次触发")
	<-done
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

// TestResyncDeduper_PerNodeIsolation 不同节点互不影响。
func TestResyncDeduper_PerNodeIsolation(t *testing.T) {
	d := NewResyncDeduper(time.Hour)
	done := make(chan string, 4)
	fn := func(u string) { done <- u }

	require.True(t, d.Trigger("a", fn))
	require.Equal(t, "a", <-done)
	waitNoInflight(t, d, "a")
	require.True(t, d.Trigger("b", fn), "另一节点不受 a 的冷却影响")
	require.Equal(t, "b", <-done)
}

// TestResyncDeduper_EmptyNodeNoop 空节点/空 fn 不触发。
func TestResyncDeduper_EmptyNodeNoop(t *testing.T) {
	d := NewResyncDeduper(time.Hour)
	require.False(t, d.Trigger("", func(string) {}))
	require.False(t, d.Trigger("n", nil))
	var nilD *ResyncDeduper
	require.False(t, nilD.Trigger("n", func(string) {}))
}

// fakeTunnelChannel 满足 grpctunnel.TunnelChannel 的最小实现（仅 Context 有意义）。
type fakeTunnelChannel struct {
	ctx context.Context
}

func (f *fakeTunnelChannel) Invoke(context.Context, string, any, any, ...grpc.CallOption) error {
	return errors.New("not implemented")
}

func (f *fakeTunnelChannel) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeTunnelChannel) Close()                   {}
func (f *fakeTunnelChannel) Context() context.Context { return f.ctx }
func (f *fakeTunnelChannel) Done() <-chan struct{}    { return nil }
func (f *fakeTunnelChannel) Err() error               { return nil }

// TestTunnelRegistry_OnOpenNotifiesWithFirstConnectDedup FR-455② + FR-456 F4：
// 语义分两条——每次隧道建立都触发 onConnected（幂等重推，不因瞬时 active=2 漏推）；
// 而长驻订阅类副作用走 onFirstConnected，仅在节点「从无到有」（active 0→1）时触发一次，
// 避免瞬时 active=2 时重复建立长驻订阅流（stdout/stderr 双写、事件重复扇出）。
func TestTunnelRegistry_OnOpenNotifiesWithFirstConnectDedup(t *testing.T) {
	reg := NewTunnelRegistry(nil)
	onConn := make(chan string, 4)
	onFirst := make(chan string, 4)
	reg.SetOnConnected(func(u string) { onConn <- u })
	reg.SetOnFirstConnected(func(u string) { onFirst <- u })

	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{nodeUUIDHeader: "n1"}))
	ch := &fakeTunnelChannel{ctx: ctx}

	reg.onOpen(ch) // active=1：onConnected + onFirstConnected
	reg.onOpen(ch) // active=2（瞬时旧未关新已开）：仅 onConnected

	require.Equal(t, "n1", <-onConn, "首次建立应触发 onConnected")
	require.Equal(t, "n1", <-onConn, "瞬时 active=2 时新连也应触发 onConnected（不因 n!=1 漏推）")
	require.Equal(t, "n1", <-onFirst, "首次建立（active 0→1）应触发一次 onFirstConnected")

	select {
	case u := <-onFirst:
		t.Fatalf("active=2 不应再次触发 onFirstConnected（否则会重复建立长驻订阅流），got %s", u)
	case <-time.After(50 * time.Millisecond):
	}
	require.True(t, reg.Connected("n1"))
}
