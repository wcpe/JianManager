//go:build windows

package daemon

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestWindowsPipeConfigContract 锁定 go-winio 命名管道的缓冲区合同。
// 控制帧必须在对端短暂不读时先落入缓冲区，避免写方长期占住连接互斥锁。
func TestWindowsPipeConfigContract(t *testing.T) {
	require.NotNil(t, pipeConfig)
	require.Equal(t, int32(512), pipeConfig.InputBufferSize)
	require.Equal(t, int32(512), pipeConfig.OutputBufferSize)
}

// TestWindowsDialTimeoutContract 锁定正超时拨号在管道实例繁忙时不会无限等待。
func TestWindowsDialTimeoutContract(t *testing.T) {
	addr := SocketAddr("", fmt.Sprintf("dial-timeout-%d-%d", os.Getpid(), time.Now().UnixNano()))
	ln, err := Listen(addr)
	require.NoError(t, err)
	defer ln.Close()

	accepted := make(chan net.Conn)
	acceptErr := make(chan error, 1)
	acceptCanceled := make(chan struct{})
	defer close(acceptCanceled)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		select {
		case accepted <- conn:
		case <-acceptCanceled:
			_ = conn.Close()
		}
	}()

	first, err := DialTimeout(addr, time.Second)
	require.NoError(t, err)
	defer first.Close()
	var peer net.Conn
	select {
	case err := <-acceptErr:
		t.Fatalf("首个命名管道连接未被接受: %v", err)
	case peer = <-accepted:
		defer peer.Close()
	case <-time.After(time.Second):
		t.Fatal("首个命名管道连接未在期限内被接受")
	}

	// 两端均先写后读：小控制帧必须落入 512 字节缓冲，不能等对端开始读取才返回。
	frame := &Frame{Header: Header{Channel: ChannelControl, Type: TypeCommand}, Payload: []byte(ControlPing)}
	for _, conn := range []net.Conn{first, peer} {
		require.NoError(t, conn.SetWriteDeadline(time.Now().Add(time.Second)))
		require.NoError(t, frame.Encode(conn), "对端未读取时，小控制帧应能写入管道缓冲")
	}
	for _, conn := range []net.Conn{first, peer} {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		got, err := Decode(conn)
		require.NoError(t, err)
		require.Equal(t, frame.Payload, got.Payload)
	}

	const timeout = 100 * time.Millisecond
	started := time.Now()
	conn, err := DialTimeout(addr, timeout)
	elapsed := time.Since(started)
	if conn != nil {
		defer conn.Close()
	}

	require.Error(t, err)
	require.Nil(t, conn)
	require.GreaterOrEqual(t, elapsed, timeout/2)
	require.Less(t, elapsed, 2*time.Second)
}
