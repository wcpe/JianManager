package daemon

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListenDial_FrameRoundTrip 验证跨平台 socket/pipe 上的帧协议端到端通信。
// Wrapper 侧监听 → Worker 侧拨号 → 双向收发帧。
//
// 用 shortSockDir 而非 t.TempDir：本用例名较长，t.TempDir() 在长 TMPDIR 下会把
// SocketAddr 顶过 sun_path 上限（实测 111 字节 > 107），导致 bind: invalid argument —
// 该失败与本用例验证的帧协议无关。同因见 ownership_test.go 的 shortSockDir 注释。
func TestListenDial_FrameRoundTrip(t *testing.T) {
	addr := SocketAddr(shortSockDir(t), "test-frame-"+t.Name())

	ln, err := Listen(addr)
	require.NoError(t, err)
	defer ln.Close()

	// wrapper 侧：接受连接，读一个 stdout 帧并回写一个 stdin 帧
	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()

		// 读取 Worker 发来的控制帧
		fr, err := Decode(conn)
		if err != nil {
			serverErr <- err
			return
		}
		// 回写一个 stdout 数据帧
		resp := &Frame{
			Header:  Header{Channel: ChannelStdout, Type: TypeData},
			Payload: []byte("pong: " + string(fr.Payload)),
		}
		serverErr <- resp.Encode(conn)
	}()

	// Worker 侧：拨号并发送控制帧
	// 给服务端 goroutine 一点启动时间
	time.Sleep(50 * time.Millisecond)

	conn, err := Dial(addr)
	require.NoError(t, err)
	defer conn.Close()

	req := &Frame{
		Header:  Header{Channel: ChannelControl, Type: TypeCommand},
		Payload: []byte("ping"),
	}
	require.NoError(t, req.Encode(conn))

	resp, err := Decode(conn)
	require.NoError(t, err)
	assert.Equal(t, ChannelStdout, resp.Channel)
	assert.Equal(t, []byte("pong: ping"), resp.Payload)

	select {
	case err := <-serverErr:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("服务端未在超时内完成")
	}
}

// TestSocketAddr 平台地址格式正确性。
func TestSocketAddr(t *testing.T) {
	addr := SocketAddr(filepath.Join(t.TempDir(), "d"), "uuid-123")
	assert.NotEmpty(t, addr)
	assert.Contains(t, addr, "uuid-123")
}

// TestFrame_StreamEcho 通过 socket 模拟多帧流式输出（stdout 连续帧）。
func TestFrame_StreamEcho(t *testing.T) {
	// 同上：用短名避免 TMPDIR 偏长时越过 sun_path 的 108 字节上限。
	addr := SocketAddr(t.TempDir(), "stream")
	ln, err := Listen(addr)
	require.NoError(t, err)
	defer ln.Close()

	messages := [][]byte{[]byte("line1\n"), []byte("line2\n"), []byte("line3\n")}

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		for _, msg := range messages {
			f := &Frame{Header: Header{Channel: ChannelStdout, Type: TypeData}, Payload: msg}
			if err := f.Encode(conn); err != nil {
				return
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	conn, err := Dial(addr)
	require.NoError(t, err)
	defer conn.Close()

	var got [][]byte
	for i := 0; i < len(messages); i++ {
		fr, err := Decode(conn)
		require.NoError(t, err)
		got = append(got, fr.Payload)
	}
	for i, m := range messages {
		assert.True(t, bytes.Equal(m, got[i]), "帧 %d 不匹配", i)
	}
}
