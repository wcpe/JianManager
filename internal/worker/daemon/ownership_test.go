package daemon

import (
	"net"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortSockDir 返回短路径目录：Unix domain socket 的 sun_path 有 108 字节上限，
// 而名字较长的用例其 t.TempDir() 可能把 SocketAddr 顶穿。
func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "jm-daemon-own-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// serveProbeConnections 驱动监听器的 Accept 循环，模拟真实 wrapper 对 Unix Socket/Named Pipe 的服务状态。
// Windows 的 npipe.Listen 只创建管道实例，若不调用 Accept，客户端拨号不会成功。
func serveProbeConnections(ln net.Listener) {
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
}

// TestWrapper_CleanupPIDFileRespectsOwnership 守护「退出清理的归属校验」。
//
// 缺陷现场：wrapper 与实例不是一对一（快速 stop→start 会换新 wrapper，而旧 wrapper 可能晚于
// 新 wrapper 才退出）。旧实现无条件删除 PID 文件与 socket 文件，于是先退出的旧 wrapper 会把新
// wrapper 刚写入的 PID 记录、以及它正在监听的 socket 一并抹掉——新实例从此既无法被 PID 文件
// 发现（WaitForPriorExit 误判「上一代已清理」直接放行），也无法按路径拨通，控制通道形同失联。
func TestWrapper_CleanupPIDFileRespectsOwnership(t *testing.T) {
	t.Run("记录已易主时不得清理", func(t *testing.T) {
		pidDir := shortSockDir(t)
		uuid := "own-other"
		pidPath := PIDFileName(pidDir, uuid)
		addr := SocketAddr(pidDir, uuid)
		// 记录指向「别的 wrapper」（用 1 号进程代表外部 pid），并造出待清理的 socket 文件。
		require.NoError(t, NewPIDFile(pidPath).WriteRecord(PIDRecord{
			WrapperPID:   1,
			InstanceUUID: uuid,
			SocketAddr:   addr,
		}))
		if runtime.GOOS != "windows" {
			require.NoError(t, os.WriteFile(addr, nil, 0o600))
		}

		w := &Wrapper{
			cfg:     WrapperConfig{InstanceUUID: uuid, PIDDir: pidDir},
			pidFile: NewPIDFile(pidPath),
			addr:    addr,
		}
		w.cleanupPIDFile()

		_, err := os.Stat(pidPath)
		assert.NoError(t, err, "PID 记录已易主，旧 wrapper 不得删除它")
		if runtime.GOOS != "windows" {
			_, err = os.Stat(addr)
			assert.NoError(t, err, "PID 记录已易主，旧 wrapper 不得删除自己已不拥有的 socket 文件")
		} else {
			t.Log("Windows 使用命名管道，没有可由 os.Stat 验证的 socket 文件；RemoveSocket 本身为空操作")
		}
	})

	t.Run("记录仍属于自己时正常清理", func(t *testing.T) {
		pidDir := shortSockDir(t)
		uuid := "own-self"
		pidPath := PIDFileName(pidDir, uuid)
		addr := SocketAddr(pidDir, uuid)
		require.NoError(t, NewPIDFile(pidPath).WriteRecord(PIDRecord{
			WrapperPID:   os.Getpid(),
			InstanceUUID: uuid,
			SocketAddr:   addr,
		}))
		if runtime.GOOS != "windows" {
			require.NoError(t, os.WriteFile(addr, nil, 0o600))
		}

		w := &Wrapper{
			cfg:     WrapperConfig{InstanceUUID: uuid, PIDDir: pidDir},
			pidFile: NewPIDFile(pidPath),
			addr:    addr,
		}
		w.cleanupPIDFile()

		_, err := os.Stat(pidPath)
		assert.True(t, os.IsNotExist(err), "仍属于自己的记录应被清理")
		if runtime.GOOS != "windows" {
			_, err = os.Stat(addr)
			assert.True(t, os.IsNotExist(err), "仍属于自己的 socket 文件应被清理")
		} else {
			t.Log("Windows 使用命名管道，没有可由 os.Stat 验证的 socket 文件；RemoveSocket 本身为空操作")
		}
	})
}

// TestWrapper_OwnsPIDRecord 守护「本 wrapper 是否仍是记录主人」的判定：只在记录明确指向别的
// wrapper 时才判否——记录缺失/无 wrapper pid 一律按「是」处理，不改变既有的自动重启行为。
func TestWrapper_OwnsPIDRecord(t *testing.T) {
	t.Run("记录指向自己", func(t *testing.T) {
		pidDir := shortSockDir(t)
		uuid := "owns-self"
		pidPath := PIDFileName(pidDir, uuid)
		require.NoError(t, NewPIDFile(pidPath).WriteRecord(PIDRecord{WrapperPID: os.Getpid(), InstanceUUID: uuid}))
		w := &Wrapper{cfg: WrapperConfig{InstanceUUID: uuid}, pidFile: NewPIDFile(pidPath)}
		assert.True(t, w.ownsPIDRecord())
	})

	t.Run("记录指向别的 wrapper", func(t *testing.T) {
		pidDir := shortSockDir(t)
		uuid := "owns-other"
		pidPath := PIDFileName(pidDir, uuid)
		require.NoError(t, NewPIDFile(pidPath).WriteRecord(PIDRecord{WrapperPID: 1, InstanceUUID: uuid}))
		w := &Wrapper{cfg: WrapperConfig{InstanceUUID: uuid}, pidFile: NewPIDFile(pidPath)}
		assert.False(t, w.ownsPIDRecord(), "记录已易主即判否：自动重启必须让位给新 wrapper")
	})

	t.Run("无记录时按自己是主人处理", func(t *testing.T) {
		pidDir := shortSockDir(t)
		uuid := "owns-none"
		w := &Wrapper{cfg: WrapperConfig{InstanceUUID: uuid}, pidFile: NewPIDFile(PIDFileName(pidDir, uuid))}
		assert.True(t, w.ownsPIDRecord(), "记录缺失不得改变既有的自动重启行为")
	})
}

// TestSocketServed 守护「按 socket 实探是否已有 wrapper 在托管」。
//
// 这是 daemon 策略 spawn 新 wrapper 前的纵深防御：PID 文件可能缺失/损坏，此时等待逻辑会误判
// 「上一代已清理」直接放行；而 socket 是实例级唯一地址，能拨通就说明确有 wrapper 在托管。
func TestSocketServed(t *testing.T) {
	pidDir := shortSockDir(t)
	uuid := "sock-served"
	addr := SocketAddr(pidDir, uuid)

	assert.False(t, SocketServed(pidDir, uuid), "无监听者时应判否（正常重启不得被误拒）")

	ln, err := Listen(addr)
	require.NoError(t, err)
	serveProbeConnections(ln)
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = ln.Close()
		}
	})

	assert.True(t, SocketServed(pidDir, uuid), "有人在监听时应判真：新 wrapper 必须让位")

	if err := ln.Close(); err != nil && runtime.GOOS != "windows" {
		require.NoError(t, err)
	}
	closed = true
	assert.False(t, SocketServed(pidDir, uuid), "监听关闭后应判否")
}
