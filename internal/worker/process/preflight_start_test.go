package process

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseJarPath(t *testing.T) {
	cases := []struct{ name, cmd, want string }{
		{"标准 -jar", "java -Xmx2G -jar server.jar nogui", "server.jar"},
		{"带引号", `java -jar "server.jar"`, "server.jar"},
		{"无 -jar", "node index.js", ""},
		{"-jar 结尾缺值", "java -jar", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseJarPath(tc.cmd); got != tc.want {
				t.Errorf("parseJarPath(%q)=%q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestCheckWorkDir(t *testing.T) {
	dir := t.TempDir()
	if err := checkWorkDir(dir); err != nil {
		t.Errorf("存在的目录应通过: %v", err)
	}
	if err := checkWorkDir(""); err == nil {
		t.Error("空工作目录应报错")
	}
	if err := checkWorkDir(filepath.Join(dir, "nope")); err == nil {
		t.Error("不存在目录应报错")
	}
	f := filepath.Join(dir, "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkWorkDir(f); err == nil {
		t.Error("文件（非目录）应报错")
	}
}

func TestCheckLaunchTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.jar"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkLaunchTarget("java -jar server.jar", dir); err != nil {
		t.Errorf("jar 存在应通过: %v", err)
	}
	if err := checkLaunchTarget("java -jar missing.jar", dir); err == nil {
		t.Error("jar 缺失应报错")
	}
	if err := checkLaunchTarget("node index.js", dir); err != nil {
		t.Errorf("非 -jar 命令应保守放行: %v", err)
	}
}

// TestManager_PreflightStart 覆盖聚合预检：全通过 / jar 缺失 / docker 放行 / 未注册报错。
func TestManager_PreflightStart(t *testing.T) {
	orig := javaMajorProbe
	defer func() { javaMajorProbe = orig }()
	javaMajorProbe = func(javaBin string) (int, bool) { return 21, true }

	okDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(okDir, "server.jar"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(t.TempDir())

	if err := m.Create("u1", "inst1", "java -jar server.jar nogui", "", okDir, nil, false, ProcessTypeDaemon, "/opt/jdk21", "", 0, 0); err != nil {
		t.Fatalf("Create u1: %v", err)
	}
	checks, err := m.PreflightStart("u1")
	if err != nil {
		t.Fatalf("PreflightStart u1: %v", err)
	}
	for _, c := range checks {
		if !c.OK {
			t.Errorf("预检项 %s 应通过：%s", c.Name, c.Message)
		}
	}

	missDir := t.TempDir()
	if err := m.Create("u2", "inst2", "java -jar server.jar nogui", "", missDir, nil, false, ProcessTypeDaemon, "/opt/jdk21", "", 0, 0); err != nil {
		t.Fatalf("Create u2: %v", err)
	}
	checks2, err := m.PreflightStart("u2")
	if err != nil {
		t.Fatalf("PreflightStart u2: %v", err)
	}
	launchOK := true
	for _, c := range checks2 {
		if c.Name == "launch_target" {
			launchOK = c.OK
		}
	}
	if launchOK {
		t.Error("jar 缺失时 launch_target 应失败")
	}

	if err := m.Create("u3", "inst3", "", "", missDir, nil, false, ProcessTypeDocker, "", "", 0, 0); err != nil {
		t.Fatalf("Create u3: %v", err)
	}
	checks3, err := m.PreflightStart("u3")
	if err != nil {
		t.Fatalf("PreflightStart u3: %v", err)
	}
	if len(checks3) != 1 || !checks3[0].OK {
		t.Errorf("docker 实例应整体放行，got %+v", checks3)
	}

	if _, err := m.PreflightStart("nope"); err == nil {
		t.Error("未注册实例应返回 error")
	}
}
