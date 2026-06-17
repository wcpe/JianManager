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
			"FOO":              "override",
			"MINECRAFT_EULA":   "true",
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
