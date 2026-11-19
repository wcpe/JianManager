package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Agent 运维知识的宿主侧安装。
//
// 双通道取内容：
//   - CP 静态路由：`GET /api/v1/skill/<path>`，匿名可读，内网无外网时可用（默认）
//   - 本地目录：`--from <dir>`，直接指向仓库根 `skill/`，便于开发与离线场景
//
// 安装目标是宿主各自的 skills 目录。三个宿主（claude / codex / cursor）都走
// 「一个技能一个子目录」的约定，区别只在根目录：
//
//	claude  → ~/.claude/skills/jianmanager
//	codex   → ~/.codex/skills/jianmanager
//	cursor  → ~/.cursor/skills/jianmanager
//
// 之所以统一加一层 `jianmanager/` 子目录：宿主按子目录发现技能，SKILL.md 必须位于
// 该子目录根部；直接摊在 skills/ 下会和用户已有的其它技能混淆。

// skillEntry 是一条待安装的知识文件（相对路径 + 内容）。
type skillEntry struct {
	Rel  string
	Body []byte
}

// skillManifest 是内嵌知识的文件清单。
//
// 与仓库根 `skill/` 目录一一对应。之所以在客户端写死清单而不依赖 CP 端列目录：
// CP 只提供单文件读取（没有目录列表接口），且清单变化本就随二进制版本走，
// 写死可让「版本与内容」严格对应，避免读到服务端更新的、当前客户端不认识的条目。
var skillManifest = []string{
	"SKILL.md",
	"references/destructive-ops.md",
	"references/instance-lifecycle.md",
	"references/policy-and-diagnostics.md",
}

// hostSkillRoot 返回某宿主的**用户级** skills 根目录。
//
// 覆盖三类来源：
//   - 单机 agent 客户端：claude（Claude Code）、codex、cursor
//   - 常见编辑器/IDE 侧的兼容约定：kiro、copilot
//   - `.agents/skills`：跨工具的通用约定，多个 harness 会一并扫描
//
// 工作区级目录（相对当前项目，如 `<ws>/.claude/skills`）由 workspaceSkillRoot 处理：
// 它们不是「这台机器装了哪个宿主」，而是「这个项目想装给谁」，两者不能混为一谈。
func hostSkillRoot(host string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户主目录: %w", err)
	}
	switch host {
	case "claude":
		return filepath.Join(home, ".claude", "skills"), nil
	case "codex":
		return filepath.Join(home, ".codex", "skills"), nil
	case "cursor":
		return filepath.Join(home, ".cursor", "skills"), nil
	case "kiro":
		return filepath.Join(home, ".kiro", "skills"), nil
	case "copilot":
		return filepath.Join(home, ".copilot", "skills"), nil
	case "agents":
		return filepath.Join(home, ".agents", "skills"), nil
	default:
		return "", fmt.Errorf("未知宿主 %q；支持 claude / codex / cursor / kiro / copilot / agents", host)
	}
}

// workspaceSkillRoot 返回某宿主在**当前项目**内的 skills 目录。
//
// 与用户级目录并存是刻意的：Claude Code、Codex、Kiro 等都会优先读工作区下的
// 同名目录，这样技能可以随仓库分发给所有协作者，而不是只装给运行安装命令的那台机器。
// Vantaloom 与 NarraFork 都按这个约定组织各自的技能库。
func workspaceSkillRoot(host, projectDir string) (string, error) {
	switch host {
	case "claude":
		return filepath.Join(projectDir, ".claude", "skills"), nil
	case "codex":
		return filepath.Join(projectDir, ".codex", "skills"), nil
	case "cursor":
		return filepath.Join(projectDir, ".cursor", "skills"), nil
	case "kiro":
		return filepath.Join(projectDir, ".kiro", "skills"), nil
	case "copilot":
		return filepath.Join(projectDir, ".github", "skills"), nil
	case "agents":
		return filepath.Join(projectDir, ".agents", "skills"), nil
	default:
		return "", fmt.Errorf("未知宿主 %q；支持 claude / codex / cursor / kiro / copilot / agents", host)
	}
}

// 安装作用域：user 装给本机所有项目，project 装进当前仓库（随仓库分发）。
const (
	skillScopeUser    = "user"
	skillScopeProject = "project"
)

// skillHosts 是全部支持的宿主，顺序即候选列表的展示顺序。
//
// 前三个是最常见的 agent 客户端，排前面让默认序号落在最可能的选择上。
var skillHosts = []string{"claude", "codex", "cursor", "kiro", "copilot", "agents"}

