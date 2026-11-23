package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 技能装载。所有用例都围绕一条主线：
// 单个坏技能不得连累整个目录，但坏在哪里必须可解释（不是静默丢弃）。

func TestParseSkillMarkdown_Valid(t *testing.T) {
	data := []byte(`---
name: instance-triage
description: 实例异常分级排查。
whenToUse: 实例起不来时。
version: "1.0"
source: official
---
# 标题

正文第一行。
`)
	s, err := ParseSkillMarkdown("instance-triage", data)
	if err != nil {
		t.Fatalf("应解析成功: %v", err)
	}
	if s.Name != "instance-triage" {
		t.Fatalf("Name 应取目录名，实际 %q", s.Name)
	}
	if s.Description != "实例异常分级排查。" {
		t.Fatalf("description 解析错误: %q", s.Description)
	}
	if s.WhenToUse != "实例起不来时。" || s.Version != "1.0" || s.Source != "official" {
		t.Fatalf("frontmatter 字段解析错误: %+v", s)
	}
	if !strings.HasPrefix(s.Instructions, "# 标题") {
		t.Fatalf("正文应去掉 frontmatter 且以标题开头，实际 %q", s.Instructions)
	}
	if strings.Contains(s.Instructions, "description:") {
		t.Fatal("正文不得包含 frontmatter")
	}
}

// TestParseSkillMarkdown_DirectoryNameWins 目录名是存在性真源，frontmatter 的 name 只作展示。
//
// 若以 frontmatter 为准，改一行 YAML 就能让两个技能重名，而「哪个会生效」
// 取决于遍历顺序——这种不确定性在运营内容里不可接受。
func TestParseSkillMarkdown_DirectoryNameWins(t *testing.T) {
	data := []byte("---\nname: something-else\ndescription: d\n---\n正文\n")
	s, err := ParseSkillMarkdown("real-dir", data)
	if err != nil {
		t.Fatalf("应解析成功: %v", err)
	}
	if s.Name != "real-dir" {
		t.Fatalf("Name 必须是目录名，实际 %q", s.Name)
	}
	if s.FrontmatterName != "something-else" {
		t.Fatalf("frontmatter 名应保留供排障，实际 %q", s.FrontmatterName)
	}
}

func TestParseSkillMarkdown_Rejects(t *testing.T) {
	cases := map[string]string{
		"无 frontmatter":   "# 只有正文\n内容\n",
		"frontmatter 未闭合": "---\ndescription: d\n\n正文\n",
		"缺 description":   "---\nname: x\n---\n正文\n",
		"正文为空":            "---\nname: x\ndescription: d\n---\n   \n",
		"YAML 非法":         "---\ndescription: [unclosed\n---\n正文\n",
	}
	for name, data := range cases {
		if _, err := ParseSkillMarkdown("x", []byte(data)); err == nil {
			t.Fatalf("%s：应报错而不是静默收下", name)
		}
	}
}

