package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/skill"
)

// 清单与内嵌 FS 必须严格一致。
//
// 这是本组测试里最重要的一条：`skillManifest` 写死在客户端，而 `skill/` 目录是权威源。
// 若目录新增了条目而清单没跟上，`jm skill install` 会**静默少装文件**——用户不会收到任何
// 报错，只是 agent 少了那篇参考文档。反过来清单写了不存在的条目，则 install 必然失败。
func TestSkillManifestMatchesEmbeddedFS(t *testing.T) {
	fromFS, err := skillManifestFromFS(skill.FS())
	if err != nil {
		t.Fatalf("遍历内嵌 FS 失败: %v", err)
	}
	got := append([]string(nil), skillManifest...)
	sort.Strings(got)
	if len(got) != len(fromFS) {
		t.Fatalf("清单条数与内嵌 FS 不一致\n清单: %v\n实际: %v", got, fromFS)
	}
	for i := range got {
		if got[i] != fromFS[i] {
			t.Errorf("第 %d 条不一致：清单 %q，实际 %q", i, got[i], fromFS[i])
		}
	}
}

// 清单里的每一条都能从内嵌 FS 读到内容。
func TestSkillManifestEntriesReadable(t *testing.T) {
	for _, rel := range skillManifest {
		body, err := skill.FS().Open(rel)
		if err != nil {
			t.Errorf("内嵌 FS 缺少 %s: %v", rel, err)
			continue
		}
		st, err := body.Stat()
		body.Close()
		if err != nil {
			t.Errorf("读取 %s 状态失败: %v", rel, err)
			continue
		}
		if st.Size() == 0 {
			t.Errorf("%s 是空文件", rel)
		}
	}
}

// 三个宿主的技能根目录都落在 `~/.<host>/skills`。
func TestHostSkillRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无主目录，跳过")
	}
	cases := map[string]string{
		"claude": filepath.Join(home, ".claude", "skills"),
		"codex":  filepath.Join(home, ".codex", "skills"),
		"cursor": filepath.Join(home, ".cursor", "skills"),
	}
	for host, want := range cases {
		got, err := hostSkillRoot(host)
		if err != nil {
			t.Errorf("%s: %v", host, err)
			continue
		}
		if got != want {
			t.Errorf("%s 根目录 = %q，期望 %q", host, got, want)
		}
	}
	if _, err := hostSkillRoot("vscode"); err == nil {
		t.Error("未知宿主应当报错")
	}
}

// --host 支持重复与逗号分隔两种写法。
func TestParseSkillInstallArgsHosts(t *testing.T) {
	opt, err := parseSkillInstallArgs([]string{"--host", "claude", "--host", "codex,cursor"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := []string{"claude", "codex", "cursor"}
	if len(opt.hosts) != len(want) {
		t.Fatalf("hosts = %v，期望 %v", opt.hosts, want)
	}
	for i := range want {
		if opt.hosts[i] != want[i] {
			t.Errorf("hosts[%d] = %q，期望 %q", i, opt.hosts[i], want[i])
		}
	}
}

// --dir / --from / --dry-run 组合解析正确。
func TestParseSkillInstallArgsDirAndDryRun(t *testing.T) {
	opt, err := parseSkillInstallArgs([]string{"--dir", "/tmp/x", "--from", "skill", "--dry-run"})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if opt.dir != "/tmp/x" || opt.from != "skill" || !opt.dryRun {
		t.Errorf("解析结果不符: %+v", opt)
	}
}

// 缺值参数必须报错，不能静默当作空。
func TestParseSkillInstallArgsMissingValue(t *testing.T) {
	if _, err := parseSkillInstallArgs([]string{"--dir"}); err == nil {
		t.Error("--dir 缺值应当报错")
	}
	if _, err := parseSkillInstallArgs([]string{"--unknown"}); err == nil {
		t.Error("未知参数应当报错")
	}
}

// 从本地目录装载：内容与内嵌 FS 逐字节一致。
func TestLoadSkillFromDir(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range skillManifest {
		want, err := skill.FS().Open(rel)
		if err != nil {
			t.Fatalf("内嵌 FS 缺少 %s: %v", rel, err)
		}
		body := make([]byte, 4096)
		n, _ := want.Read(body)
		want.Close()
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, body[:n], 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := loadSkillFromDir(dir)
	if err != nil {
		t.Fatalf("装载失败: %v", err)
	}
	if len(entries) != len(skillManifest) {
		t.Fatalf("装载 %d 条，期望 %d", len(entries), len(skillManifest))
	}
	for _, e := range entries {
		if len(e.Body) == 0 {
			t.Errorf("%s 内容为空", e.Rel)
		}
	}
}

// 本地目录缺文件时给出明确错误，而不是装一半。
func TestLoadSkillFromDirMissingFile(t *testing.T) {
	if _, err := loadSkillFromDir(t.TempDir()); err == nil {
		t.Error("空目录应当报错（缺 SKILL.md）")
	}
}

// 未配置 CP 地址时，提示里必须包含 --from 这条自救路径。
func TestLoadSkillFromCPNoURL(t *testing.T) {
	_, err := loadSkillFromCP(Config{})
	if err == nil {
		t.Fatal("未配置地址应当报错")
	}
	if !strings.Contains(err.Error(), "--from") {
		t.Errorf("错误信息未提示 --from 自救路径: %v", err)
	}
}
