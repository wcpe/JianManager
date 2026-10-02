package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// injectCgroupFS 把 cgroup 挂载点与「自身 cgroup」读数替换为假值，使测试可以完整验证迁移写路径，
// **不触碰真实 cgroup 层级**（真机上这是生产 Worker 的 cgroup，测试绝不能真迁移）。
func injectCgroupFS(t *testing.T, mountRoot, ownCgroup string) {
	t.Helper()
	oldRoot, oldReader := cgroupMountRoot, ownCgroupReader
	cgroupMountRoot = mountRoot
	ownCgroupReader = func() (string, error) { return ownCgroup, nil }
	t.Cleanup(func() { cgroupMountRoot, ownCgroupReader = oldRoot, oldReader })
}

// TestDaemonCgroupRootDerivation 锁定「Worker 单元 cgroup → 隔离根」的推导与全部保守边界。
func TestDaemonCgroupRootDerivation(t *testing.T) {
	const workerUnit = "/user.slice/user-1000.slice/user@1000.service/app.slice/jm-prod-worker.service"
	cases := []struct {
		name string
		own  string
		want string
		ok   bool
	}{
		{"用户会话内 service 单元", workerUnit,
			"/user.slice/user-1000.slice/user@1000.service/app.slice/" + daemonCgroupDirName, true},
		{"系统级 service 单元", "/system.slice/jianmanager-worker.service",
			"/system.slice/" + daemonCgroupDirName, true},
		{"已在隔离目录内（幂等判断）", "/user.slice/user-1000.slice/user@1000.service/app.slice/" + daemonCgroupDirName + "/inst-1", "", false},
		{"session scope（手工前台运行）", "/user.slice/user-1000.slice/user@1000.service/session-3.scope", "", false},
		{"容器内 cgroup 根", "/", "", false},
		{"空路径", "", "", false},
		{"相对路径", "user.slice/x.service", "", false},
		{"非 service 末段", "/system.slice/jianmanager-worker.slice", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := DaemonCgroupRoot(c.own)
			assert.Equal(t, c.ok, ok)
			if c.ok {
				assert.Equal(t, c.want, got)
			}
		})
	}
}

// TestDaemonCgroupDirFor 锁定叶目录拼接：两种自身形态（Worker 单元 / 已隔离）都能推出同一实例目录。
func TestDaemonCgroupDirFor(t *testing.T) {
	const unit = "/user.slice/user-1000.slice/user@1000.service/app.slice/jm-prod-worker.service"
	dir, ok := DaemonCgroupDirFor(unit, "inst-1")
	require.True(t, ok)
	assert.Equal(t, "/user.slice/user-1000.slice/user@1000.service/app.slice/"+daemonCgroupDirName+"/inst-1", dir)

	// 自身已在隔离目录内：返回自身（wrapper 退出时据此清理自己的目录）。
	same, ok := DaemonCgroupDirFor(dir, "inst-1")
	require.True(t, ok)
	assert.Equal(t, dir, same)

	// 无 UUID 一律不迁移。
	_, ok = DaemonCgroupDirFor(unit, "")
	assert.False(t, ok)
}

// TestCgroupIsolationEnabledByEnv 锁定运行期逃生开关的取值口径（默认开启）。
func TestCgroupIsolationEnabledByEnv(t *testing.T) {
	for _, off := range []string{"0", "false", "FALSE", "no", "off", "Off", "disabled", " 0 "} {
		t.Setenv(EnvCgroupIsolation, off)
		assert.False(t, CgroupIsolationEnabled(), "%q 应关闭隔离", off)
	}
	for _, on := range []string{"", "1", "true", "yes", "on", "anything"} {
		t.Setenv(EnvCgroupIsolation, on)
		assert.True(t, CgroupIsolationEnabled(), "%q 应保持隔离开启", on)
	}
}

// TestEscapeWorkerUnitCgroupWritesSelfPID 验证自迁动作的写路径：建隔离目录并把自身 PID 写进 cgroup.procs。
// 用假挂载点，故不会真迁移测试进程。
func TestEscapeWorkerUnitCgroupWritesSelfPID(t *testing.T) {
	root := t.TempDir()
	const unit = "/user.slice/user-1000.slice/user@1000.service/app.slice/jm-lab-worker.service"
	injectCgroupFS(t, root, unit)

	dir, err := EscapeWorkerUnitCgroup("inst-escape")
	require.NoError(t, err)
	want := filepath.Join(root, "/user.slice/user-1000.slice/user@1000.service/app.slice", daemonCgroupDirName, "inst-escape")
	require.Equal(t, want, dir)

	raw, err := os.ReadFile(filepath.Join(want, "cgroup.procs"))
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid()), strings.TrimSpace(string(raw)))
}

// TestEscapeWorkerUnitCgroupSkipsNonServiceCgroup 验证保守边界：非 service 单元（手工前台/容器/scope）
// 一律不迁移——此时不存在「重启单元连坐」的问题面，且越权迁移只会带来风险。
func TestEscapeWorkerUnitCgroupSkipsNonServiceCgroup(t *testing.T) {
	root := t.TempDir()
	injectCgroupFS(t, root, "/user.slice/user-1000.slice/user@1000.service/session-3.scope")

	dir, err := EscapeWorkerUnitCgroup("inst-escape")
	require.NoError(t, err)
	assert.Empty(t, dir)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries, "非 service 单元不得创建任何 cgroup 目录")
}

