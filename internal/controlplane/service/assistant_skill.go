package service

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// AI 助手技能（Skill）装载。
//
// ## 为什么是「目录 + SKILL.md」而不是数据库表
//
// 参照 Vantaloom 的官方技能目录约定（CC 兼容）：一个技能就是一个子目录，
// 内含 `SKILL.md`（YAML frontmatter + Markdown 正文），目录名即技能名。
// 这样做的收益是三件具体的事：
//   - 技能内容可以进版本库、走 code review，与代码同源演进；
//   - 运维可以直接 `git pull` / 手工放文件，不必学一套后台表结构；
//   - 「技能是否存在」在文件系统上可一眼确认，不需要连库查。
//
// 代价是**没有事务与外键**：删掉目录即时生效、误删不可回滚。因此这里的读路径
// 一律「容错装载」——单个技能写坏不得让整个目录装载失败，否则一个错别字
// 会让所有技能一起消失，而管理员只会看到「技能没了」。
//
// ## 为什么技能不得执行代码
//
// 技能是**纯文本指令**，拼进系统提示词。它不是插件、不加载 so/dll、不跑脚本。
// 一旦允许执行，就等于给「能编辑技能的人」开了任意代码执行面，
// 而技能是运营内容、其编辑权限远宽于平台管理权限。

// Skill 一个已装载的技能。
type Skill struct {
	// Name 技能名，取自目录名（不是 frontmatter 里的 name）。
	//
	// 为什么以目录名为准：目录名是「存在性」的唯一真源（有目录才有技能），
	// frontmatter 的 name 只是展示。若两者冲突以目录名为准，并把冲突记在
	// FrontmatterName 里供排障——否则改个 frontmatter 就能让两个技能重名。
	Name string
	// FrontmatterName frontmatter 里声明的 name（可能与 Name 不同，仅用于排障提示）。
	FrontmatterName string
	Description     string
	WhenToUse       string
	Version         string
	// Source 技能来源标记（Vantaloom 用 official 表示官方只读）。
	Source string
	// Instructions Markdown 正文（去掉 frontmatter 后的全部内容）。
	Instructions string
	// Path SKILL.md 的绝对路径，供管理页展示与排障。
	Path string
}

// skillFrontmatter 是 `SKILL.md` 的 YAML 头。
type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	WhenToUse   string `yaml:"whenToUse"`
	Version     string `yaml:"version"`
	Source      string `yaml:"source"`
}

// ErrSkillInvalid 技能文件不可用。
var ErrSkillInvalid = errors.New("技能文件不合法")

// ParseSkillMarkdown 解析一份 `SKILL.md`。
//
// dirName 是技能所在目录名，作为 Name 的真源。
//
// 只接受以 `---` 开头并闭合的 frontmatter；缺失 frontmatter 时**报错而不是猜**：
// 没有 frontmatter 就取不到 description 与 whenToUse，模型无从判断何时该用它，
// 静默收下只会得到一堆永远不会被触发的技能。
func ParseSkillMarkdown(dirName string, data []byte) (*Skill, error) {
	dirName = strings.TrimSpace(dirName)
	if dirName == "" {
		return nil, fmt.Errorf("%w: 目录名为空", ErrSkillInvalid)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("%w: 缺少以 --- 开头的 YAML frontmatter", ErrSkillInvalid)
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, fmt.Errorf("%w: frontmatter 未闭合（缺少结束的 ---）", ErrSkillInvalid)
	}
	header := rest[:end]
	body := rest[end+len("\n---"):]
	// 跳过结束标记后紧跟的一个换行（文件通常写成 `---\n正文`）。
	body = strings.TrimPrefix(body, "\n")

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
		return nil, fmt.Errorf("%w: frontmatter 不是合法 YAML: %v", ErrSkillInvalid, err)
	}
	fm.Description = strings.TrimSpace(fm.Description)
	if fm.Description == "" {
		// 没有 description 的技能无法被模型判断用途，等同不可用。
		return nil, fmt.Errorf("%w: 缺少 description", ErrSkillInvalid)
	}
	instructions := strings.TrimSpace(body)
	if instructions == "" {
		return nil, fmt.Errorf("%w: 正文指令为空", ErrSkillInvalid)
	}
	return &Skill{
		Name:            dirName,
		FrontmatterName: strings.TrimSpace(fm.Name),
		Description:     fm.Description,
		WhenToUse:       strings.TrimSpace(fm.WhenToUse),
		Version:         strings.TrimSpace(fm.Version),
		Source:          strings.TrimSpace(fm.Source),
		Instructions:    instructions,
	}, nil
}

