package daemon

import (
	"bytes"
	"errors"
	"io"
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


// blockingWriter 是带「在飞窗口」的假连接：Write 停留 inFlightWindow 后返回，
// 统计在飞数量与 CloseHandle 次数。模拟 npipe 重叠 I/O「Write 在飞尚未返回」。
type blockingWriter struct {
	mu             sync.Mutex
	closed         bool
	inFlight       int           // 正在 Write 的调用数
	closes         int           // CloseHandle 调用次数（managedConn.closeOnce 应恒为 1）
	inFlightWindow time.Duration // 每次 Write 的在飞时长
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{inFlightWindow: 5 * time.Millisecond}
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.inFlight++
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.inFlight--
		b.mu.Unlock()
	}()
	time.Sleep(b.inFlightWindow)
	if func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.closed }() {
		return 0, errors.New("write on closed connection")
	}
	return len(p), nil
}

func (b *blockingWriter) Read([]byte) (int, error) { return 0, io.EOF }

func (b *blockingWriter) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closes++
	b.closed = true
	return nil
}

func (b *blockingWriter) snapshot() (inFlight, closes int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inFlight, b.closes
}
// TestWrapperFrameWritesAreSerialized 锁定「wrapper 侧帧写入必须整体互斥」不变量。
//
// 背景（Windows 真机验收发现）：workerConn 由多个 goroutine 共享（stdout/stderr 两条
// io.Copy、心跳 pong、退出事件），Frame.Encode 分 5 次 Write；无互斥时两次 Encode 的
// 分段写入交错成坏帧，Worker 解码失败即关连接。客户端侧 daemonStrategy.writeMu 已锁
// 同一不变量，wrapper 侧此前漏配——本用例与 TestDaemonStrategy_WriteFrameSerializesEncoding
// 对称；还原 writeFrameTo 的互斥后本用例转红。
func TestWrapperFrameWritesAreSerialized(t *testing.T) {
	w := &Wrapper{cfg: WrapperConfig{InstanceUUID: "frame-serialize"}}
	server, client := net.Pipe()
	defer client.Close()
	conn := &managedConn{netConn: server}

	frameA := &Frame{Header: Header{Channel: ChannelStdout, Type: TypeData},
		Payload: bytes.Repeat([]byte("a"), 256)}
	frameB := &Frame{Header: Header{Channel: ChannelStderr, Type: TypeData},
		Payload: bytes.Repeat([]byte("b"), 256)}

	// 先收全量再解析（server.Close 触发 client EOF），避免交错时解码器挂死。
	done := make(chan []byte, 1)
	go func() {
		raw, _ := io.ReadAll(client)
		done <- raw
	}()

	const total = 8
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		f := frameA
		if i%2 == 1 {
			f = frameB
		}
		go func(f *Frame) {
			defer wg.Done()
			_ = w.writeFrameTo(conn, f)
		}(f)
	}
	wg.Wait()
	_ = conn.Close()

	raw := <-done
	rd := bytes.NewReader(raw)
	for i := 0; i < total; i++ {
		fr, err := Decode(rd)
		require.NoError(t, err, "第 %d 帧无法解码（wrapper 侧并发写入被交错）", i+1)
		require.True(t, bytes.Equal(fr.Payload, frameA.Payload) || bytes.Equal(fr.Payload, frameB.Payload),
			"第 %d 帧载荷由多次写入交错而成: %q", i+1, fr.Payload)
	}
	require.Zero(t, rd.Len(), "流中存在多余字节（写入被交错）")
}

// TestWrapperConnCloseDoesNotRaceInFlightWrite 锁定「关闭不得击中在飞 Encode」不变量。
//
// 背景（Windows 真机验收发现）：npipe 每次读写带独立 overlapped 事件、由等待 goroutine
// WaitForSingleObject 收尾；Close 与在飞 Write 并发会让等待方拿到失效/被复用的句柄，
// 实测三种 Go runtime fatal（semasleep wait_failed / preemptM duplicatehandle failed /
// stopTheWorld not stopped）。修复路径：所有关闭经 closeWorkerConn 持 connMu（等在飞
// Encode 返回）+ managedConn.closeOnce（恰好关一次）。还原任一半不变量后本用例转红：
//   - 若 Close 不再等 connMu（还原为直接 Close）：统计到的「关闭时在飞写」会大于 0；
//   - 若去掉 closeOnce：重复关闭会产生第二次 CloseHandle。
//
// 变异验证记录（在 wrapper.go 临时还原无互斥直写/直关后运行本文件）：
//   - TestWrapperFrameWritesAreSerialized → FAIL（载荷被交错污染）；
//   - TestWrapperConnCloseDoesNotRaceInFlightWrite → FAIL（关闭时在飞写 > 0）。
func TestWrapperConnCloseDoesNotRaceInFlightWrite(t *testing.T) {
	w := &Wrapper{cfg: WrapperConfig{InstanceUUID: "close-race"}}
	bw := newBlockingWriter()
	conn := &managedConn{netConn: bw}

	// 写入方（模拟 stdout/stderr io.Copy）：高频短写，由 connMu 串行，
	// 每条 Write 停留 inFlightWindow 制造「在飞窗口」。
	const writers = 4
	const writesEach = 10
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := &Frame{Header: Header{Channel: ChannelStdout, Type: TypeData}, Payload: []byte("noise")}
			for j := 0; j < writesEach; j++ {
				_ = w.writeFrameTo(conn, f)
			}
		}()
	}

	// 关闭方与写入方并发：替换路径（Accept 接到新连接）就是这样的时序。
	var closerDone sync.WaitGroup
	closerDone.Add(1)
	go func() {
		defer closerDone.Done()
		w.closeWorkerConn(conn)
	}()

	wg.Wait()
	closerDone.Wait()

	inFlightAtClose, closes := bw.snapshot()
	require.Equal(t, 1, closes, "managedConn.closeOnce 必须保证每个连接恰好关闭一次")
	require.Zero(t, inFlightAtClose, "Close 不得与在飞 Encode 并发（Windows 重叠 I/O 句柄失效 → runtime fatal）")

	// 重复关闭验证 closeOnce 幂等。
	w.closeWorkerConn(conn)
	_, closes = bw.snapshot()
	require.Equal(t, 1, closes, "重复关闭不得产生第二次 CloseHandle")
}
