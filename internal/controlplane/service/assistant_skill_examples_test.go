package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 仓库自带示例技能必须能被真实装载器解析。
//
// ## 为什么这条要成为永久用例
//
// `configs/skills/*/SKILL.md` 是随仓库发布的官方技能内容，它们的格式约束
// （frontmatter 必须闭合、必须有 description、正文非空）只由装载器实现来判定。
// 若只在开发时手工验证一次，之后任何人改坏一个缩进或漏一个 `---`，
// 装载器会把它归入 Skipped 静默跳过——线上表现就是「这个技能不见了」，
// 而 CI 全绿。把断言固化下来，「改坏示例」当场变红。
//
// ## 为什么不用 ../../../configs/skills 这种相对路径
//
// 相对路径依赖「测试的工作目录恰好是包目录」这一隐含前提。
// 用 runtime.Caller 定位本文件再回溯到仓库根，结构变了会直接报错，
// 而不是读到空目录后「空跑通过」——后者是最坏的情况：断言看着在守，实际什么都没守。
func TestRepoExampleSkillsParse(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件路径")
	}
	// 本文件位于 <repo>/internal/controlplane/service/，向上三级即仓库根。
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	skillsRoot := filepath.Join(repoRoot, "configs", "skills")
	if _, err := os.Stat(skillsRoot); err != nil {
		t.Fatalf("仓库示例技能目录不存在（%s）：%v", skillsRoot, err)
	}

	res := LoadSkills(skillsRoot)
	if len(res.Skipped) > 0 {
		t.Fatalf("示例技能存在不合规项，装载器会静默跳过：%v", res.Skipped)
	}
	if len(res.Skills) < 2 {
		t.Fatalf("示例技能至少应有 2 个，实际 %d（目录 %s）", len(res.Skills), skillsRoot)
	}
	for _, s := range res.Skills {
		if s.Description == "" {
			t.Fatalf("技能 %s 缺 description", s.Name)
		}
		if s.Instructions == "" {
			t.Fatalf("技能 %s 正文为空", s.Name)
		}
		// 正文里不得残留 frontmatter 分隔符，否则会被原样拼进系统提示词。
		for _, line := range strings.Split(s.Instructions, "\n") {
			if strings.TrimSpace(line) == "---" {
				t.Fatalf("技能 %s 的正文混入了 frontmatter 分隔符", s.Name)
			}
		}
	}
}