// SkillLoadResult 一次目录装载的结果。
type SkillLoadResult struct {
	// Skills 成功装载的技能，按 Name 升序稳定排列。
	Skills []Skill
	// Skipped 被跳过的条目（子目录名 → 原因）。用于管理页与日志排障：
	// 静默丢弃会让「我明明放了技能怎么没有」无从解释。
	Skipped map[string]string
}

// LoadSkills 扫描技能根目录并装载全部技能。
//
// 目录不存在时返回空结果而不是错误：全新部署没有 skills 目录是正常状态，
// 让它变成一个启动期错误会把「还没放技能」误报成故障。
func LoadSkills(root string) SkillLoadResult {
	res := SkillLoadResult{Skipped: map[string]string{}}
	root = strings.TrimSpace(root)
	if root == "" {
		return res
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if !os.IsNotExist(err) {
			res.Skipped[root] = err.Error()
		}
		return res
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		mdPath := filepath.Join(root, name, "SKILL.md")
		data, rerr := os.ReadFile(mdPath)
		if rerr != nil {
			// 没有 SKILL.md 的子目录不是技能（约定如此），不算错误、也不上报。
			if os.IsNotExist(rerr) {
				continue
			}
			res.Skipped[name] = rerr.Error()
			continue
		}
		skill, perr := ParseSkillMarkdown(name, data)
		if perr != nil {
			res.Skipped[name] = perr.Error()
			continue
		}
		skill.Path = mdPath
		res.Skills = append(res.Skills, *skill)
	}
	sort.Slice(res.Skills, func(i, j int) bool { return res.Skills[i].Name < res.Skills[j].Name })
	return res
}

// SetSkillsRoot 配置技能根目录并立即装载一次（装配层在启动时调用）。
//
// 装载结果缓存在服务上：构建系统提示词是每轮 run 的热路径，不能每轮读盘。
// 被跳过的技能在这里**记 WARN 日志**——否则「我明明放了技能却没有」无从解释，
// 而 Skipped 只有打开管理端点才看得到，等于默认不可见。
func (s *AssistantChatService) SetSkillsRoot(root string) SkillLoadResult {
	res := LoadSkills(root)
	s.skillsMu.Lock()
	s.skillRoot = strings.TrimSpace(root)
	s.skills = res.Skills
	s.skillSkipped = res.Skipped
	s.skillsMu.Unlock()

	for name, reason := range res.Skipped {
		slog.Warn("AI 助手技能装载失败，已跳过", "skill", name, "reason", reason, "root", root)
	}
	slog.Info("AI 助手技能已装载", "root", root, "loaded", len(res.Skills), "skipped", len(res.Skipped))
	return res
}

// ReloadSkills 重新扫描技能目录（供管理页「重新扫描」触发）。
//
// 目录未配置时返回空结果而不是报错：技能是可选内容，未配置属正常状态。
func (s *AssistantChatService) ReloadSkills() SkillLoadResult {
	s.skillsMu.RLock()
	root := s.skillRoot
	s.skillsMu.RUnlock()
	if root == "" {
		return SkillLoadResult{Skipped: map[string]string{}}
	}
	return s.SetSkillsRoot(root)
}

// SetEnabledSkillNames 设置启用的技能白名单；传空表示「全部已装载技能都启用」。
func (s *AssistantChatService) SetEnabledSkillNames(names []string) {
	s.skillsMu.Lock()
	s.skillEnabled = append([]string(nil), names...)
	s.skillsMu.Unlock()
}

