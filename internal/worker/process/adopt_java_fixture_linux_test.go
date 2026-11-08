//go:build linux

package process

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// FR-497③ 验收补足：真实 java 进程夹具的编译与启动辅助（Linux）。
//
// 为什么需要真 java（而不是 `sleep`）：本 FR 被登记为未验收的三项里，有两项恰好是 `sleep` 表达
// 不出来的——「健康巡检覆盖」要求实例真的监听（或不监听）某个端口，`sleep` 两者都不做，探针恒失败，
// 于是「巡检有没有检查到它」与「探针是否可达」两件事在断言上无法区分；「Stop 链路」要求被托管进程
// 真的消费 stdin 的 `stop` 并走优雅关服，`sleep` 不读 stdin，wrapper 只能等到优雅超时后强杀，
// 于是「走的是 wrapper 控制帧还是强杀」这个规格问题在 `sleep` 夹具下永远得不到答案。
//
// 编译成本取舍：每次跑到这些用例都调一次 javac 会拖慢 `go test`。故按**源文件内容哈希**缓存到
// 系统临时目录，正常开发只有源改动后第一次真编。缓存跨进程共享，多个用例、多次运行共用。
// 没有 java/javac（CI 常无 JDK）时一律 t.Skip——夹具不得把 CI 变红。

// mcStubSource 返回夹具源文件路径（testdata 下，随仓库提交）。
func mcStubSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join("testdata", "mcstub", "MinecraftServerStub.java")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("java 夹具源文件缺失：%s（应随仓库提交）", path)
	}
	return path
}

// javaToolchain 返回 (java, javac) 可执行文件路径；任一缺失即跳过用例。
//
// 用 exec.LookPath 而非固定路径：开发机可能只有 java（JRE）没有 javac，那时编译型夹具必须跳过，
// 不能报失败。JDK 装在 PATH 之外的机器可通过 JM_TEST_JAVA_HOME 覆盖。
func javaToolchain(t *testing.T) (string, string) {
	t.Helper()
	bin := ""
	if home := os.Getenv("JM_TEST_JAVA_HOME"); home != "" {
		bin = filepath.Join(home, "bin") + string(os.PathSeparator)
	}
	java, err := exec.LookPath(bin + "java")
	if err != nil {
		t.Skip("未找到 java，跳过真实 java 进程验收（不影响 CI）")
	}
	javac, err := exec.LookPath(bin + "javac")
	if err != nil {
		t.Skip("未找到 javac（仅有 JRE），跳过真实 java 进程验收（不影响 CI）")
	}
	return java, javac
}

// compileMCStub 编译 java 夹具并返回 classes 目录路径。
//
// 缓存在 os.TempDir()/jm-mcstub-<内容哈希>/ 下：源文件一改哈希即变，不会读到陈旧 class。
func compileMCStub(t *testing.T) string {
	t.Helper()
	_, javac := javaToolchain(t)
	src := mcStubSource(t)
	body, err := os.ReadFile(src)
	require.NoError(t, err)

	sum := sha256.Sum256(body)
	outDir := filepath.Join(os.TempDir(), "jm-mcstub-"+hex.EncodeToString(sum[:8]))
	classFile := filepath.Join(outDir, "MinecraftServerStub.class")
	if _, statErr := os.Stat(classFile); statErr == nil {
		return outDir // 已编译过且源未变
	}
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	cmd := exec.Command(javac, "-encoding", "UTF-8", "-d", outDir, src)
	cmd.Env = filterJavaToolOptions(os.Environ())
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "javac 编译 java 夹具失败：%s", string(out))
	if _, statErr := os.Stat(classFile); statErr != nil {
		t.Fatalf("javac 未产出 %s：%v", classFile, statErr)
	}
	return outDir
}

// filterJavaToolOptions 去掉 JAVA_TOOL_OPTIONS——开发机常把它设成 -Djava.io.tmpdir=...，
// JVM 会把它打到 stderr 首行并污染夹具的输出断言（真实 MC 服务端不打这一行）。
func filterJavaToolOptions(env []string) []string {
	const key = "JAVA_TOOL_OPTIONS="
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// freeTCPPort 申请一个空闲 TCP 端口并立即释放，供夹具监听使用。
//
// 理想做法是端口 0 让内核分配，但夹具端口要写进 PID 记录并参与巡检，必须在启动前就知道；
// 故用「先占后放」的经典手法。竞态窗口（放掉后被别人抢走）在测试环境里可以接受，且夹具监听
// 失败会让用例以「端口未就绪」的形式明确失败，不会伪装成通过。
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// mcStubStartCommand 构造「以 java 跑夹具」的启动命令串（wrapper 的 StartCommand 是 shell 串）。
//
// port>0 时夹具会听 `127.0.0.1:port/metrics`（响应维度可达形态）；port<=0 时完全不起监听
// （不可达形态，用于验假死判定）。logPath 非空时夹具把每行日志同时追写进该文件（模拟服务端
// 自己的 logs/latest.log，是「停止是否走优雅关服」唯一可靠的观测面，理由见 java 源文件注释）。
// shellQuote 复用 shutdown_cgroup_test.go 里已有的同包助手（单引号包裹 + 转义）。
func mcStubStartCommand(t *testing.T, classes string, port int, name, logPath string) string {
	t.Helper()
	java, _ := javaToolchain(t)
	cmd := java + " -cp " + shellQuote(classes) + " MinecraftServerStub --name " + shellQuote(name)
	if port > 0 {
		cmd += " --port " + strconv.Itoa(port)
	}
	if logPath != "" {
		cmd += " --log " + shellQuote(logPath)
	}
	return cmd
}

// waitFileContains 等文件出现并包含 want 子串，返回等待结束时的完整内容。
func waitFileContains(t *testing.T, path, want string, timeout time.Duration) string {
	t.Helper()
	var last string
	require.Eventuallyf(t, func() bool {
		b, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		last = string(b)
		return strings.Contains(last, want)
	}, timeout, 50*time.Millisecond, "文件 %s 未在 %s 内出现 %q，当前内容：\n%s", path, timeout, want, last)
	b, _ := os.ReadFile(path)
	return string(b)
}
