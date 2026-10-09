package daemon

import (
	"strings"
	"testing"
)

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

// TestComposeEnv_StripsInheritedJavaHome 复现真机事故（2026-10-09，10 台 Paper 1.20 全部起不来）。
//
// daemon 是线上主用进程型：wrapper 由 Worker 拉起，wrapper 再拉起 java。wrapper 的基线同样是
// os.Environ()（Worker 自身环境），此前只「追加」cfg.JavaHome、从不剥离继承值，于是实例 Java 运行时
// 受 Worker 环境漂移影响（真机为 ~/.sdkman/candidates/java/current 软链被切到 Java 8）。
// 期望：有绑定 → 只剩唯一绑定值；无绑定 → 不残留。
func TestComposeEnv_StripsInheritedJavaHome(t *testing.T) {
	base := []string{
		"JAVA_HOME=/home/u/.sdkman/candidates/java/current",
		"PATH=/usr/local/bin:/usr/bin",
	}
	out := composeEnv(base, WrapperConfig{JavaHome: "/opt/jdk-17"})

	if n := countEnvKey(out, "JAVA_HOME"); n != 1 {
		t.Fatalf("JAVA_HOME 条目数应为 1（仅绑定值），实际 %d：%v", n, out)
	}
	for _, kv := range out {
		if kv == "JAVA_HOME=/home/u/.sdkman/candidates/java/current" {
			t.Fatalf("继承的脏 JAVA_HOME 未被剥离：%v", out)
		}
	}
}

// TestComposeEnv_UnboundStripsInheritedJavaHome 未绑定 JDK 时不得残留继承的 JAVA_HOME。
func TestComposeEnv_UnboundStripsInheritedJavaHome(t *testing.T) {
	base := []string{
		"JAVA_HOME=/home/u/.sdkman/candidates/java/current",
		"PATH=/usr/local/bin:/usr/bin",
	}
	out := composeEnv(base, WrapperConfig{})

	if n := countEnvKey(out, "JAVA_HOME"); n != 0 {
		t.Fatalf("未绑定 JDK 时不应残留 JAVA_HOME，实际 %d 条：%v", n, out)
	}
}

// TestComposeEnv_EnvVarsDedupsBase 兑现 composeEnv 注释承诺「与 process.ComposeEnv 行为一致」：
// 实例自定义 EnvVars 必须移除基线同名键，而不是纯 append 留下重复条目。
func TestComposeEnv_EnvVarsDedupsBase(t *testing.T) {
	base := []string{"FOO=base", "JAVA_HOME=/old"}
	out := composeEnv(base, WrapperConfig{
		JavaHome: "/opt/jdk-17",
		EnvVars:  map[string]string{"FOO": "override"},
	})

	if n := countEnvKey(out, "FOO"); n != 1 {
		t.Fatalf("FOO 条目数应为 1，实际 %d：%v", n, out)
	}
	if n := countEnvKey(out, "JAVA_HOME"); n != 1 {
		t.Fatalf("JAVA_HOME 条目数应为 1，实际 %d：%v", n, out)
	}
	for _, kv := range out {
		if strings.HasPrefix(kv, "FOO=") && kv != "FOO=override" {
			t.Fatalf("实例 env 未覆盖基线 FOO：%v", out)
		}
	}
}
