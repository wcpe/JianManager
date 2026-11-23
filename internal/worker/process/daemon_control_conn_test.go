package process

// 本文件覆盖 Worker 与 daemon wrapper 之间**控制长连接**的韧性：
// 连接被外部拨号替换后可自愈、并发下发不互相替换、帧写入不被并发交错。
// 证据采集侧的「探活不得有副作用」见 evidence_probe_conn_test.go。

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/daemon"
)

const daemonControlStopFileEnv = "JIANMANAGER_CONTROL_TEST_STOP_FILE"

// TestDaemonControlFixture 是控制连接测试的 stdin 消费替身，独立停止文件使清理不依赖控制连接健康。
func TestDaemonControlFixture(t *testing.T) {
	stopFile := os.Getenv(daemonControlStopFileEnv)
	if stopFile == "" {
		return
	}
	if err := os.WriteFile(stopFile+".ready", nil, 0600); err != nil {
		os.Exit(2)
	}
	go func() { _, _ = io.Copy(io.Discard, os.Stdin) }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(stopFile); err == nil {
			os.Exit(0)
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			os.Exit(1)
		}
	}
}

func daemonControlFixtureCommand(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	if runtime.GOOS == "windows" {
		// 让 cmd 展开带引号的路径，避免 Go 的命令行转义把引号变成字面量反斜杠。
		return `%JIANMANAGER_CONTROL_TEST_EXECUTABLE% -test.run=TestDaemonControlFixture$`
	}
	return "'" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestDaemonControlFixture$"
}

func daemonControlFixtureEnv(t *testing.T, stopFile string) map[string]string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	return map[string]string{
		daemonControlStopFileEnv:              stopFile,
		"JIANMANAGER_CONTROL_TEST_EXECUTABLE": `"` + executable + `"`,
	}
}

// TestDaemonControlFixtureIndependentStop 验证控制通道不可达时，独立停止文件仍可结束替身进程。
func TestDaemonControlFixtureIndependentStop(t *testing.T) {
	stopFile := filepath.Join(t.TempDir(), "control-fixture.stop")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", daemonControlFixtureCommand(t))
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", daemonControlFixtureCommand(t))
	}
	cmd.Env = os.Environ()
	for key, value := range daemonControlFixtureEnv(t, stopFile) {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var output []byte
	done := make(chan error, 1)
	go func() {
		var err error
		output, err = cmd.CombinedOutput()
		done <- err
		close(done)
	}()
	defer func() {
		_ = os.WriteFile(stopFile, nil, 0600)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("独立停止信号回归测试未回收替身")
		}
	}()
	require.Eventually(t, func() bool {
		_, err := os.Stat(stopFile + ".ready")
		return err == nil
	}, 3*time.Second, 20*time.Millisecond, "替身必须先实际启动，再验证独立停止")
	require.NoError(t, os.WriteFile(stopFile, nil, 0600))
	select {
	case err := <-done:
		require.NoError(t, err, "独立停止信号应使替身自行退出，输出: %s", output)
	case <-ctx.Done():
		t.Fatal("替身未响应独立停止信号")
	}
}

func cleanupDaemonWrapper(t *testing.T, pidDir, uuid string, done <-chan error) {
	t.Helper()

	// 即使控制通道完全失效，替身也会自行退出；done 随后证明 wrapper 已完成 cmd.Wait。
	if err := os.WriteFile(filepath.Join(pidDir, "control-fixture.stop"), nil, 0600); err != nil {
		t.Errorf("写入替身独立停止信号失败: %v", err)
	}
	// 不根据 PID 文件盲杀：这里 wrapper 与测试同进程，Unix 按组终止可能误杀测试宿主。
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatalf("daemon wrapper %s 未在清理期限内回收替身并退出", uuid)
	}
}

