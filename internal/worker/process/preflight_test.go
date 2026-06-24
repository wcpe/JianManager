package process

import (
	"strings"
	"testing"
)

// withJavaProbe 临时替换 java 版本探测器，返回还原函数。
// 让 preflight 单测无需真实 JDK：用桩函数模拟「未装/版本 N」。
func withJavaProbe(t *testing.T, fn func(javaBin string) (int, bool)) {
	t.Helper()
	prev := javaMajorProbe
	javaMajorProbe = fn
	t.Cleanup(func() { javaMajorProbe = prev })
}

// TestPreflightJavaVersion_UnboundTooLow 复现 BUG-012：实例未绑定 JDK（JavaHome/JDKBinPath 皆空），
// PATH 上的 java 是低版本（如 Java 8），启动 MC（命令含 java）应在启动前返回明确错误，
// 而非放任游戏服以 UnsupportedClassVersionError 静默崩在自身日志里。
func TestPreflightJavaVersion_UnboundTooLow(t *testing.T) {
	withJavaProbe(t, func(javaBin string) (int, bool) {
		// 模拟 PATH 上能跑但版本过低的 java（Java 8）。
		return 8, true
	})

	spec := CommandSpec{
		UUID:         "inst-mc",
		StartCommand: "java -Xms1024M -Xmx1024M -jar paper.jar nogui",
		// 未绑定 JDK：JavaHome 与 JDKBinPath 均为空。
	}
	err := preflightJavaVersion(spec)
	if err == nil {
		t.Fatal("未绑定 JDK 且 PATH java 版本过低，preflight 应返回错误")
	}
	// 错误信息应指引绑定/安装合适 JDK。
	if !strings.Contains(err.Error(), "JDK") {
		t.Fatalf("错误信息应指引绑定/安装 JDK，实际: %q", err.Error())
	}
}

// TestPreflightJavaVersion_UnboundJavaMissing 未绑定 JDK 且 PATH 上根本没有可用 java
// （java -version 跑不起来）→ 启动前明确报错，而非让 shell 报「'java' 不是命令」崩在日志。
func TestPreflightJavaVersion_UnboundJavaMissing(t *testing.T) {
	withJavaProbe(t, func(javaBin string) (int, bool) {
		return 0, false // 探测失败：未安装/不可执行
	})

	spec := CommandSpec{
		UUID:         "inst-mc",
		StartCommand: "java -jar server.jar nogui",
	}
	err := preflightJavaVersion(spec)
	if err == nil {
		t.Fatal("PATH 无可用 java 时 preflight 应返回错误")
	}
	if !strings.Contains(err.Error(), "JDK") {
		t.Fatalf("错误信息应指引绑定/安装 JDK，实际: %q", err.Error())
	}
}

// TestPreflightJavaVersion_UnboundOK 未绑定 JDK 但 PATH java 版本满足下限（如 Java 21）→ 通过。
func TestPreflightJavaVersion_UnboundOK(t *testing.T) {
	withJavaProbe(t, func(javaBin string) (int, bool) {
		return 21, true
	})

	spec := CommandSpec{
		UUID:         "inst-mc",
		StartCommand: "java -jar paper.jar nogui",
	}
	if err := preflightJavaVersion(spec); err != nil {
		t.Fatalf("PATH java 版本满足下限时应通过，实际报错: %v", err)
	}
}

// TestPreflightJavaVersion_BoundResolvesBinAndProbes 绑定 JDK 时，preflight 探测的是
// 绑定 JDK 的 bin/java（而非 PATH），且绑定可运行即通过——不对绑定 JDK 施加下限
// （CP 已按实例要求选定该 JDK）。
func TestPreflightJavaVersion_BoundProbesBoundBinary(t *testing.T) {
	var probed string
	withJavaProbe(t, func(javaBin string) (int, bool) {
		probed = javaBin
		return 8, true // 即便绑定的是 Java 8 也通过（用户显式绑定）
	})

	spec := CommandSpec{
		UUID:         "inst-mc",
		StartCommand: "java -jar legacy.jar nogui",
		JavaHome:     "/opt/jdk-8",
	}
	if err := preflightJavaVersion(spec); err != nil {
		t.Fatalf("绑定 JDK 可运行时应通过，实际报错: %v", err)
	}
	if !strings.Contains(probed, "jdk-8") {
		t.Fatalf("绑定 JDK 时应探测绑定的 bin/java，实际探测: %q", probed)
	}
}

// TestPreflightJavaVersion_BoundUnrunnable 绑定 JDK 但其 bin/java 跑不起来（损坏/路径错）→
// 启动前明确报错，而非让进程崩在日志。
func TestPreflightJavaVersion_BoundUnrunnable(t *testing.T) {
	withJavaProbe(t, func(javaBin string) (int, bool) {
		return 0, false
	})

	spec := CommandSpec{
		UUID:         "inst-mc",
		StartCommand: "java -jar paper.jar nogui",
		JDKBinPath:   "/opt/broken-jdk/bin",
	}
	err := preflightJavaVersion(spec)
	if err == nil {
		t.Fatal("绑定 JDK 不可运行时 preflight 应返回错误")
	}
}

// TestPreflightJavaVersion_NonJavaSkipped 非 java 启动命令（如 node 代理脚本、纯 shell）
// 不做 java 校验，避免误伤非 MC/非 java 实例。
func TestPreflightJavaVersion_NonJavaSkipped(t *testing.T) {
	called := false
	withJavaProbe(t, func(javaBin string) (int, bool) {
		called = true
		return 0, false
	})

	spec := CommandSpec{
		UUID:         "inst-x",
		StartCommand: "node proxy.js",
	}
	if err := preflightJavaVersion(spec); err != nil {
		t.Fatalf("非 java 命令不应触发 java 校验，实际报错: %v", err)
	}
	if called {
		t.Fatal("非 java 命令不应调用 java 版本探测")
	}
}
