package decompiler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCFRArgs(t *testing.T) {
	// 整 jar / 单 class：无 jarEntry，不带 --jarfilter。
	args := cfrArgs("/tools/cfr.jar", "/work/Foo.class", "")
	require.Equal(t, []string{"-jar", "/tools/cfr.jar", "/work/Foo.class", "--silent", "true"}, args)

	// jar 内某 class：带 --jarfilter，类名去 .class、正斜杠保留。
	args2 := cfrArgs("/tools/cfr.jar", "/work/Foo.jar", "com/example/Foo.class")
	require.Contains(t, args2, "--jarfilter")
	require.Contains(t, args2, "com/example/Foo")
	require.NotContains(t, strings.Join(args2, " "), ".class")
}

func TestResolveJavaBin(t *testing.T) {
	dir := t.TempDir()
	exeName := "java"
	if runtime.GOOS == "windows" {
		exeName = "java.exe"
	}

	// 构造一个假的实例 JDK bin/java。
	jdkRoot := filepath.Join(dir, "jdk21")
	binDir := filepath.Join(jdkRoot, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	javaPath := filepath.Join(binDir, exeName)
	require.NoError(t, os.WriteFile(javaPath, []byte("#!/bin/sh\n"), 0o755))

	// 实例 JDK 根的 bin 应被解析到。
	got := ResolveJavaBin(jdkRoot, "", nil)
	require.Equal(t, javaPath, got)

	// 显式 bin 目录优先。
	got2 := ResolveJavaBin("", binDir, nil)
	require.Equal(t, javaPath, got2)

	// 系统候选。
	got3 := ResolveJavaBin("", "", []string{jdkRoot})
	require.Equal(t, javaPath, got3)

	// 全无（且 PATH 无 java 时）→ 空；此处不强断言（CI 可能 PATH 有 java）。
	_ = ResolveJavaBin(filepath.Join(dir, "nope"), "", nil)
}

func TestRun_NoJavaOrCFR(t *testing.T) {
	_, err := Run(context.Background(), Options{JavaBin: "", CFRJar: "x", Target: "y"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "JDK")

	_, err = Run(context.Background(), Options{JavaBin: "java", CFRJar: "", Target: "y"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "CFR")
}

// findJavaForTest 找一个可用的 java：优先 JAVA_HOME/bin/java，否则 PATH。无则空串。
func findJavaForTest() string {
	exeName := "java"
	if runtime.GOOS == "windows" {
		exeName = "java.exe"
	}
	if jh := os.Getenv("JAVA_HOME"); jh != "" {
		cand := filepath.Join(jh, "bin", exeName)
		if fileExists(cand) {
			return cand
		}
	}
	if p, err := exec.LookPath(exeName); err == nil {
		return p
	}
	return ""
}

// TestRun_RealCFR 真机端到端：下载 CFR（sha256 pin）→ 编译一个 class → CFR 反编译出源码。
// 需要 JDK（含 javac）与联网；缺任一即跳过（标「待真机验」）。
func TestRun_RealCFR(t *testing.T) {
	if testing.Short() {
		t.Skip("short 模式跳过真机 CFR 反编译")
	}
	java := findJavaForTest()
	if java == "" {
		t.Skip("无可用 JDK，跳过真机反编译")
	}
	javac := strings.TrimSuffix(java, "java") + "javac"
	if runtime.GOOS == "windows" {
		javac = strings.TrimSuffix(java, "java.exe") + "javac.exe"
	}
	if !fileExists(javac) {
		t.Skip("无 javac，跳过真机反编译")
	}

	// 解析 CFR（按需下载到临时缓存，sha256 pin 校验真实 jar）。
	cache := filepath.Join(t.TempDir(), "cache")
	p := NewProvider(Config{CacheDir: cache, AllowDownload: true})
	cfrJar, err := p.Resolve()
	if err != nil {
		t.Skipf("CFR 不可用（可能无网络）: %v", err)
	}

	// 写一个最小 Java 类并编译。
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "Hello.java"),
		[]byte("public class Hello {\n  public int add(int a,int b){ return a+b; }\n}\n"), 0o644))
	out, err := exec.Command(javac, filepath.Join(src, "Hello.java")).CombinedOutput()
	require.NoError(t, err, "javac 失败: %s", out)
	classFile := filepath.Join(src, "Hello.class")
	require.FileExists(t, classFile)

	res, err := Run(context.Background(), Options{JavaBin: java, CFRJar: cfrJar, Target: classFile})
	require.NoError(t, err)
	require.Contains(t, res.Source, "public class Hello")
	require.Contains(t, res.Source, "add")
	require.Equal(t, "CFR "+CFRVersion, res.Decompiler)
}