// TestEscapeWorkerUnitCgroupDisabledByEnv 验证开关关闭时不动系统。
func TestEscapeWorkerUnitCgroupDisabledByEnv(t *testing.T) {
	root := t.TempDir()
	injectCgroupFS(t, root, "/user.slice/user-1000.slice/user@1000.service/app.slice/jm-lab-worker.service")
	t.Setenv(EnvCgroupIsolation, "off")

	dir, err := EscapeWorkerUnitCgroup("inst-escape")
	require.NoError(t, err)
	assert.Empty(t, dir)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestMigratePIDTreeToCgroupMovesDescendants 验证接管路径的迁移范围：wrapper 与其 java 是两棵进程组，
// 只迁 wrapper 会漏掉 java——故必须按 PPID 链枚举后代一并迁移。
func TestMigratePIDTreeToCgroupMovesDescendants(t *testing.T) {
	if os.Getenv("JM_TEST_NO_FORK") != "" {
		t.Skip("环境要求禁止 fork")
	}
	root := t.TempDir()
	injectCgroupFS(t, root, "/user.slice/user-1000.slice/user@1000.service/app.slice/jm-lab-worker.service")

	// 造一棵真进程树：父（替身 wrapper）→ 子（替身 java）。子进程继承父进程 cgroup 的时机在 fork，
	// 此处只验证「枚举 + 写 cgroup.procs」这一步，故用假挂载点承接写入。
	child := exec.Command("sh", "-c", "sleep 30 & wait")
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _, _ = child.Process.Wait() })
	time.Sleep(300 * time.Millisecond) // 等子进程 fork 出来

	descendants := descendantsOf(child.Process.Pid)
	require.NotEmpty(t, descendants, "替身 wrapper 应有后代（模拟 java）")

	moved, err := MigratePIDTreeToCgroup(child.Process.Pid, "inst-adopt")
	require.NoError(t, err)
	// 只迁 root 会让 java 留在会被连坐的 cgroup 里——故本条锁定「root + 全部后代」都被迁。
	// 注：假挂载点是普通目录，多次 write 会因文件偏移互相覆盖（真实 cgroupfs 的 cgroup.procs 每次
	// write 都是一条独立迁移指令，无此语义），故这里以迁移计数为判据；真实 cgroup 上的成员归属由
	// process 包的端到端用例（cgroup 级 SIGTERM 后 wrapper 与 java 仍存活）出具证据。
	assert.Equal(t, 1+len(descendants), moved, "wrapper 与其后代必须一并迁移")

	dir := filepath.Join(root, "/user.slice/user-1000.slice/user@1000.service/app.slice",
		daemonCgroupDirName, "inst-adopt")
	st, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, st.IsDir())
}

// TestSweepEmptyDaemonCgroupDirsOnlyEmpty 验证启动清扫只删空目录（被 kill -9 的 wrapper 留下的空壳）。
func TestSweepEmptyDaemonCgroupDirsOnlyEmpty(t *testing.T) {
	root := t.TempDir()
	injectCgroupFS(t, root, "/user.slice/user-1000.slice/user@1000.service/app.slice/jm-lab-worker.service")
	rootDir := filepath.Join(root, "/user.slice/user-1000.slice/user@1000.service/app.slice", daemonCgroupDirName)
	require.NoError(t, os.MkdirAll(filepath.Join(rootDir, "empty-uuid"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(rootDir, "live-uuid"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, "live-uuid", "cgroup.procs"), []byte("1\n"), 0o644))

	SweepEmptyDaemonCgroupDirs()

	_, err := os.Stat(filepath.Join(rootDir, "empty-uuid"))
	assert.True(t, os.IsNotExist(err), "空目录应被清扫")
	_, err = os.Stat(filepath.Join(rootDir, "live-uuid"))
	assert.NoError(t, err, "仍有进程在册的目录不得被删")
}

// TestRemoveDaemonCgroupDirKeepsNonEmpty 验证退出清理遇到非空目录（java 仍在其中）时保持不动。
func TestRemoveDaemonCgroupDirKeepsNonEmpty(t *testing.T) {
	root := t.TempDir()
	injectCgroupFS(t, root, "/user.slice/user-1000.slice/user@1000.service/app.slice/jm-lab-worker.service")
	dir, ok := DaemonCgroupDirFor("/user.slice/user-1000.slice/user@1000.service/app.slice/jm-lab-worker.service", "inst-1")
	require.True(t, ok)
	fsDir := filepath.Join(root, dir)
	require.NoError(t, os.MkdirAll(fsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fsDir, "cgroup.procs"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644))

	RemoveDaemonCgroupDir("inst-1")
	_, err := os.Stat(fsDir)
	assert.NoError(t, err, "目录非空时不得强删")

	// 清空后即可回收（含空的根目录）。
	require.NoError(t, os.Remove(filepath.Join(fsDir, "cgroup.procs")))
	RemoveDaemonCgroupDir("inst-1")
	_, err = os.Stat(fsDir)
	assert.True(t, os.IsNotExist(err), "目录已空应被回收")
}