// detectHosts 探测本机/本项目已存在哪些宿主的配置目录。
//
// 判定「已存在」用宿主自身的配置根（如 `~/.kiro`），而不是 skills 子目录——
// 用户刚装好宿主、还没建 skills 目录时也应算作可用目标。
// 项目级目录优先级更高：同一宿主在项目里装了，说明这个仓库就是想分发给它。
func detectHosts(projectDir string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	mark := func(host string) {
		if !seen[host] {
			seen[host] = true
			out = append(out, host)
		}
	}

	for _, host := range skillHosts {
		// 项目级：`<ws>/.<host>` 或 copilot 的 `.github`。
		if root, err := workspaceSkillRoot(host, projectDir); err == nil {
			base := filepath.Dir(root)
			if st, err := os.Stat(base); err == nil && st.IsDir() {
				mark(host)
				continue
			}
		}
		// 用户级：`~/.<host>`。
		if st, err := os.Stat(filepath.Join(home, "."+host)); err == nil && st.IsDir() {
			mark(host)
		}
	}
	return out
}

// joinSkillDir 拼出某个 skills 根下本技能的安装目录。
//
// 统一多一层 `jianmanager/`：宿主按子目录发现技能，SKILL.md 必须位于该子目录根部。
func joinSkillDir(skillRoot string) string {
	return filepath.Join(skillRoot, "jianmanager")
}

// runSkill 分发 `jm skill <sub>`。
func runSkill(cfg Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: jm skill install [--host <host>]... [--scope user|project] [--dir <path>] [--from <本地目录>] [--dry-run]")
	}
	sub := args[0]
	switch sub {
	case "install":
		return runSkillInstall(cfg, args[1:])
	case "list":
		return runSkillList()
	default:
		return fmt.Errorf("未知 skill 子命令 %q；支持 install / list", sub)
	}
}

// runSkillList 打印内置清单，便于用户知道会装什么。
func runSkillList() error {
	for _, rel := range skillManifest {
		fmt.Println(rel)
	}
	return nil
}

// skillInstallOptions 是 install 的解析结果。
type skillInstallOptions struct {
	hosts  []string
	dir    string
	from   string
	dryRun bool
	// scope 决定装到用户级还是项目级；project 为项目根（scope=project 时用）。
	scope   string
	project string
}

// parseSkillInstallArgs 解析 install 参数。
//
// --host 可重复，也可逗号分隔；不传则自动探测。
func parseSkillInstallArgs(args []string) (skillInstallOptions, error) {
	var opt skillInstallOptions
	for i := 0; i < len(args); i++ {
		a := args[i]
		needValue := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s 需要一个值", a)
			}
			i++
			return args[i], nil
		}
		switch a {
		case "--host":
			v, err := needValue()
			if err != nil {
				return opt, err
			}
			for _, h := range strings.Split(v, ",") {
				if h = strings.TrimSpace(h); h != "" {
					opt.hosts = append(opt.hosts, h)
				}
			}
		case "--dir":
			v, err := needValue()
			if err != nil {
				return opt, err
			}
			opt.dir = v
		case "--from":
			v, err := needValue()
			if err != nil {
				return opt, err
			}
			opt.from = v
		case "--scope":
			v, err := needValue()
			if err != nil {
				return opt, err
			}
			v = strings.ToLower(strings.TrimSpace(v))
			if v != skillScopeUser && v != skillScopeProject {
				return opt, fmt.Errorf("--scope 仅支持 user 或 project，收到 %q", v)
			}
			opt.scope = v
		case "--project":
			v, err := needValue()
			if err != nil {
				return opt, err
			}
			opt.project = v
		case "--dry-run":
			opt.dryRun = true
		case "-h", "--help":
			return opt, fmt.Errorf("用法: jm skill install [--host <host>]... [--scope user|project] [--dir <path>] [--from <本地目录>] [--dry-run]")
		default:
			return opt, fmt.Errorf("未知参数 %q", a)
		}
	}
	return opt, nil
}

