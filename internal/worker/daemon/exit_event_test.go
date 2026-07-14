package daemon

import (
	"bytes"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExitEvent_FrameRoundTrip 退出事件经控制通道事件帧编解码往返无损（FR-313）。
func TestExitEvent_FrameRoundTrip(t *testing.T) {
	fr, err := EncodeExitEventFrame(ExitEvent{Event: EventJavaExit, ExitCode: 137, Signal: "killed", DurationMs: 4200})
	require.NoError(t, err)
	assert.Equal(t, ChannelControl, fr.Channel)
	assert.Equal(t, TypeEvent, fr.Type)

	var buf bytes.Buffer
	require.NoError(t, fr.Encode(&buf))
	decoded, err := Decode(&buf)
	require.NoError(t, err)

	ev, ok := DecodeExitEvent(decoded.Payload)
	require.True(t, ok)
	assert.Equal(t, 137, ev.ExitCode)
	assert.Equal(t, "killed", ev.Signal)
	assert.Equal(t, int64(4200), ev.DurationMs)
}

// TestDecodeExitEvent_Filters 非 java_exit 事件与非法 JSON 被忽略（ok=false），
// 保证未来新增事件类型对老消费方安全。
func TestDecodeExitEvent_Filters(t *testing.T) {
	_, ok := DecodeExitEvent([]byte(`{"event":"future_event","exit_code":1}`))
	assert.False(t, ok, "未知事件应被忽略")

	_, ok = DecodeExitEvent([]byte(`not-json`))
	assert.False(t, ok, "非法 JSON 应被忽略")
}

// TestExitInfo_NilState Wait 出错且无退出状态时退出码取 -1、信号为空。
func TestExitInfo_NilState(t *testing.T) {
	code, sig := ExitInfo(nil)
	assert.Equal(t, -1, code)
	assert.Empty(t, sig)
}

// TestExitInfo_RealProcess 真实进程以非零码退出：退出码被正确提取；
// 非信号退出时信号为空（Windows 恒为空，与 spec「Windows 信号留空」一致）。
func TestExitInfo_RealProcess(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", "exit 3")
	} else {
		cmd = exec.Command("sh", "-c", "exit 3")
	}
	err := cmd.Run()
	require.Error(t, err, "非零退出应返回 ExitError")

	code, sig := ExitInfo(cmd.ProcessState)
	assert.Equal(t, 3, code)
	assert.Empty(t, sig, "非信号退出信号应为空")
}
