package ws

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResetBuffer_F3CrashTailContamination 复现 F3（FR-471 缺陷修复）：
// 终端环形缓冲跨运行共享、若不按本次启动清空，上一轮输出会残留并污染本轮崩溃快照的根因归类。
//
// 真机现场：同一实例先后注入不同崩溃，第二次注入端口占用却被判为 oom——因缓冲里上一轮的
// OutOfMemoryError 行仍在，分类器按优先级取走了它。修复为「实例每次开始新一轮运行即重置缓冲」。
func TestResetBuffer_F3CrashTailContamination(t *testing.T) {
	s := NewTerminalServer(testSecret)

	// 第一轮运行：输出 OOM 文本（缓冲持有该行）。
	s.Broadcast("inst-1", "stdout", "java.lang.OutOfMemoryError: Java heap space\n")
	assert.Contains(t, string(s.BufferedOutput("inst-1")), "OutOfMemoryError",
		"第一轮输出应进入缓冲")

	// 第二轮运行开始：重置缓冲（等价 StartInstanceHandler 的行为）。
	s.ResetBuffer("inst-1")

	out := s.BufferedOutput("inst-1")
	assert.NotContains(t, string(out), "OutOfMemoryError",
		"新一次运行开始后，上一轮崩溃文本不得残留（否则崩溃快照根因会被带偏）")

	// 第二轮真实输出：只应看到本轮文本。
	s.Broadcast("inst-1", "stdout", "java.net.BindException: Address already in use\n")
	got := string(s.BufferedOutput("inst-1"))
	assert.Contains(t, got, "BindException")
	assert.NotContains(t, got, "OutOfMemoryError", "本轮缓冲不得混入上一轮文本")
}

// TestResetBuffer_NoBufferIsNoop 无缓冲时重置为 no-op（不建缓冲、不 panic）。
func TestResetBuffer_NoBufferIsNoop(t *testing.T) {
	s := NewTerminalServer(testSecret)
	require.NotPanics(t, func() { s.ResetBuffer("never-seen") })
	assert.Nil(t, s.BufferedOutput("never-seen"), "重置不应创建缓冲")
}
