//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package eventstore

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 硬句柄限额只在隔离测试子进程下降低，不影响主测试进程或机器配置。
func TestSnapshotManySegmentsWithHardDescriptorLimit(t *testing.T) {
	if os.Getenv("EVENTSTORE_NOFILE_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSnapshotManySegmentsWithHardDescriptorLimit$", "-test.v")
		command.Env = append(os.Environ(), "EVENTSTORE_NOFILE_CHILD=1")
		output, err := command.CombinedOutput()
		t.Logf("低句柄限额子进程输出：\n%s", output)
		require.NoError(t, err)
		return
	}
	require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: 64, Max: 64}))
	var limit syscall.Rlimit
	require.NoError(t, syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit))
	require.EqualValues(t, 64, limit.Cur)
	require.EqualValues(t, 64, limit.Max)
	root := t.TempDir()
	key := "inst:1/file/g1"
	store, err := Open(root)
	require.NoError(t, err)
	store.SetMaxSegmentBytes(1)
	for i := 0; i < 96; i++ {
		require.NoError(t, store.Append(key, []logtypes.Event{ev(i)}))
	}
	require.NoError(t, store.Close())
	reopened, err := Open(root)
	require.NoError(t, err)
	count, err := reopened.Count(key)
	require.NoError(t, err)
	require.Equal(t, 96, count)
	var snapshots []*ReadSnapshot
	for i := 0; i < 12; i++ {
		snapshot, err := reopened.Snapshot(key)
		require.NoError(t, err, "固定引用不能按历史段数长期占用句柄")
		snapshots = append(snapshots, snapshot)
	}
	require.NoError(t, reopened.Close())
	for _, snapshot := range snapshots {
		seen := 0
		require.NoError(t, snapshot.Iterate(func(event logtypes.Event) error {
			require.Equal(t, ev(seen).EventID, event.EventID)
			seen++
			return nil
		}))
		require.Equal(t, 96, seen)
		require.NoError(t, snapshot.Close())
	}
	t.Logf("hard NOFILE=%d，96 sealed段，12固定引用跨Store.Close，逐个读取全部事件成功", limit.Max)
}
