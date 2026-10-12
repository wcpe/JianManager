package process

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 验证：基线环境被保留，JAVA_HOME 注入，JDK/bin 前置到 PATH，
// 实例 EnvVars 覆盖基线同名键。
func TestComposeEnv_JavaHomeAndPath(t *testing.T) {
	pathKey := "PATH"
	if runtime.GOOS == "windows" {
		pathKey = "Path"
	}
	base := []string{
		pathKey + "=/usr/bin",
		"FOO=base",
		"USER=tester",
	}
	spec := CommandSpec{
		JavaHome: "/opt/jdk-21",
		EnvVars: map[string]string{
			"FOO":            "override",
			"MINECRAFT_EULA": "true",
		},
	}
	out := ComposeEnv(base, spec)
	lookup := func(k string) string {
		for _, kv := range out {
			if i := strings.IndexByte(kv, '='); i > 0 && kv[:i] == k {
				return kv[i+1:]
			}
		}
		return ""
	}
	if got := lookup("JAVA_HOME"); got != "/opt/jdk-21" {
		t.Fatalf("JAVA_HOME 未注入: %q", got)
	}
	gotPath := lookup(pathKey)
	bin := filepath.Join("/opt/jdk-21", "bin")
	if !strings.HasPrefix(gotPath, bin+string(os.PathListSeparator)) {
		t.Fatalf("PATH 未前置 JDK bin: %q (expected prefix %q)", gotPath, bin)
	}
	if !strings.HasSuffix(gotPath, "/usr/bin") {
		t.Fatalf("原 PATH 尾部丢失: %q", gotPath)
	}
	if got := lookup("FOO"); got != "override" {
		t.Fatalf("实例 env 未覆盖基线: %q", got)
	}
	if got := lookup("USER"); got != "tester" {
		t.Fatalf("基线 USER 丢失: %q", got)
	}
	if got := lookup("MINECRAFT_EULA"); got != "true" {
		t.Fatalf("实例自定义 env 丢失: %q", got)
	}
}

func TestComposeEnv_EmptyJavaHome(t *testing.T) {
	base := []string{"FOO=base"}
	out := ComposeEnv(base, CommandSpec{EnvVars: map[string]string{"FOO": "x"}})
	// base 中的 FOO=base 应被覆盖，数组里只保留一个 FOO=x。
	count := 0
	lastFOO := ""
	for _, kv := range out {
		if i := strings.IndexByte(kv, '='); i > 0 && kv[:i] == "FOO" {
			count++
			lastFOO = kv[i+1:]
		}
	}
	if count != 1 || lastFOO != "x" {
		t.Fatalf("无 JAVA_HOME 时实例 env 应替换基线 FOO, got %v", out)
	}
}

// countEnvKey 统计 out 中键为 k 的条目数（用于断言「重复键已消除」）。
func countEnvKey(out []string, k string) int {
	n := 0
	for _, kv := range out {
		if i := strings.IndexByte(kv, '='); i > 0 && kv[:i] == k {
			n++
		}
	}
	return n
}

// TestComposeEnv_StripsInheritedJavaHome 复现真机事故（2026-10-09，10 台 Paper 1.20 全部起不来）：
//
// Worker 自身环境里带着 JAVA_HOME（真机为 ~/.sdkman/candidates/java/current，该软链被切到 Java 8），
// ComposeEnv 以 os.Environ() 为基线原样拷贝，只「追加」绑定的 JAVA_HOME、从不剥离继承值，
// 于是实例进程继承了一个**不由实例绑定决定**的、可随 Worker 环境漂移的 JAVA_HOME。
//
// 期望：实例的 JAVA_HOME 只由实例绑定的 JDK 决定，继承来的值必须被剥离（有绑定→唯一绑定值）。
func TestComposeEnv_StripsInheritedJavaHome(t *testing.T) {
	base := []string{
		"JAVA_HOME=/home/user/.sdkman/candidates/java/current", // Worker 继承的脏值
		"PATH=/usr/local/bin:/usr/bin",
	}
	out := ComposeEnv(base, CommandSpec{JavaHome: "/opt/jdk-17"})

	if n := countEnvKey(out, "JAVA_HOME"); n != 1 {
		t.Fatalf("JAVA_HOME 条目数应为 1（仅绑定值），实际 %d：%v", n, out)
	}
	for _, kv := range out {
		if kv == "JAVA_HOME=/home/user/.sdkman/candidates/java/current" {
			t.Fatalf("继承的脏 JAVA_HOME 未被剥离：%v", out)
		}
	}
}

// TestComposeEnv_UnboundStripsInheritedJavaHome 未绑定 JDK 时必须剥离继承的 JAVA_HOME：
// 留着它等于让实例的 Java 运行时指向 Worker 环境里的可变软链，是本次事故的直接成因。
func TestComposeEnv_UnboundStripsInheritedJavaHome(t *testing.T) {
	base := []string{
		"JAVA_HOME=/home/user/.sdkman/candidates/java/current",
		"PATH=/usr/local/bin:/usr/bin",
	}
	out := ComposeEnv(base, CommandSpec{})

	if n := countEnvKey(out, "JAVA_HOME"); n != 0 {
		t.Fatalf("未绑定 JDK 时不应残留 JAVA_HOME，实际 %d 条：%v", n, out)
	}
}

// TestComposeEnv_InstanceEnvVarsCanStillSetJavaHome 锁定逃生通道：剥离继承值不等于「禁止指定」。
// 实例级 EnvVars（控制台「环境变量」页签）显式设置的 JAVA_HOME 仍然生效且唯一——
// 这是有意保留的正规途径，取代「靠 Worker 进程环境碰巧带什么」的隐式传播。
func TestComposeEnv_InstanceEnvVarsCanStillSetJavaHome(t *testing.T) {
	base := []string{
		"JAVA_HOME=/home/user/.sdkman/candidates/java/current", // 继承的脏值
		"FOO=base",
	}
	out := ComposeEnv(base, CommandSpec{
		JavaHome: "/opt/jdk-17", // 绑定 JDK
		EnvVars:  map[string]string{"JAVA_HOME": "/opt/jdk-8-for-legacy"},
	})

	if n := countEnvKey(out, "JAVA_HOME"); n != 1 {
		t.Fatalf("JAVA_HOME 条目数应为 1，实际 %d：%v", n, out)
	}
	for _, kv := range out {
		if strings.HasPrefix(kv, "JAVA_HOME=") && kv != "JAVA_HOME=/opt/jdk-8-for-legacy" {
			t.Fatalf("实例级 EnvVars 未取得最终决定权：%v", out)
		}
	}
}
