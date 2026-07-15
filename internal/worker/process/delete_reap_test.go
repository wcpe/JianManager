package process

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// FR-310 删除路径 reap 单测：删除运行中 daemon 实例前，应按 PID 记录强杀 wrapper 与 Java 两棵进程树
// （Unix 上 Java 自成进程组，只杀 wrapper 够不到它），死透后清理遗留的 PID 文件 / socket；杀不死则保留
// PID 文件待接管扫描再兜底；无 PID 文件（非 daemon / 已自清理）为空操作。杀树 / 存活探测 / 等待经桩替身。
func TestReapDaemonForDelete(t *testing.T) {
	const (
		wrapperPID = 5101
		javaPID    = 5102
	)
	tests := []struct {
		name          string
		hasPIDFile    bool
		wrapperPID    int
		javaPID       int
		killMakesDead bool
		killErr       error
		wantKilled    []int
		wantCleaned   bool // 期望 PID 文件与 socket 被清理
	}{
		{
			name:          "存活 daemon 树→强杀 wrapper+java 两棵→死透→清理 pid/sock",
			hasPIDFile:    true,
			wrapperPID:    wrapperPID,
			javaPID:       javaPID,
			killMakesDead: true,
			wantKilled:    []int{wrapperPID, javaPID},
			wantCleaned:   true,
		},
		{
			name:          "杀不死（权限不足）→保留 pid/sock 待接管扫描兜底",
			hasPIDFile:    true,
			wrapperPID:    wrapperPID,
			javaPID:       javaPID,
			killErr:       errors.New("operation not permitted"),
			killMakesDead: false,
			wantKilled:    []int{wrapperPID, javaPID},
			wantCleaned:   false,
		},
		{
			name:          "wrapper 与 java 同 PID→只杀一棵→死透→清理",
			hasPIDFile:    true,
			wrapperPID:    wrapperPID,
			javaPID:       wrapperPID,
			killMakesDead: true,
			wantKilled:    []int{wrapperPID},
			wantCleaned:   true,
		},
		{
			name:        "无 PID 文件（非 daemon / 已自清理）→空操作",
			hasPIDFile:  false,
			wantKilled:  nil,
			wantCleaned: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			const uuid = "del-reap"
			pidPath := filepath.Join(dir, uuid+".pid")
			sockPath := filepath.Join(dir, uuid+".sock")
			// socket 清理仅 Unix 有文件语义（daemon.RemoveSocket 删文件）；Windows 走命名管道无文件
			// （RemoveSocket 为空操作），故 socket 断言仅在非 Windows 生效。
			unixSock := runtime.GOOS != "windows"
			if tt.hasPIDFile {
				require.NoError(t, daemon.NewPIDFile(pidPath).WriteRecord(daemon.PIDRecord{
					WrapperPID:   tt.wrapperPID,
					JavaPID:      tt.javaPID,
					SocketAddr:   sockPath,
					InstanceUUID: uuid,
					WorkDir:      dir,
				}))
				if unixSock {
					require.NoError(t, os.WriteFile(sockPath, []byte{}, 0o600))
				}
			}

			m := NewManager(dir)
			dead := map[int]bool{}
			var killed []int
			m.recoverPIDAlive = func(pid int) bool { return !dead[pid] }
			m.recoverSleep = func(time.Duration) {}
			m.recoverKillTree = func(pid int) error {
				killed = append(killed, pid)
				if tt.killMakesDead {
					dead[pid] = true
				}
				return tt.killErr
			}

			m.ReapDaemonForDelete(uuid)

			assert.Equal(t, tt.wantKilled, killed, "应先杀 wrapper 树再补杀 Java 树（同 PID 时只杀一棵）")
			if !tt.hasPIDFile {
				return
			}
			if tt.wantCleaned {
				assert.NoFileExists(t, pidPath, "死透后应清理 PID 文件")
				if unixSock {
					assert.NoFileExists(t, sockPath, "死透后应清理 socket")
				}
			} else {
				assert.FileExists(t, pidPath, "未死透应保留 PID 文件待接管扫描再兜底")
				if unixSock {
					assert.FileExists(t, sockPath, "未死透应保留 socket")
				}
			}
		})
	}
}
