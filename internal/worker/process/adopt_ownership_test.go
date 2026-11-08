package process

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// FR-497③ 归属复核收紧的跨平台用例。
//
// 依赖 /proc 的真实进程用例在 adopt_ownership_linux_test.go（`//go:build linux`）：强特征就是
// 「实例 UUID 出现在 cmdline / 进程环境」，而进程环境只有 /proc 能读，故那些用例按平台隔离；
// 本文件的判据（无 UUID 可比对 / PID 非法 / 只有弱特征）与平台无关，Windows 也须成立。
//
// 转红方式（改回缺陷行为）：让 DefaultVerifyAdoptOwnership 退化为 DefaultVerifyProcessOwnership 的
// 宽口径（多特征任一命中），「弱特征单独不判真」的断言即失守。

// TestDefaultVerifyAdoptOwnership_MissingUUID 无实例 UUID 就无从比对强特征 → 一律不判真。
//
// 收养记录的 UUID 来自 PID 文件名（adoptableRecord 已保证非空），这条是纵深防御：
// 若上游漏传 UUID，绝不允许退化成「按工作目录猜归属」。
func TestDefaultVerifyAdoptOwnership_MissingUUID(t *testing.T) {
	for _, expectWrapper := range []bool{false, true} {
		ok, evidence := DefaultVerifyAdoptOwnership(os.Getpid(), "", "/some/work/dir", expectWrapper)
		assert.False(t, ok, "UUID 为空不得判真（expectWrapper=%v）", expectWrapper)
		assert.Equal(t, "missing_instance_uuid", evidence)
	}
}

// TestDefaultVerifyAdoptOwnership_InvalidPID PID<=0 / 不存在一律不通过（保守不收养）。
func TestDefaultVerifyAdoptOwnership_InvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1, 1 << 30} {
		ok, evidence := DefaultVerifyAdoptOwnership(pid, "11111111-2222-3333-4444-555555555555", "/w", false)
		assert.False(t, ok, "pid=%d 不得判真", pid)
		assert.Contains(t, []string{"invalid_pid", "process_unreadable", "cmdline_unreadable"}, evidence,
			"pid=%d 应给出可解释的拦截口径，实际 %q", pid, evidence)
	}
}

// TestDefaultVerifyAdoptOwnership_RejectsSelfProcess self 进程的 cmdline 含可执行路径、cwd 为测试
// 包目录：两者都可能命中「含工作目录 / cwd 等于工作目录」的弱特征，但都不含本实例 UUID
// → 强特征不成立 → 不判真。这条钉住弱特征不能单独判真。
func TestDefaultVerifyAdoptOwnership_RejectsSelfProcess(t *testing.T) {
	cwd, err := os.Getwd()
	assert.NoError(t, err)
	const uuid = "no-such-uuid-9f0a"

	ok, evidence := DefaultVerifyAdoptOwnership(os.Getpid(), uuid, cwd, false)
	assert.False(t, ok, "self 进程 cwd 等于传入工作目录，但无强特征 → 不得判真")
	assert.Equal(t, "weak_evidence_only", evidence, "应如实记录「只有弱特征」")

	ok, evidence = DefaultVerifyAdoptOwnership(os.Getpid(), uuid, cwd, true)
	assert.False(t, ok, "self 进程 argv 不是 daemon → wrapper 分支不得判真")
	assert.Equal(t, "wrapper_argv_mismatch", evidence)
}
