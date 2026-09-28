package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 记录必须指向**当前进程**且带可核对字段：pid/starttime/exe/cwd 任一缺失都会让读者
// 无法区分「PID 仍是那个 Worker」与「PID 被复用」（这正是生产遗留过期 worker.pid 的害处）。
func TestWriteWorkerPIDRecordPointsAtCurrentProcess(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	require.NoError(t, writeWorkerPIDRecord(dataDir, 19102, filepath.Join(root, "worker.yml")))

	path := filepath.Join(root, "logs", "worker.pid")
	info, err := os.Stat(path)
	require.NoError(t, err, "应在 data 的同级 logs/ 下写出记录")
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "记录含进程路径信息，权限应为 0600")

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			fields[k] = v
		}
	}

	require.Equal(t, strconv.Itoa(os.Getpid()), fields["pid"], "pid 必须是当前进程")
	if _, ok := fields["starttime"]; ok && fields["starttime"] != "" {
		require.Regexp(t, `^[0-9]+$`, fields["starttime"], "starttime 应为 clock tick 计数")
	}
	require.Equal(t, dataDir, fields["data_dir"])
	require.Equal(t, "19102", fields["ws_port"])
	require.Equal(t, filepath.Join(root, "worker.yml"), fields["config"])
	require.NotEmpty(t, fields["binary"])
	if exe, err := os.Executable(); err == nil {
		require.Equal(t, exe, fields["binary"])
	}
}

// 重复写必须覆盖旧记录：这正是「过期 PID 文件」的直接修复（旧内容不得残留）。
func TestWriteWorkerPIDRecordOverwritesStaleRecord(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	logDir := filepath.Join(root, "logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755))
	stale := filepath.Join(logDir, "worker.pid")
	require.NoError(t, os.WriteFile(stale, []byte("1921525\n"), 0o600))

	require.NoError(t, writeWorkerPIDRecord(dataDir, 19102, ""))

	b, err := os.ReadFile(stale)
	require.NoError(t, err)
	require.NotContains(t, string(b), "1921525", "旧 PID 不得残留")
	require.Contains(t, string(b), "pid="+strconv.Itoa(os.Getpid()))
}
