package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/worker/daemon"
)

// doubleCtrlCWindow 是「连按两次 Ctrl+C 升级为强杀」的判定窗口。
// 首次 Ctrl+C 发优雅 stop；窗口内再次 Ctrl+C 视为运营者确认强杀。
const doubleCtrlCWindow = 2 * time.Second

// sendStdin 把一行用户输入编码为 ChannelStdin 的 Data 帧写入 conn（自动补换行）。
// 镜像 Worker daemonStrategy.SendCommand 的帧约定，使 wrapper 原样转发到 MC 进程 stdin。
func sendStdin(w io.Writer, line string) error {
	fr := &daemon.Frame{
		Header:  daemon.Header{Channel: daemon.ChannelStdin, Type: daemon.TypeData},
		Payload: []byte(line + "\n"),
	}
	return fr.Encode(w)
}

// sendControl 把一条控制命令（stop/kill/ping）编码为 ChannelControl 的 Command 帧写入 conn。
// 镜像 Worker daemonStrategy.sendControl 的帧约定。
func sendControl(w io.Writer, cmd string) error {
	fr := &daemon.Frame{
		Header:  daemon.Header{Channel: daemon.ChannelControl, Type: daemon.TypeCommand},
		Payload: []byte(cmd),
	}
	return fr.Encode(w)
}

// streamOutput 持续从 conn 解码 daemon 回传帧，把 Stdout/Stderr 的 Data 帧分别写到 out/errw。
// 控制响应帧（如 ping 的 pong）忽略。连接断开（daemon 退出/EOF）时返回 nil——这是紧急控制台
// 「daemon 退出则自动退出」的语义来源。其余解码错误同样收敛为 nil（连接已不可用，退出即可）。
func streamOutput(conn io.Reader, out, errw io.Writer) error {
	for {
		fr, err := daemon.Decode(conn)
		if err != nil {
			// EOF / 连接断开：daemon 已退出，正常收尾。
			return nil
		}
		switch fr.Channel {
		case daemon.ChannelStdout:
			_, _ = out.Write(fr.Payload)
		case daemon.ChannelStderr:
			_, _ = errw.Write(fr.Payload)
		}
	}
}

// runEmergency 实现 `jmctl emergency`：交互式紧急终端。
// 无 --instance 时列出存活实例供选择；选定后 Dial socket，并发跑「读 socket→终端」「读
// stdin→socket」「Ctrl+C→stop/kill」三路；daemon 退出（读循环结束）即整体退出。
func runEmergency(args []string) error {
	fs := newFlagSet("emergency")
	instanceFlag := fs.String("instance", "", "目标实例 UUID 或其唯一前缀（省略则交互选择）")
	pidDirFlag := fs.String("pid-dir", "", "daemon PID 目录（默认数据根下 var/servers）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	pidDir, err := resolvePIDDir(*pidDirFlag)
	if err != nil {
		return err
	}
	insts, err := scanInstances(pidDir)
	if err != nil {
		return err
	}

	target, err := pickInstance(insts, *instanceFlag)
	if err != nil {
		return err
	}
	if !target.Alive {
		return fmt.Errorf("实例 %s 的 wrapper 进程（PID %d）已不存活，无法连接", target.UUID, target.WrapperPID)
	}

	conn, err := daemon.Dial(target.SocketAddr)
	if err != nil {
		return fmt.Errorf("连接实例 %s 的 socket 失败: %w", target.UUID, err)
	}
	defer conn.Close()

	printBanner(target)
	return interact(conn)
}

// pickInstance 确定 emergency 的目标实例：给了 --instance 走前缀补全；否则交互选择。
func pickInstance(insts []instanceInfo, instanceFlag string) (instanceInfo, error) {
	if strings.TrimSpace(instanceFlag) != "" {
		return resolvePrefix(insts, instanceFlag)
	}
	alive := aliveInstances(insts)
	if len(alive) == 0 {
		return instanceInfo{}, fmt.Errorf("当前没有存活的 daemon 实例可供连接")
	}
	return promptSelect(alive, os.Stdin, os.Stdout)
}

// aliveInstances 过滤出存活实例。
func aliveInstances(insts []instanceInfo) []instanceInfo {
	var alive []instanceInfo
	for _, in := range insts {
		if in.Alive {
			alive = append(alive, in)
		}
	}
	return alive
}

// promptSelect 在终端列出存活实例并读取用户选择（序号，从 1 起）。
func promptSelect(alive []instanceInfo, in io.Reader, out io.Writer) (instanceInfo, error) {
	fmt.Fprintln(out, "存活的 daemon 实例：")
	for i, inst := range alive {
		fmt.Fprintf(out, "  [%d] %s  (JavaPID %d)  %s\n", i+1, inst.UUID, inst.JavaPID, inst.WorkDir)
	}
	fmt.Fprintf(out, "请输入序号选择实例 (1-%d): ", len(alive))

	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return instanceInfo{}, fmt.Errorf("读取选择失败: %w", err)
	}
	idx, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || idx < 1 || idx > len(alive) {
		return instanceInfo{}, fmt.Errorf("无效的选择 %q", strings.TrimSpace(line))
	}
	return alive[idx-1], nil
}

// printBanner 打印连接后的提示头（对应 spec §3.4 终端示意）。
func printBanner(target instanceInfo) {
	fmt.Printf("[jmctl] 已连接实例 %s\n", target.UUID)
	fmt.Printf("[jmctl] Java PID: %d\n", target.JavaPID)
	fmt.Println("[jmctl] 输入命令发送到 MC 控制台")
	fmt.Println("[jmctl] Ctrl+C = 优雅关服，连按两次 = 强杀")
}

// interact 运行交互主循环：
//   - goroutine A：streamOutput 把 daemon stdout/stderr 打到终端；结束（daemon 退出）后通知主退出。
//   - goroutine B：逐行读本地 stdin，作为 stdin 帧发给 daemon。
//   - 主 goroutine：监听 SIGINT（Ctrl+C），首次发 stop、窗口内再次发 kill。
//
// 任一「daemon 退出」事件（读循环结束）触发整体退出。
func interact(conn net.Conn) error {
	done := make(chan struct{})

	// A：daemon→终端。读循环结束即 daemon 退出。
	go func() {
		_ = streamOutput(conn, os.Stdout, os.Stderr)
		fmt.Println("\n[jmctl] 连接已断开，daemon 已退出")
		close(done)
	}()

	// B：终端 stdin→daemon。stdin 关闭/出错则停止转发（不视为退出，daemon 仍在跑）。
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			if err := sendStdin(conn, scanner.Text()); err != nil {
				return
			}
		}
	}()

	// 主：Ctrl+C → stop / 连按 kill。
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	var lastInt time.Time
	for {
		select {
		case <-done:
			return nil
		case <-sigCh:
			now := time.Now()
			if !lastInt.IsZero() && now.Sub(lastInt) <= doubleCtrlCWindow {
				fmt.Println("\n[jmctl] 已发送 kill 命令，强制终止...")
				_ = sendControl(conn, daemon.ControlKill)
			} else {
				fmt.Println("\n[jmctl] 已发送 stop 命令，等待关服...（再次 Ctrl+C 强杀）")
				_ = sendControl(conn, daemon.ControlStop)
			}
			lastInt = now
		}
	}
}
