package process

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDefaultVerifyProcessOwnership_SelfProcess 用当前测试进程做确定性验证：
// 以真实进程的 cwd / cmdline 校准「确属」判定，无需外部夹具。
func TestDefaultVerifyProcessOwnership_SelfProcess(t *testing.T) {
	pid := os.Getpid()
	cwd, err := os.Getwd()
	assert.NoError(t, err)

	// 工作目录命中（cwd 等于工作目录）→ 确属。
	assert.True(t, DefaultVerifyProcessOwnership(pid, "", cwd, false),
		"进程 cwd 等于工作目录应判定确属")

	// cmdline 含工作目录路径 → 确属（此处以测试可执行文件路径近似；cmdline 至少含可执行路径）。
	exe, exeErr := os.Executable()
	if exeErr == nil && exe != "" {
		assert.True(t, DefaultVerifyProcessOwnership(pid, "", exe, false),
			"cmdline 含工作目录/可执行路径应判定确属")
	}

	// 工作目录与 UUID 均不匹配 → 不确属（不杀）。
	assert.False(t, DefaultVerifyProcessOwnership(pid, "no-such-uuid-9f0a", "/nonexistent/workdir-xyz", false),
		"特征都不匹配应判定为非目标实例")
}

// TestDefaultVerifyProcessOwnership_InvalidPID pid<=0 或不存在一律不通过（保守不杀）。
func TestDefaultVerifyProcessOwnership_InvalidPID(t *testing.T) {
	assert.False(t, DefaultVerifyProcessOwnership(0, "u", "/w", false))
	assert.False(t, DefaultVerifyProcessOwnership(-1, "u", "/w", false))
	// 一个几乎不可能存在的 PID；NewProcess 失败 → false。
	assert.False(t, DefaultVerifyProcessOwnership(1<<30, "u", "/w", false))
}

// TestLooksLikeJianManagerDaemon wrapper 标识启发式判定。
func TestLooksLikeJianManagerDaemon(t *testing.T) {
	cases := []struct {
		cmdline string
		want    bool
	}{
		{"/usr/local/bin/worker daemon", true},
		{"/opt/JianManager/jianmanager daemon --data-dir /x", true},
		{"C:\\JM\\worker.exe daemon", true},
		{"/usr/local/bin/worker", false},
		{"java -jar server.jar nogui", false},
		{"worker run", false},
		{"", false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, looksLikeJianManagerDaemon(c.cmdline), "cmdline=%q", c.cmdline)
	}
}

// TestSamePath 路径等价判定（相对路径/相等/不等）。
func TestSamePath(t *testing.T) {
	assert.True(t, samePath("/a/b", "/a/b"))
	assert.False(t, samePath("/a/b", "/a/c"))
	assert.False(t, samePath("", "/a"))
	if cwd, err := os.Getwd(); err == nil {
		assert.True(t, samePath(cwd, cwd), "同一路径应判等价")
		if strings.HasPrefix(cwd, "/") {
			assert.True(t, samePath(cwd, cwd+"/"), "尾随分隔符应判等价")
		}
	}
}
