package grpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
)

// TestResourceSnapshotContexts_DiskBudgetNotTruncatedByProcessBudget N-5：磁盘统计的 10s 预算
// 必须**真正可达**，不得被进程快照的 3s 预算截断。
//
// 缺陷现场：GetInstanceResourceSnapshot 先用 3s 派生 snapshotCtx 并把它作为 wctx 传给
// snapshotInstanceProcessTree，instanceWorkDirBytes 再对它 WithTimeout(10s)——父预算 3s
// 才是实际生效值。后果：大目录遍历在 3s 被砍 → WorkDirAvailable=false → CP 侧
// quota_metric_source.go 只在 WorkDirAvailable 时填 DiskBytes，磁盘维度整维无值，
// 磁盘配额静默不生效；而注释仍声称「磁盘统计有独立预算」。
func TestResourceSnapshotContexts_DiskBudgetNotTruncatedByProcessBudget(t *testing.T) {
	rpcCtx, rpcCancel := context.WithTimeout(context.Background(), time.Minute)
	defer rpcCancel()

	processCtx, workDirCtx, cancel := resourceSnapshotContexts(rpcCtx)
	defer cancel()

	processDeadline, ok := processCtx.Deadline()
	require.True(t, ok, "进程快照 ctx 必须有 deadline")
	require.InDelta(t, instanceResourceSnapshotTimeout.Seconds(),
		time.Until(processDeadline).Seconds(), 1.0, "进程快照预算应为 3s")

	workDirDeadline, ok := workDirCtx.Deadline()
	require.True(t, ok, "磁盘统计 ctx 必须有 deadline")
	// 关键断言：远大于 3s。修复前该值为 3s（被父预算截断）。
	require.Greater(t, time.Until(workDirDeadline), instanceResourceSnapshotTimeout+time.Second,
		"磁盘统计预算不得被 3s 进程预算截断（修复前此处实际只剩 3s）")
	require.InDelta(t, instanceWorkDirSizeTimeout.Seconds(),
		time.Until(workDirDeadline).Seconds(), 1.0, "磁盘统计预算应为 10s")
}

// TestResourceSnapshotContexts_BoundedByRPCDedline 两个 ctx 仍受入站 RPC deadline 约束：
// 「独立预算」不等于「无限挂起」——CP 侧 quotaDiskTimeout=15s 的 deadline 必须继续生效。
func TestResourceSnapshotContexts_BoundedByRPCDedline(t *testing.T) {
	rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer rpcCancel()

	_, workDirCtx, cancel := resourceSnapshotContexts(rpcCtx)
	defer cancel()

	// 入站 deadline 更紧时以入站为准（10s 预算不能延长调用方给的 4s）。
	processDeadline, _ := workDirCtx.Deadline()
	require.LessOrEqual(t, time.Until(processDeadline), 4*time.Second+time.Millisecond,
		"磁盘统计不得越过入站 RPC deadline")

	// 入站 ctx 取消后两个派生 ctx 都要随之取消。
	rpcCancel()
	require.Eventually(t, func() bool {
		return workDirCtx.Err() != nil
	}, time.Second, 10*time.Millisecond, "入站 ctx 取消必须传播到磁盘统计 ctx")
}

// TestInstanceWorkDirBytes_SumsRegularFilesOnly 磁盘统计口径：只累加常规文件，
// 且目录不存在/不可读时返回 available=false（不得用 0 冒充「没占空间」）。
func TestInstanceWorkDirBytes_SumsRegularFilesOnly(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "world"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "server.jar"), make([]byte, 1024), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "world", "region.mca"), make([]byte, 2048), 0o644))

	mgr := process.NewManager(t.TempDir())
	srv := NewServer(mgr, "node-n5", nil, nil, nil)
	const uuid = "n5-disk"
	require.NoError(t, mgr.Create(uuid, "实例", "./run.sh", "", workDir, nil, false,
		process.ProcessTypeDaemon, "", "", 0, 0))

	ctx, cancel := context.WithTimeout(context.Background(), instanceWorkDirSizeTimeout)
	defer cancel()
	total, ok := srv.instanceWorkDirBytes(ctx, uuid)
	require.True(t, ok)
	require.EqualValues(t, 1024+2048, total, "只累加常规文件大小")

	// 实例未注册 → 不可用（不是 0 占用）。
	_, ok = srv.instanceWorkDirBytes(ctx, "n5-unknown")
	require.False(t, ok)

	// ctx 已取消 → 不再做遍历，返回不可用。
	cancelled, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, ok = srv.instanceWorkDirBytes(cancelled, uuid)
	require.False(t, ok)
}
