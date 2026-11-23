package vlsup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSupervisorGuards 守住 vlsup 的三条安全边界。
//
// 原在 runbook 包（跨包集成测试的聚集处，该包已随旧链路删除而无生产代码）。
// 这三条断言的都是 vlsup 自己的契约，搬回本包；改动 vlsup 时它们会就近失败。
func TestSupervisorGuards(t *testing.T) {
	// ① 资产校验：SHA 不匹配必须拒绝（防止执行被替换的二进制）
	dir := t.TempDir()
	exe := filepath.Join(dir, "victoria-logs-fake")
	require.NoError(t, os.WriteFile(exe, []byte("fake"), 0o755))
	require.Error(t, VerifyAsset(exe, "deadbeef"))

	// ② 只允许本机回环：外网地址必须拒绝构造。
	// 这条是硬约束——VL 只监听 localhost，不能被跨机访问。
	_, err := NewClient(ClientOptions{BaseURL: "http://example.com:9428", Username: "u", Password: "p"})
	require.Error(t, err)

	// ③ 诊断日志 sink 不得写进 VL（否则诊断输出会把日志索引搅乱）
	assert.False(t, IndependentLoggerSink{}.WritesIntoVL())
}
