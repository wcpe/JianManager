package daemon

import (
	"bytes"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureWriteCloser 捕获写入字节，作为 Java stdin 的测试替身。
type captureWriteCloser struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *captureWriteCloser) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *captureWriteCloser) Close() error { return nil }

func (c *captureWriteCloser) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// TestResolveStopCommand 校验优雅停止命令的取值：空回退到 MC 的 stop，否则用配置值。
func TestResolveStopCommand(t *testing.T) {
	assert.Equal(t, "stop", resolveStopCommand(""))
	assert.Equal(t, "stop", resolveStopCommand("  "))
	assert.Equal(t, "end", resolveStopCommand("end"))
	assert.Equal(t, "end", resolveStopCommand(" end "))
}

// TestStopJavaWritesConfiguredCommand 验证优雅停止时写入 Java stdin 的是「按配置派生」
// 的停止命令，而非硬编码的 "stop"。这是 FR-035 代理实例停止缺陷的回归测试：
// 代理（StopCommand="end"）必须收到 "end" 而非 "stop"，否则代理不退出、端口不释放。
func TestStopJavaWritesConfiguredCommand(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		want       string
	}{
		{"空配置回退到 stop（MC 后端）", "", "stop\n"},
		{"代理使用 end", "end", "end\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// stopJava 要求 cmd.Process != nil；用真实但无害的保活进程满足该前置，
			// 其 stdin 不接到捕获器（stopJava 写的是 w.javaStdin，与进程本身解耦）。
			cmd := buildJavaCmd(WrapperConfig{StartCommand: keepAliveCommand()})
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
			require.NoError(t, cmd.Start())
			t.Cleanup(func() { _ = cmd.Process.Kill() })

			cap := &captureWriteCloser{}
			w := &Wrapper{
				cfg:     WrapperConfig{InstanceUUID: "t", StopCommand: tt.configured},
				closing: make(chan struct{}),
			}
			w.javaCmd = cmd
			w.javaStdin = cap

			w.stopJava(false)

			assert.Equal(t, tt.want, cap.String())
		})
	}
}
