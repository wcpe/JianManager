package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 六个宿主的用户级根目录都落在 `~/.<host>/skills`。
func TestHostSkillRootAllHosts(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无主目录")
	}
	want := map[string]string{
		"claude":  filepath.Join(home, ".claude", "skills"),
		"codex":   filepath.Join(home, ".codex", "skills"),
		"cursor":  filepath.Join(home, ".cursor", "skills"),
		"kiro":    filepath.Join(home, ".kiro", "skills"),
		"copilot": filepath.Join(home, ".copilot", "skills"),
		"agents":  filepath.Join(home, ".agents", "skills"),
	}
	for host, exp := range want {
		got, err := hostSkillRoot(host)
		if err != nil {
			t.Errorf("%s: %v", host, err)
			continue
		}
		if got != exp {
			t.Errorf("%s 根目录 = %q，期望 %q", host, got, exp)
		}
	}
	if _, err := hostSkillRoot("vscode"); err == nil {
		t.Error("未知宿主应当报错")
	}
}

// skillHosts 与 hostSkillRoot / workspaceSkillRoot 必须完全对应。
//
// 任何一侧漏掉一个宿主，该宿主就无法被 --host 选中，也不会出现在交互菜单里。
func TestSkillHostsCoverage(t *testing.T) {
	project := t.TempDir()
	for _, h := range skillHosts {
		if _, err := hostSkillRoot(h); err != nil {
			t.Errorf("hostSkillRoot 缺 %s: %v", h, err)
		}
		if _, err := workspaceSkillRoot(h, project); err != nil {
			t.Errorf("workspaceSkillRoot 缺 %s: %v", h, err)
		}
	}
	if len(skillHosts) != 6 {
		t.Errorf("宿主数应为 6，实际 %d", len(skillHosts))
	}
}

// 项目级目录与各 harness 的真实约定一致。
//
// 这些路径不是随意定的：Vantaloom 与 NarraFork 正是按这些目录组织各自技能库，
// 装错目录等于技能不生效。
func TestWorkspaceSkillRootConventions(t *testing.T) {
	ws := filepath.Join("tmp", "ws")
	cases := map[string]string{
		"claude":  filepath.Join(ws, ".claude", "skills"),
		"codex":   filepath.Join(ws, ".codex", "skills"),
		"cursor":  filepath.Join(ws, ".cursor", "skills"),
		"kiro":    filepath.Join(ws, ".kiro", "skills"),
		"copilot": filepath.Join(ws, ".github", "skills"),
		"agents":  filepath.Join(ws, ".agents", "skills"),
	}
	for host, exp := range cases {
		got, err := workspaceSkillRoot(host, ws)
		if err != nil {
			t.Errorf("%s: %v", host, err)
			continue
		}
		if got != exp {
			t.Errorf("%s 项目级目录 = %q，期望 %q", host, got, exp)
		}
	}
}

// --scope 只接受 user / project，其它值必须报错。
func TestParseSkillInstallScope(t *testing.T) {
	opt, err := parseSkillInstallArgs([]string{"--scope", "project"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if opt.scope != skillScopeProject {
		t.Errorf("scope = %q", opt.scope)
	}
	if _, err := parseSkillInstallArgs([]string{"--scope", "global"}); err == nil {
		t.Error("非法 scope 应报错")
	}
}

// --project 生效，且与 --scope project 一起决定落点。
func TestParseSkillInstallProject(t *testing.T) {
	opt, err := parseSkillInstallArgs([]string{"--scope", "project", "--project", "/x/ws"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if opt.project != "/x/ws" {
		t.Errorf("project = %q", opt.project)
	}
}

// buildTargets 在 project 作用域下给出项目内路径。
func TestBuildTargetsProjectScope(t *testing.T) {
	ws := t.TempDir()
	targets := buildTargets([]string{"claude"}, skillScopeProject, ws)
	if len(targets) != len(skillHosts) {
		t.Fatalf("应列出 %d 个宿主，实际 %d", len(skillHosts), len(targets))
	}
	if targets[0].Label != "claude" {
		t.Fatalf("首个应为 claude，实际 %s", targets[0].Label)
	}
	want := filepath.Join(ws, ".claude", "skills", "jianmanager")
	if targets[0].Path != want {
		t.Errorf("项目级 claude 路径 = %q，期望 %q", targets[0].Path, want)
	}
}

// detectHosts 能识别项目内已存在的宿主目录（不只用户级）。
func TestDetectHostsIncludesWorkspace(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".kiro", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	found := detectHosts(ws)
	var has bool
	for _, h := range found {
		if h == "kiro" {
			has = true
		}
	}
	if !has {
		t.Errorf("应识别出项目内的 .kiro，实际 %v", found)
	}
}