// SkillsSnapshot 返回当前已装载技能与被跳过项（供管理端点）。
func (s *AssistantChatService) SkillsSnapshot() ([]Skill, map[string]string) {
	s.skillsMu.RLock()
	defer s.skillsMu.RUnlock()
	skills := append([]Skill(nil), s.skills...)
	skipped := make(map[string]string, len(s.skillSkipped))
	for k, v := range s.skillSkipped {
		skipped[k] = v
	}
	return skills, skipped
}

// systemPromptWithSkills 在基础系统提示词后依次拼入「自定义系统提示词」与「已启用技能」。
//
// 组装顺序固定为：内置基线 → 自定义 → 技能（§3.1）。
// 三层各自可为空；全空时只剩基线。
//
// 空启用列表 = 全部启用（见 skillEnabled 的注释）。没有任何技能时**不追加空标题**——
// 多出一行「## 已启用技能」而没有内容，只会让模型困惑。
func (s *AssistantChatService) systemPromptWithSkills(base string) string {
	prompt := base
	// 自定义提示词取设置表里的值。读失败**不阻断**：DB 抖一下不该让整轮对话失败，
	// 退化到「只有基线」比报错更符合预期（内容层面缺一段，比完全不能用好）。
	if custom := s.customSystemPrompt(); custom != "" {
		prompt = prompt + "\n\n" + custom
	}

	s.skillsMu.RLock()
	skills := s.skills
	enabled := s.skillEnabled
	s.skillsMu.RUnlock()

	names := enabled
	if len(names) == 0 {
		names = make([]string, 0, len(skills))
		for _, sk := range skills {
			names = append(names, sk.Name)
		}
	}
	block := ComposeSkillInstructions(skills, names)
	if block == "" {
		return prompt
	}
	return prompt + "\n\n" + block
}

// builtinToolsEnabled 读「平台自带工具」开关的生效值。
//
// 读失败按**启用**处理：DB 抖一下不该让助手突然失去全部工具
// （那会让正在排障的会话变成「什么都不能用」，而原因与故障无关）。
func (s *AssistantChatService) builtinToolsEnabled() bool {
	if s.db == nil {
		return true
	}
	var setting model.AssistantSetting
	if err := s.db.First(&setting, 1).Error; err != nil {
		return true
	}
	return builtinToolsEnabled(setting)
}

// customSystemPrompt 读设置表里的自定义系统提示词（空串表示未配置）。
func (s *AssistantChatService) customSystemPrompt() string {
	if s.db == nil {
		return ""
	}
	var setting model.AssistantSetting
	if err := s.db.First(&setting, 1).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(setting.SystemPrompt)
}

// ComposeSkillInstructions 把选中的技能拼成系统提示词片段。
//
// ## 为什么顺序必须稳定
//
// 同样的技能集合必须产出同样的提示词。否则每轮拼出的文本都不同，
// 上下文缓存全部失效（成本上升），且「同一问题两次回答不一致」将无从归因。
// 因此按 Name 升序，与调用方传入顺序无关。
//
// 未命中的技能名**静默忽略**：会话里记录的启用列表可能包含已被删除的技能，
// 让整轮失败等于「删掉一个技能导致历史会话全部不能继续」。
func ComposeSkillInstructions(skills []Skill, enabledNames []string) string {
	if len(enabledNames) == 0 || len(skills) == 0 {
		return ""
	}
	want := make(map[string]struct{}, len(enabledNames))
	for _, n := range enabledNames {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = struct{}{}
		}
	}
	selected := make([]Skill, 0, len(want))
	for _, s := range skills {
		if _, ok := want[s.Name]; ok {
			selected = append(selected, s)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	if len(selected) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 已启用技能\n\n")
	for _, s := range selected {
		b.WriteString("### ")
		b.WriteString(s.Name)
		b.WriteString("\n\n")
		if s.WhenToUse != "" {
			b.WriteString("适用时机：")
			b.WriteString(s.WhenToUse)
			b.WriteString("\n\n")
		}
		b.WriteString(s.Instructions)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}