// TestDaemonStrategy_SendCommandRecoversFromLostControlConnection 是同一事故的**韧性子**回归：
// 控制长连接被外部原因踢掉（本机 jmctl 应急客户端拨号、探针拨号、wrapper 侧异常）后，
// 旧实现只把错误抛给调用方，d.conn 仍指向死 socket，于是 console 命令**永久** broken pipe，
// 只能重启整个 Worker——实测 12 个实例全部失联。此处要求下发命令能自行重连并成功送达。
func TestDaemonStrategy_SendCommandRecoversFromLostControlConnection(t *testing.T) {
	t.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "1s")
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "1s")

	pidDir := shortDaemonPIDDir(t)
	uuid := "daemon-sendcmd-recover-" + filepath.Base(pidDir)
	cfg := daemon.WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: daemonControlFixtureCommand(t),
		EnvVars:      daemonControlFixtureEnv(t, filepath.Join(pidDir, "control-fixture.stop")),
		WorkDir:      pidDir,
		AutoRestart:  false,
		PIDDir:       pidDir,
	}
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- daemon.RunWithReady(cfg, ready)
	}()
	t.Cleanup(func() {
		cleanupDaemonWrapper(t, pidDir, uuid, done)
	})
	waitDaemonWrapperReady(t, ready, done)

	pidPath := filepath.Join(pidDir, uuid+".pid")
	require.Eventually(t, func() bool {
		rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
		return err == nil && rec.JavaPID > 0 && daemon.IsPIDAlive(rec.JavaPID)
	}, 15*time.Second, 50*time.Millisecond, "应能读到存活的被托管子进程 pid")

	mgr := NewManager(pidDir)
	recovered, err := mgr.RecoverDaemonInstances()
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	d, ok := inst.strategy.(*daemonStrategy)
	require.True(t, ok)
	require.NoError(t, d.SendCommand("say baseline"), "基线：接管后命令应可送达")

	// 外部客户端拨号 → wrapper 关闭旧连接（这正是生产上让 12 个实例失联的同一机制）。
	intruder, err := daemon.Dial(daemon.SocketAddr(pidDir, uuid))
	require.NoError(t, err)
	defer intruder.Close()
	require.Eventually(t, func() bool {
		return d.sendControl(daemon.ControlPing) != nil
	}, 3*time.Second, 20*time.Millisecond, "原控制长连接应已被外部拨号替换为失效连接")

	// 关键断言：此时 SendCommand 必须自愈（重连后送达），而不是把 broken pipe 抛给运维。
	require.NoError(t, d.SendCommand("say after-reconnect"),
		"控制长连接失效后 SendCommand 应自行重连并送达，而不是永久 broken pipe")
}

