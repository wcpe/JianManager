package mcp

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SSE 兼容路径的连接登记测试（ADR-096 决策 5）。
//
// 这里**不再**有协议会话：无空闲/绝对超时、无巡检、无并发上限、无快照/列表，
// 连接只在「/sse 建连 → /message 回推 → 断开注销」这条链路上存在。

func TestSSEConnRegistry_RegisterGetUnregister(t *testing.T) {
	reg := NewSSEConnRegistry()
	p := testPrincipal(7, "sse-tok")

	conn := reg.Register(p, "10.0.0.5")
	require.NotNil(t, conn)
	assert.Regexp(t, `^mcps_[0-9a-f]{32}$`, conn.ID,
		"连接 id 保持 mcps_<32hex> 形态：/message?sessionId= 继续可用")
	assert.Same(t, p, conn.Principal, "归属校验要用的主体须随连接登记")
	assert.Equal(t, "10.0.0.5", conn.ClientIP)
	assert.False(t, conn.ConnectedAt.IsZero())

	got, ok := reg.Get(conn.ID)
	require.True(t, ok)
	assert.Same(t, conn, got)

	reg.Unregister(conn.ID)
	_, ok = reg.Get(conn.ID)
	assert.False(t, ok, "注销后不可再取")
	reg.Unregister(conn.ID) // 幂等：重复注销不得 panic
}

func TestSSEConn_SendAndClose(t *testing.T) {
	reg := NewSSEConnRegistry()
	conn := reg.Register(testPrincipal(1, "a"), "127.0.0.1")

	require.NoError(t, conn.SendSSE([]byte(`{"jsonrpc":"2.0"}`)))
	select {
	case data := <-conn.SSEChannel():
		assert.JSONEq(t, `{"jsonrpc":"2.0"}`, string(data))
	default:
		t.Fatal("推送的帧应可从回推通道读出")
	}

	conn.Close()
	assert.ErrorIs(t, conn.SendSSE([]byte("x")), ErrSSEConnClosed, "关闭后不得再推送")
	select {
	case _, open := <-conn.SSEChannel():
		assert.False(t, open, "关闭后回推通道应已关闭（/sse 处理循环据此退出）")
	default:
		t.Fatal("关闭后回推通道应已关闭")
	}
	select {
	case <-conn.Context().Done():
	default:
		t.Fatal("关闭后 context 应已取消")
	}
	conn.Close() // 幂等
}

func TestSSEConnRegistry_StopClosesAllConns(t *testing.T) {
	reg := NewSSEConnRegistry()
	c1 := reg.Register(testPrincipal(1, "a"), "127.0.0.1")
	c2 := reg.Register(testPrincipal(2, "b"), "127.0.0.2")

	reg.Stop()

	for _, conn := range []*SSEConn{c1, c2} {
		select {
		case <-conn.Context().Done():
		default:
			t.Fatalf("Stop 后连接 %s 的 context 应已取消", conn.ID)
		}
		_, ok := reg.Get(conn.ID)
		assert.False(t, ok, "Stop 后登记表应已清空")
	}
}

// TestSSEConnRegistry_NoLimit 回归保护：并发上限与超时已随无状态化移除。
//
// 旧实现的故障面是「重连不关旧连接 → 配额耗尽 → 永久 429」，故这里显式断言
// 连续登记 64 条连接全部成功且各自可推送（没有被上限拒绝，也没有被巡检回收）。
func TestSSEConnRegistry_NoLimit(t *testing.T) {
	reg := NewSSEConnRegistry()
	const n = 64
	conns := make([]*SSEConn, 0, n)
	for i := 0; i < n; i++ {
		conns = append(conns, reg.Register(testPrincipal(uint(i+1), "busy"), "127.0.0.1"))
	}
	require.Len(t, conns, n)
	for _, conn := range conns {
		_, ok := reg.Get(conn.ID)
		require.True(t, ok, "连接 %s 不应被上限或巡检回收", conn.ID)
		require.NoError(t, conn.SendSSE([]byte("ping")))
	}
}

func TestSSEConnRegistry_ConcurrentRegisterUnregister(t *testing.T) {
	reg := NewSSEConnRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn := reg.Register(testPrincipal(uint(i+1), "c"), "127.0.0.1")
			_ = conn.SendSSE([]byte("x"))
			reg.Unregister(conn.ID)
		}(i)
	}
	wg.Wait()
}
