package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// TestSendStdin 用户输入行被编码为 ChannelStdin 的 Data 帧、附换行（镜像 daemonStrategy.SendCommand）。
func TestSendStdin(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, sendStdin(&buf, "list"))

	fr, err := daemon.Decode(&buf)
	require.NoError(t, err)
	assert.Equal(t, daemon.ChannelStdin, fr.Channel)
	assert.Equal(t, daemon.TypeData, fr.Type)
	assert.Equal(t, "list\n", string(fr.Payload))
}

// TestSendControl_Stop Ctrl+C 首次发 ChannelControl 的 stop 命令帧。
func TestSendControl_Stop(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, sendControl(&buf, daemon.ControlStop))

	fr, err := daemon.Decode(&buf)
	require.NoError(t, err)
	assert.Equal(t, daemon.ChannelControl, fr.Channel)
	assert.Equal(t, daemon.TypeCommand, fr.Type)
	assert.Equal(t, "stop", string(fr.Payload))
}

// TestSendControl_Kill 连按两次 Ctrl+C 发 kill 命令帧。
func TestSendControl_Kill(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, sendControl(&buf, daemon.ControlKill))

	fr, err := daemon.Decode(&buf)
	require.NoError(t, err)
	assert.Equal(t, daemon.ChannelControl, fr.Channel)
	assert.Equal(t, "kill", string(fr.Payload))
}

// TestStreamOutput_StdoutStderr 读循环把 daemon 回传的 stdout/stderr Data 帧写到对应 writer，
// 控制响应帧（pong）被忽略，连接 EOF（daemon 退出）时正常返回 nil。
func TestStreamOutput_StdoutStderr(t *testing.T) {
	// 预置一段「daemon→jmctl」方向的帧流：stdout、stderr、control 响应，然后 EOF。
	var wire bytes.Buffer
	writeFrame(t, &wire, daemon.ChannelStdout, daemon.TypeData, []byte("server started\n"))
	writeFrame(t, &wire, daemon.ChannelStderr, daemon.TypeData, []byte("a warning\n"))
	writeFrame(t, &wire, daemon.ChannelControl, daemon.TypeResponse, []byte("pong"))

	var outBuf, errBuf bytes.Buffer
	err := streamOutput(&wire, &outBuf, &errBuf)
	// 帧流读尽后 Decode 触发 EOF，streamOutput 视为 daemon 退出，返回 nil。
	require.NoError(t, err)
	assert.Equal(t, "server started\n", outBuf.String())
	assert.Equal(t, "a warning\n", errBuf.String())
}

// TestStreamOutput_EmptyEOF 立即 EOF（daemon 已退出）也返回 nil，不卡死。
func TestStreamOutput_EmptyEOF(t *testing.T) {
	var empty bytes.Buffer
	var outBuf, errBuf bytes.Buffer
	require.NoError(t, streamOutput(&empty, &outBuf, &errBuf))
	assert.Empty(t, outBuf.String())
	assert.Empty(t, errBuf.String())
}

// writeFrame 向 buffer 写一帧（测试夹具，模拟 daemon 端编码）。
func writeFrame(t *testing.T, w io.Writer, ch daemon.Channel, typ daemon.Type, payload []byte) {
	t.Helper()
	fr := &daemon.Frame{Header: daemon.Header{Channel: ch, Type: typ}, Payload: payload}
	require.NoError(t, fr.Encode(w))
}
