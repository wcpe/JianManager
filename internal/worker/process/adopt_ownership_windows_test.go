//go:build windows

package process

import (
	"os"
	"os/exec"
	"testing"

	psproc "github.com/shirou/gopsutil/v4/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-497③ Windows 侧可执行断言（`//go:build windows`）。
//
// **本机是 Linux，这些用例从未在 Windows 上真机跑过。** 它们的价值有两个，且都有限：
//  1. 它们是「收养在 Windows 上必须 fail-closed」这条契约的可执行描述——一旦有人给 Windows 补上
//     进程环境读取（PEB / NtQueryInformationProcess），本文件的断言会立刻转红，逼他同步复核
//     整个判据（而不是悄悄改变平台行为）；
//  2. `GOOS=windows go vet ./internal/worker/process/` 会编译它，保证 Windows 侧代码路径不会被
//     改坏而没人发现。
//
// 未验清单（真机才能验，见 FR-497 验收报告「Windows」一节）：
//  - `IsPIDAlive` 的 OpenProcess 语义（句柄权限不足时会误判「不在」）；
//  - `taskkill /T /F` 的进程树终止范围、异步性，以及「杀完立查仍存活」的窗口；
//  - PID 复用判据（本项目只用 PID 数字，没有进程创建时刻校验）；
//  - `samePath` 在 Windows 上大小写不敏感（当前实现是字符串比较，`C:\A` ≠ `c:\a`）。

// TestAdoptOwnership_WindowsHasNoProcEnviron Windows 上没有 /proc：环境读取必须**失败**
// （而不是返回空串或伪造结果），这是收养 fail-closed 的事实基础。
func TestAdoptOwnership_WindowsHasNoProcEnviron(t *testing.T) {
	env, err := readProcessEnviron(os.Getpid())
	require.Error(t, err, "Windows 无 /proc/<pid>/environ，必须报错而不是返回空环境")
	require.Empty(t, env)

	assert.False(t, processEnvMatchesInstance(os.Getpid(), "11111111-2222-3333-4444-555555555555"),
		"环境读不到 ⇒ 强特征不成立（宁可不收养，绝不误纳管）")
}

// TestAdoptOwnership_WindowsSelfProcessFailsClosed 自进程的归属复核必须 fail-closed。
//
// 自进程是最容易「被凑巧判真」的对象：cwd 与 cmdline 都可读，只要误把弱特征当强特征，它就会通过。
func TestAdoptOwnership_WindowsSelfProcessFailsClosed(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	cwd, err := os.Getwd()
	require.NoError(t, err)

	ok, evidence := DefaultVerifyAdoptOwnership(os.Getpid(), uuid, cwd, false)
	assert.False(t, ok, "Java 分支：无环境强特征 + cmdline 无该 UUID ⇒ 不得判属")
	assert.Equal(t, "weak_evidence_only", evidence, "只有 cwd 弱特征时必须如实标注")

	ok, evidence = DefaultVerifyAdoptOwnership(os.Getpid(), uuid, cwd, true)
	assert.False(t, ok, "wrapper 分支：测试进程 argv 不是 daemon ⇒ 不得判属")
	assert.Contains(t, []string{"wrapper_argv_mismatch", "env_uuid_missing"}, evidence,
		"两平台口径一致：argv 不符或环境读不到，二者皆拒")
}

// TestAdoptOwnership_WindowsDaemonArgvStillRejected 即使 argv 形如 `<worker.exe> daemon`，
// 环境读不到也必须拒（这正是 Windows 上收养「不生效、只告警」的直接原因）。
//
// 构造：把测试二进制复制成 `worker.exe` 并用自己的 daemon 形态 argv 起一个真实子进程。
// 它一定不会跑任何用例（argv 不匹配 -test.run），只为造出「wrapper 形状」的 cmdline。
func TestAdoptOwnership_WindowsDaemonArgvStillRejected(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	exe, err := os.Executable()
	require.NoError(t, err)
	body, err := os.ReadFile(exe)
	require.NoError(t, err)

	dir := t.TempDir()
	workerExe := dir + `\worker.exe`
	require.NoError(t, os.WriteFile(workerExe, body, 0o755))

	cmd := exec.Command(workerExe, "daemon")
	cmd.Env = append(os.Environ(), "JM_FR497_WINDOWS_PROBE=1")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	p, err := psproc.NewProcess(int32(cmd.Process.Pid))
	require.NoError(t, err)
	cmdline, err := p.Cmdline()
	require.NoError(t, err)
	require.True(t, looksLikeJianManagerDaemon(cmdline), "前提：argv 必须是 worker daemon 形态：%s", cmdline)

	ok, evidence := DefaultVerifyAdoptOwnership(cmd.Process.Pid, uuid, dir, true)
	assert.False(t, ok, "argv 形如 daemon 但环境读不到 ⇒ Windows 上不得收养")
	assert.Equal(t, "env_uuid_missing", evidence)
}

// TestWindowsProcessTreeKillPrimitives 与 Stop 链路相关的 Windows 原语冒烟。
//
// 只断言「不 panic、参数非法时安全返回」这一层——真正的进程树终止范围必须真机验（本机无 Windows）。
func TestWindowsProcessTreeKillPrimitives(t *testing.T) {
	// pid<=0 一律无操作（绝不把 0/-1 变成「杀当前进程组」这类事故）。
	require.NoError(t, daemon.KillPIDTree(0))
	require.NoError(t, daemon.KillPIDTree(-1))
	require.NoError(t, signalPIDTree(0))

	// 自进程必须判活（OpenProcess 能拿到自己的句柄）。
	assert.True(t, daemon.IsPIDAlive(os.Getpid()))
	// 一个几乎必然不存在的 PID 必须判死。
	assert.False(t, daemon.IsPIDAlive(1<<30))
}