// runSkillInstall 把知识文件装进宿主 skills 目录。
//
// 三种入口，按明确度递增：
//   - 无参数 + 交互终端：列出候选位置让用户选（默认全选已探测到的宿主）
//   - 无参数 + 非交互（管道 / CI / JM_NO_PROMPT）：直接装到全部已探测宿主，不阻塞
//   - 显式 --host / --dir：按指定目标装，不提示
func runSkillInstall(cfg Config, args []string) error {
	opt, err := parseSkillInstallArgs(args)
	if err != nil {
		return err
	}

	// 作用域与项目根：--scope 未指定时按 user（装给本机）。
	scope := opt.scope
	if scope == "" {
		scope = skillScopeUser
	}
	projectDir := opt.project
	if projectDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("无法确定当前目录: %w", err)
		}
		projectDir = cwd
	}

	// resolveRoot 把「某个宿主」解析成该作用域下的 skills 根。
	resolveRoot := func(host string) (string, error) {
		if scope == skillScopeProject {
			return workspaceSkillRoot(host, projectDir)
		}
		return hostSkillRoot(host)
	}

	var roots []string

	switch {
	case opt.dir != "":
		// 显式目录优先，hosts/scope 都不参与路径推导。
		roots = []string{opt.dir}

	case len(opt.hosts) > 0:
		for _, h := range opt.hosts {
			root, err := resolveRoot(h)
			if err != nil {
				return err
			}
			roots = append(roots, joinSkillDir(root))
		}

	default:
		// 未指定目标：交互环境让用户选，否则沿用「装到全部已探测宿主」。
		// --dry-run 也走交互：先选再看落点，比「不给选就预览全部」更符合预期。
		detected := detectHosts(projectDir)
		if isInteractive() {
			picked, customDir, err := promptTargets(buildTargets(detected, scope, projectDir))
			if err != nil {
				if err == errUserCancelled {
					fmt.Println("已取消。")
					return nil
				}
				return err
			}
			if customDir != "" {
				roots = []string{customDir}
			} else {
				for _, t := range picked {
					roots = append(roots, t.Path)
				}
			}
		} else {
			if len(detected) == 0 {
				return fmt.Errorf("未探测到任何宿主（~/.claude、~/.codex、~/.cursor、~/.kiro、~/.copilot、~/.agents 均不存在）；请用 --host 指定，或 --dir 直接给目录")
			}
			for _, h := range detected {
				root, err := resolveRoot(h)
				if err != nil {
					return err
				}
				roots = append(roots, joinSkillDir(root))
			}
		}
	}

	entries, srcDesc, err := loadSkillEntries(cfg, opt.from)
	if err != nil {
		return err
	}

	for _, root := range roots {
		for _, e := range entries {
			dst := filepath.Join(root, filepath.FromSlash(e.Rel))
			if opt.dryRun {
				fmt.Printf("[dry-run] %s  (%d 字节)\n", dst, len(e.Body))
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fmt.Errorf("创建目录失败 %s: %w", filepath.Dir(dst), err)
			}
			if err := os.WriteFile(dst, e.Body, 0o644); err != nil {
				return fmt.Errorf("写入失败 %s: %w", dst, err)
			}
			fmt.Printf("已安装 %s\n", dst)
		}
	}

	if !opt.dryRun {
		fmt.Printf("\n共 %d 个文件 × %d 个位置，来源：%s\n", len(entries), len(roots), srcDesc)
	}
	return nil
}

// loadSkillEntries 按通道取全部知识文件。
//
// 优先本地目录（--from），否则走 CP 静态路由。返回内容来源描述供用户核对。
func loadSkillEntries(cfg Config, from string) ([]skillEntry, string, error) {
	if from != "" {
		entries, err := loadSkillFromDir(from)
		return entries, "本地目录 " + from, err
	}
	entries, err := loadSkillFromCP(cfg)
	return entries, "CP " + cfg.BaseURL + "/api/v1/skill", err
}

// loadSkillFromDir 从本地目录读取（开发/离线场景）。
func loadSkillFromDir(dir string) ([]skillEntry, error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("本地 skill 目录不可用: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("--from 需要目录，收到文件: %s", dir)
	}
	out := make([]skillEntry, 0, len(skillManifest))
	for _, rel := range skillManifest {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", rel, err)
		}
		out = append(out, skillEntry{Rel: rel, Body: body})
	}
	return out, nil
}

// loadSkillFromCP 经 CP 匿名静态路由逐条取回。
//
// 该路由不需鉴权，但客户端可能尚未登录（没配 URL）——此时给出可操作的提示，
// 而不是让用户去猜「为什么没登录也能失败」。
func loadSkillFromCP(cfg Config) ([]skillEntry, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("未配置 Control Plane 地址，无法经 CP 拉取；请先用 `jm config set-url <url>` 配置，或用 --from <本地 skill 目录>")
	}
	c := newClient(cfg)
	out := make([]skillEntry, 0, len(skillManifest))
	for _, rel := range skillManifest {
		body, err := c.get("/api/v1/skill/"+rel, nil)
		if err != nil {
			// 连不上/未部署时，把「不依赖 CP 也能装」的出口一并说清楚：
			// 内网与离线场景本就该走 GitHub 或本地 skill/ 目录。
			return nil, fmt.Errorf("拉取 %s 失败: %w\n提示：无外网或 CP 未部署时可改用 `--from <本地 skill 目录>`，或直接 clone 仓库后用 `npx skills add`", rel, err)
		}
		out = append(out, skillEntry{Rel: rel, Body: body})
	}
	return out, nil
}

// skillManifestFromFS 供测试校验清单与内嵌 FS 一致。
//
// 单独抽出来是因为清单写死在客户端，一旦 skill/ 目录新增条目而清单没跟上，
// 安装结果会静默缺文件——这是最难被察觉的一类缺陷。
func skillManifestFromFS(fsys fs.FS) ([]string, error) {
	var out []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}
