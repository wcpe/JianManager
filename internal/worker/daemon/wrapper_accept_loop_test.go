package daemon

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// failingListener 是恒返回错误的假 listener，用于驱动接受循环的错误路径。
type failingListener struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (l *failingListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	l.calls++
	l.mu.Unlock()
	return nil, l.err
}

func (l *failingListener) Close() error   { return nil }
func (l *failingListener) Addr() net.Addr { return &net.TCPAddr{} }

func (l *failingListener) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// 接受循环韧性：瞬时 Accept 错误不得终止接受循环。
//
// 背景（Windows 真机验收暴露）：原先 Accept 一旦返回错误就记日志并 return，接受循环永久退出——
// 此后该实例再也无法被 Worker 连接（wrapper 只在进程重启时重建监听），客户端侧 WaitNamedPipe
// 持续报「管道一直不可用」，命令全部失败且无法自愈。Linux 上该失效态被 backlog 语义掩盖
// （拨号仍返回成功、命令静默堆积却无人接受），故只在 Windows 上表现为测试失败。
//
// 本用例锁定「循环必须继续重试」这一不变量：旧实现会在第一次错误后退出，此处会因 calls 不再
// 增长而超时失败。
func TestWrapperAcceptLoopSurvivesTransientAcceptError(t *testing.T) {
	w := &Wrapper{
		cfg:     WrapperConfig{InstanceUUID: "accept-retry"},
		closing: make(chan struct{}),
	}
	ln := &failingListener{err: errors.New("transient accept failure")}

	done := make(chan struct{})
	go func() { w.acceptLoop(ln); close(done) }()

	require.Eventually(t, func() bool { return ln.callCount() >= 3 },
		5*time.Second, 10*time.Millisecond,
		"Accept 持续出错时接受循环必须继续重试，而不是永久退出（退出后实例再也无法被连接）")

	// 关闭后循环应退出，不残留 goroutine。
	close(w.closing)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("关闭后接受循环应退出")
	}
}

// 关闭路径：listener 已被 Close（Start 的 defer ln.Close()）时接受循环正常退出，且不再重试。
func TestWrapperAcceptLoopExitsWhenWrapperClosed(t *testing.T) {
	w := &Wrapper{
		cfg:     WrapperConfig{InstanceUUID: "accept-closed"},
		closing: make(chan struct{}),
	}
	w.closed = true // 关闭路径已置位（见 Wrapper.Stop/Kill 流程）
	ln := &failingListener{err: errors.New("use of closed network connection")}

	done := make(chan struct{})
	go func() { w.acceptLoop(ln); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("wrapper 已关闭时接受循环应退出")
	}
	require.Equal(t, 1, ln.callCount(), "关闭导致的 Accept 错误不应触发重试")
}
