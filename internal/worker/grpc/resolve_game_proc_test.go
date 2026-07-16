package grpc

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveGameProc_FallsBackToSelf 无 java 后代时回退可用进程（根/最深后代），不得 nil。
// 用当前测试进程作根：跨平台稳定、无需 spawn 真实 java。
func TestResolveGameProc_FallsBackToSelf(t *testing.T) {
	p := resolveGameProc(context.Background(), int32(os.Getpid()))
	require.NotNil(t, p, "存在的进程必须解析出非 nil")
}

// TestResolveGameProc_InvalidPID 不存在的 PID 返回 nil，调用方据此走 note 分支。
func TestResolveGameProc_InvalidPID(t *testing.T) {
	p := resolveGameProc(context.Background(), -1)
	assert.Nil(t, p)
}
