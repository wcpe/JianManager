package process

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// TestDaemonStrategy_StopSendsDisableRestartFirst 守护「停止必须先禁用自动重启」。
//
// 缺陷现场：wrapper 的自动重启判定基于启动期快照的 cfg.AutoRestart 与粘性开关。若停止只下发
// stop 帧，Java 先于 stop 生效而退出时 wrapper 仍会按策略把它拉起来——真机现象即「手动 stop
// 之后 daemon 又把进程自动拉起过一次（短暂启动后退出）」。故停止的第一个控制帧必须是
// disable_restart，且禁用只作用于当前 wrapper（下次启动 spawn 全新 wrapper，不污染后续运行）。
//
// 用一个只记录帧的替身监听者确定性地观测帧序，不依赖真实 wrapper 的时序。
func TestDaemonStrategy_StopSendsDisableRestartFirst(t *testing.T) {
	pidDir := shortDaemonPIDDir(t)
	uuid := "daemon-stop-disable-" + filepath.Base(pidDir)
	addr := daemon.SocketAddr(pidDir, uuid)

	ln, err := daemon.Listen(addr)
	require.NoError(t, err)
	defer ln.Close()

	frames := make(chan string, 8)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		for {
			f, decodeErr := daemon.Decode(conn)
			if decodeErr != nil {
				return
			}
			frames <- string(f.Payload)
		}
	}()

	mgr := NewManager(pidDir)
	d := newDaemonStrategy(mgr, CommandSpec{UUID: uuid, WorkDir: pidDir, ProcessType: ProcessTypeDaemon})
	require.NoError(t, d.Reconnect(addr), "连接替身监听者失败")

	require.NoError(t, d.Stop())

	got := make([]string, 0, 2)
	deadline := time.After(3 * time.Second)
	for len(got) < 2 {
		select {
		case f := <-frames:
			got = append(got, f)
		case <-deadline:
			t.Fatalf("未在超时内收到两个控制帧，已收到 %v", got)
		}
	}
	assert.Equal(t, daemon.ControlDisableRestart, got[0],
		"停止的第一个控制帧必须是禁用自动重启，否则 wrapper 会把 Java 再拉起来")
	assert.Equal(t, daemon.ControlStop, got[1], "随后才是优雅停止帧")

	_ = d.Close()
}
