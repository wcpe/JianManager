package grpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/worker/daemon"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// startReapedSleeper 起一个独立进程组的存活替身进程，并挂一个 goroutine 在其退出时 Wait 回收
// （否则被杀后成僵尸，signal-0 探测仍报存活，waitPIDsGone 永等不到死透）。返回 PID 与清理函数。
func startReapedSleeper(t *testing.T) (int, func()) {
	t.Helper()
	cmd, err := startDetachedSleeper()
	require.NoError(t, err)
	pid := cmd.Process.Pid
	waited := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(waited) }()
	return pid, func() {
		_ = daemon.KillPIDTree(pid)
		select {
		case <-waited:
		case <-time.After(3 * time.Second):
		}
	}
}

// TestRemoveInstance_ReapsLingeringDaemonTree 复现 FR-310 真机缺陷：删除运行中 daemon 实例时，
// 删除流程只杀 wrapper 组，Unix 上自成进程组（wrapper.applyProcAttr）的 Java 子进程未死，
// RemoveAll 之后 Java 继续写 world region 把工作目录重建出来（worker 日志打印「已清理工作目录」，
// 盘上却残留 world/plugins），且 wrapper 被强杀时其 defer 未执行、遗留 <uuid>.pid / <uuid>.sock。
//
// 用「注册表已摘除但 PID 记录仍指向存活进程树」建模该竞态（Worker 重启窗口 / 优雅停机未死透）：
// 绕过运行态守卫，忠实复现「删除路径必须先杀死整棵进程树再清目录」。当前实现下 RemoveInstance
// 从不消费 PID 记录 → 进程树存活、pid/sock 残留（本测试 RED）；修复后进程树死透、三者皆清（GREEN）。
func TestRemoveInstance_ReapsLingeringDaemonTree(t *testing.T) {
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	// 关键：manager 的 pidDir 必须与数据根 var/servers 一致（wrapper 真实写 pid/sock 之处），
	// 删除路径的进程树 reap 才能按 <uuid>.pid 找到记录。
	srv := NewServer(process.NewManager(root.ServersDir()), "test-node", nil, nil, root)

	const uuid = "77777777-7777-7777-7777-777777777777"

	// wrapper 与 java 各起一棵独立进程组的存活替身（Unix Setpgid，faithful 到真实 wrapper 派生 Java 的
	// 进程组语义）：唯有按各自 PID 杀树才能都杀掉，杀 wrapper 一棵够不到 java。
	wrapperPID, cleanWrapper := startReapedSleeper(t)
	defer cleanWrapper()
	javaPID, cleanJava := startReapedSleeper(t)
	defer cleanJava()

	// 托管区内的工作目录 + world 子目录（模拟真实实例目录）。
	workDir := filepath.Join(root.ServersDir(), "fr316-precheck-test-c3a5033b")
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "world"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "server.jar"), []byte("jar"), 0o644))

	// PID 记录 + socket 占位：wrapper 被强杀时其 defer cleanupPIDFile 不执行，二者遗留在 var/servers 下。
	pidPath := daemon.PIDFileName(root.ServersDir(), uuid)
	sockAddr := daemon.SocketAddr(root.ServersDir(), uuid)
	require.NoError(t, daemon.NewPIDFile(pidPath).WriteRecord(daemon.PIDRecord{
		WrapperPID:   wrapperPID,
		JavaPID:      javaPID,
		SocketAddr:   sockAddr,
		InstanceUUID: uuid,
		WorkDir:      workDir,
	}))
	writeSocketPlaceholder(t, sockAddr)

	require.True(t, daemon.IsPIDAlive(javaPID), "前置：java 替身应存活")
	require.True(t, daemon.IsPIDAlive(wrapperPID), "前置：wrapper 替身应存活")
	require.FileExists(t, pidPath)

	// 未注册实例（Worker 重启后未重推）：走 req.WorkDir 兜底解析，绕过运行态守卫，
	// 忠实复现「注册表已无、但进程树 + PID 记录仍在」的删除窗口。
	resp, err := srv.RemoveInstance(context.Background(), &workerpb.RemoveInstanceRequest{
		InstanceUuid: uuid,
		WorkDir:      "var/servers/fr316-precheck-test-c3a5033b",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, "删除应成功: %s", resp.Error)
	assert.False(t, resp.WorkDirSkipped)

	// 进程树必须死透（删目录前已确保），杜绝存活 Java 回写工作目录。
	assert.Eventually(t, func() bool {
		return !daemon.IsPIDAlive(javaPID) && !daemon.IsPIDAlive(wrapperPID)
	}, 4*time.Second, 50*time.Millisecond, "删除必须强杀整棵 daemon 进程树（含自成进程组的 Java）")

	assert.NoDirExists(t, workDir, "工作目录必须删除且不被存活进程回写")
	assert.NoFileExists(t, pidPath, "遗留 PID 文件必须清理")
	assertSocketGone(t, sockAddr)
}