// TestDaemonStrategy_ConcurrentSendCommandAfterConnectionLoss 覆盖并发下发：
// 控制长连接被外部拨号替换后，多个调用方可能同时发现连接失效并各自重连。
// wrapper 只保留最新连接，若并发拨号不被串行化，后拨者会踢掉先拨者刚登记的连接，
// 造成命令丢失或反复重连。本用例要求并发下发全部成功，且在 -race 下无数据竞争
// （sendFrame 读 d.conn 与 reconnectAndSend/readLoop 写 d.conn 必须遵循同一加锁纪律）。
//
// 夹具必须持续消费 stdin，而非使用 keepAliveCmd（ping/sleep）：
// 本用例并发写入 16×8=128 条 1KiB 命令，替身若不读 stdin，第 4 条即塞满 stdin 管道缓冲
// （约 4KiB），此后写入阻塞至替身寿命结束（ping -n 30 场景 ≈28.9s），退出瞬间缓冲丢失，
// 与测试收尾竞态——Windows 真机复验中该夹具假象稳定复现（128 条写入耗时 28.9s / 589µs
// 两组对照，见 .tmp/acceptance/r2_stdin_probe.log）。测试替身持续丢弃 stdin，保持同样的消费语义，
// 并通过独立停止文件保证控制连接失效后仍可退出。
func TestDaemonStrategy_ConcurrentSendCommandAfterConnectionLoss(t *testing.T) {
	t.Setenv("JIANMANAGER_GRACEFUL_STOP_TIMEOUT", "1s")
	t.Setenv("JIANMANAGER_START_WAIT_PRIOR_EXIT_TIMEOUT", "1s")

	pidDir := shortDaemonPIDDir(t)
	uuid := "daemon-concurrent-send-" + filepath.Base(pidDir)
	cfg := daemon.WrapperConfig{
		InstanceUUID: uuid,
		StartCommand: daemonControlFixtureCommand(t),
		EnvVars:      daemonControlFixtureEnv(t, filepath.Join(pidDir, "control-fixture.stop")),
		WorkDir:      pidDir,
		AutoRestart:  false,
		PIDDir:       pidDir,
	}
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- daemon.RunWithReady(cfg, ready)
	}()
	t.Cleanup(func() {
		cleanupDaemonWrapper(t, pidDir, uuid, done)
	})
	waitDaemonWrapperReady(t, ready, done)

	pidPath := filepath.Join(pidDir, uuid+".pid")
	require.Eventually(t, func() bool {
		rec, err := daemon.NewPIDFile(pidPath).ReadRecord()
		return err == nil && rec.JavaPID > 0 && daemon.IsPIDAlive(rec.JavaPID)
	}, 15*time.Second, 50*time.Millisecond, "应能读到存活的被托管子进程 pid")

	mgr := NewManager(pidDir)
	recovered, err := mgr.RecoverDaemonInstances()
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	inst, ok := mgr.GetInstance(uuid)
	require.True(t, ok)
	d, ok := inst.strategy.(*daemonStrategy)
	require.True(t, ok)
	require.NoError(t, d.SendCommand("say baseline"))

	// 外部拨号替换长连接（jmctl / 探针的同一机制）。
	intruder, err := daemon.Dial(daemon.SocketAddr(pidDir, uuid))
	require.NoError(t, err)
	defer intruder.Close()
	require.Eventually(t, func() bool {
		return d.sendControl(daemon.ControlPing) != nil
	}, 3*time.Second, 20*time.Millisecond, "原控制长连接应已被替换为失效连接")

	// 并发下发：全部必须成功（各自重连、或被串行化后复用同一条新连接）。
	//
	// 规模与载荷刻意放大：Frame.Encode 分 5 次 Write，若并发写入不被互斥，两次 Encode
	// 会交错成坏帧（wrapper 解码失败即关闭该连接）。交错需要两次 Encode 在多次 syscall
	// 之间被调度穿插，窗口很窄——用小批量+短命令几乎撞不上，故这里用较多并发与 1KiB
	// 级命令把窗口拉宽，使该缺陷可被稳定复现（撤销写互斥后本用例会失败）。
	const senders, perSender = 16, 8
	longCommand := "say " + strings.Repeat("x", 1024)
	errs := make([]error, senders*perSender)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for s := 0; s < senders; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			<-start
			for i := 0; i < perSender; i++ {
				errs[s*perSender+i] = d.SendCommand(longCommand)
			}
		}(s)
	}
	close(start)
	sendersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(sendersDone)
	}()
	t.Cleanup(func() {
		select {
		case <-sendersDone:
			return
		default:
		}
		// 先独立回收私有替身，不让策略的关闭等待挡住夹具收尾。
		strategyClosed := make(chan struct{})
		go func() {
			defer close(strategyClosed)
			_ = d.Close()
		}()
		cleanupDaemonWrapper(t, pidDir, uuid, done)
		select {
		case <-strategyClosed:
		case <-time.After(5 * time.Second):
			t.Error("压力测试控制策略关闭超时")
		}
		select {
		case <-sendersDone:
		case <-time.After(5 * time.Second):
			t.Error("压力写入收尾超时：尚未登记的在途重连无法由测试直接取消")
		}
	})
	select {
	case <-sendersDone:
	case <-time.After(15 * time.Second):
		t.Fatal("并发下发未在期限内完成，触发替身与控制连接清理")
	}

	for i, err := range errs {
		require.NoError(t, err, "并发下发第 %d 条不应失败（不得因并发重连互相替换而丢命令）", i)
	}
	// 收敛后控制通道仍可用。
	require.NoError(t, d.sendControl(daemon.ControlPing), "并发重连后控制长连接应仍可用")
}