// TestLoadSkills_SkipsBrokenButKeepsOthers 一个坏技能不得让整个目录为空。
//
// 这条守的是真实运维场景：管理员手改某个 SKILL.md 时打错一个缩进，
// 若装载器整体失败，表现就是「所有技能突然都没了」，而原因无从判断。
func TestLoadSkills_SkipsBrokenButKeepsOthers(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("建目录失败: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}
	write("good-a", "---\ndescription: a\n---\n正文 A\n")
	write("good-b", "---\ndescription: b\n---\n正文 B\n")
	write("broken", "---\n缺 description\n---\n正文\n")
	// 没有 SKILL.md 的子目录不是技能，应被忽略且不计入 skipped。
	if err := os.MkdirAll(filepath.Join(root, "not-a-skill"), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}

	res := LoadSkills(root)
	if len(res.Skills) != 2 {
		t.Fatalf("应装载 2 个技能，实际 %d（%+v）", len(res.Skills), res.Skills)
	}
	// 升序稳定：调用方顺序不影响结果。
	if res.Skills[0].Name != "good-a" || res.Skills[1].Name != "good-b" {
		t.Fatalf("技能应按名字升序，实际 %q, %q", res.Skills[0].Name, res.Skills[1].Name)
	}
	if _, ok := res.Skipped["broken"]; !ok {
		t.Fatal("坏技能必须出现在 Skipped 里（否则无从解释为什么少了它）")
	}
	if _, ok := res.Skipped["not-a-skill"]; ok {
		t.Fatal("无 SKILL.md 的子目录不是技能，不应计入 skipped")
	}
}

// TestLoadSkills_MissingRootIsEmpty 全新部署没有 skills 目录是正常状态。
func TestLoadSkills_MissingRootIsEmpty(t *testing.T) {
	res := LoadSkills(filepath.Join(t.TempDir(), "nope"))
	if len(res.Skills) != 0 {
		t.Fatalf("目录不存在应返回空，实际 %d", len(res.Skills))
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("目录不存在不应上报 skipped，实际 %v", res.Skipped)
	}
}

// TestComposeSkillInstructions_StableOrderAndFilter 拼接顺序稳定且只含启用项。
func TestComposeSkillInstructions_StableOrderAndFilter(t *testing.T) {
	skills := []Skill{
		{Name: "zeta", Instructions: "Z 指令"},
		{Name: "alpha", Instructions: "A 指令", WhenToUse: "当 A 时"},
		{Name: "beta", Instructions: "B 指令"},
	}
	// 传入顺序刻意打乱，且包含一个已不存在的技能名。
	out := ComposeSkillInstructions(skills, []string{"zeta", "alpha", "deleted-skill"})
	if !strings.Contains(out, "A 指令") || !strings.Contains(out, "Z 指令") {
		t.Fatalf("启用的技能内容应被拼入，实际 %q", out)
	}
	if strings.Contains(out, "B 指令") {
		t.Fatal("未启用的技能不得出现")
	}
	if !strings.Contains(out, "适用时机：当 A 时") {
		t.Fatalf("whenToUse 应作为适用时机写入，实际 %q", out)
	}
	// 稳定性：同一集合再算一次必须字节相同（否则上下文缓存全失效）。
	again := ComposeSkillInstructions(skills, []string{"alpha", "zeta"})
	if out != again {
		t.Fatalf("同一技能集合必须产出相同文本\n第一次=%q\n第二次=%q", out, again)
	}
	// alpha 应在 zeta 之前（升序）。
	if strings.Index(out, "A 指令") > strings.Index(out, "Z 指令") {
		t.Fatal("技能应按名字升序拼接")
	}
}

// TestComposeSkillInstructions_EmptyWhenNothingEnabled 空启用集返回空串（不产生空标题）。
func TestComposeSkillInstructions_EmptyWhenNothingEnabled(t *testing.T) {
	skills := []Skill{{Name: "a", Instructions: "x"}}
	if out := ComposeSkillInstructions(skills, nil); out != "" {
		t.Fatalf("未启用任何技能应返回空串，实际 %q", out)
	}
	if out := ComposeSkillInstructions(skills, []string{"missing"}); out != "" {
		t.Fatalf("启用的技能全不存在时应返回空串，实际 %q", out)
	}
}

// TestSystemPromptWithSkills_ComposesEnabled 技能内容必须真的拼进系统提示词。
//
// 这条守的是「接进对话」这一步：装载器与拼接函数各自单测全绿，
// 也可能因为调用点漏改而完全没有生效（本仓历史上 SetTargetVerifier 就是这样：
// 实现完整、单测齐全，但全仓无一处调用）。因此这里断言**服务级的组装结果**。
func TestSystemPromptWithSkills_ComposesEnabled(t *testing.T) {
	svc := &AssistantChatService{}
	svc.skills = []Skill{
		{Name: "alpha", Description: "A", Instructions: "A 指令文本", WhenToUse: "当 A 时"},
		{Name: "beta", Description: "B", Instructions: "B 指令文本"},
	}

	// 默认（未指定启用列表）= 全部启用。
	got := svc.systemPromptWithSkills("基础提示")
	if !strings.HasPrefix(got, "基础提示") {
		t.Fatalf("必须保留基础提示词在前，实际 %q", got)
	}
	if !strings.Contains(got, "A 指令文本") || !strings.Contains(got, "B 指令文本") {
		t.Fatalf("默认应拼入全部技能，实际 %q", got)
	}

	// 显式白名单只拼入指定的。
	svc.SetEnabledSkillNames([]string{"beta"})
	only := svc.systemPromptWithSkills("基础提示")
	if strings.Contains(only, "A 指令文本") {
		t.Fatalf("白名单外的技能不得拼入，实际 %q", only)
	}
	if !strings.Contains(only, "B 指令文本") {
		t.Fatalf("白名单内的技能应拼入，实际 %q", only)
	}
}

// TestSystemPromptWithSkills_NoSkillsKeepsBasePrompt 没有技能时不得多出空标题。
func TestSystemPromptWithSkills_NoSkillsKeepsBasePrompt(t *testing.T) {
	svc := &AssistantChatService{}
	if got := svc.systemPromptWithSkills("基础提示"); got != "基础提示" {
		t.Fatalf("无技能时应原样返回，实际 %q", got)
	}
}

// TestSetSkillsRoot_LoadsAndExposesSkipped 装载结果与被跳过项都要能被管理端点读到。
func TestSetSkillsRoot_LoadsAndExposesSkipped(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ok")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\ndescription: 可用技能\n---\n正文\n"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	bad := filepath.Join(root, "bad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("# 没有 frontmatter\n"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	svc := &AssistantChatService{}
	res := svc.SetSkillsRoot(root)
	if len(res.Skills) != 1 {
		t.Fatalf("应装载 1 个技能，实际 %d", len(res.Skills))
	}
	skills, skipped := svc.SkillsSnapshot()
	if len(skills) != 1 || skills[0].Name != "ok" {
		t.Fatalf("快照应含 ok 技能，实际 %+v", skills)
	}
	if _, ok := skipped["bad"]; !ok {
		t.Fatalf("坏技能必须出现在 skipped 里，实际 %v", skipped)
	}
	// 快照必须是副本：外部改动不得污染服务内部状态。
	skipped["注入"] = "x"
	_, again := svc.SkillsSnapshot()
	if _, ok := again["注入"]; ok {
		t.Fatal("SkillsSnapshot 必须返回副本，不能把内部 map 直接交出去")
	}
}