// TestDaemonStrategy_WriteFrameSerializesEncoding 直接锁定「帧写入必须整体互斥」这一不变量。
//
// 背景：`daemon.Frame.Encode` 分 **5 次 Write**（Channel/Type/Flags/Length + 载荷）。控制连接由
// 多个调用方共享（console 命令、Kill/Stop、并发重连），若写入不互斥，两次 Encode 会交错成坏帧；
// wrapper 侧解码失败随即关闭该连接——命令方各自看到「写入成功」，通道却已失效。
//
// 为什么用 net.Pipe 且「先收全量再解析」：pipe 的 Write 会阻塞至对端读取，两个并发 Encode 的
// 多次 Write 因此必然有机会互相穿插，使「交错」从偶发变为可稳定复现；而先由 io.ReadAll 收全
// 字节、再逐帧解析，可避免交错时解码器停在永不满足的长度上挂死——失败必然表现为明确断言。
func TestDaemonStrategy_WriteFrameSerializesEncoding(t *testing.T) {
	d := &daemonStrategy{}
	frameA := &daemon.Frame{Header: daemon.Header{Channel: daemon.ChannelStdin, Type: daemon.TypeData},
		Payload: []byte("AAAA-" + strings.Repeat("a", 64))}
	frameB := &daemon.Frame{Header: daemon.Header{Channel: daemon.ChannelControl, Type: daemon.TypeCommand},
		Payload: []byte("BBBB-" + strings.Repeat("b", 64))}

	const rounds = 20
	for round := 0; round < rounds; round++ {
		server, client := net.Pipe()
		defer server.Close()
		defer client.Close()
		require.NoError(t, server.SetReadDeadline(time.Now().Add(5*time.Second)))
		require.NoError(t, client.SetWriteDeadline(time.Now().Add(5*time.Second)))
		writeErr := make(chan error, 2)
		go func() {
			var wg sync.WaitGroup
			for _, f := range []*daemon.Frame{frameA, frameB} {
				wg.Add(1)
				go func(f *daemon.Frame) {
					defer wg.Done()
					writeErr <- d.writeFrame(client, f)
				}(f)
			}
			wg.Wait()
			_ = client.Close() // 让读侧收到 EOF，收集完整字节流
		}()

		raw, err := io.ReadAll(server)
		require.NoError(t, err)
		rd := bytes.NewReader(raw)
		for i := 0; i < 2; i++ {
			fr, err := daemon.Decode(rd)
			require.NoError(t, err, "第 %d 轮第 %d 帧无法解码（并发写入被交错）", round, i)
			body := string(fr.Payload)
			require.True(t, strings.HasPrefix(body, "AAAA-") || strings.HasPrefix(body, "BBBB-"),
				"第 %d 轮第 %d 帧载荷异常: %q", round, i, body)
			require.False(t, strings.Contains(body, "a") && strings.Contains(body, "b"),
				"第 %d 轮第 %d 帧载荷由两次写入交错而成: %q", round, i, body)
		}
		require.Zero(t, rd.Len(), "第 %d 轮流中存在多余字节（写入被交错）", round)
		for i := 0; i < 2; i++ {
			select {
			case err := <-writeErr:
				require.NoError(t, err)
			case <-time.After(1 * time.Second):
				t.Fatalf("第 %d 轮第 %d 次帧写入未在截止时间内完成", round, i)
			}
		}
	}
}
