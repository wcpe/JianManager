package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wcpe/JianManager/internal/platform/keyring"
	"github.com/wcpe/JianManager/internal/version"
)

// `jm doctor`：一次性回答「我现在用的是哪个 jm、配置在哪、能不能连上」。
//
// 为什么不做 `jm install`：一个正在运行的程序无法安全替换自身（Windows 下
// 运行中的 .exe 既不能覆盖也不能删除），而且自行挪动二进制会破坏 npm / pnpm
// 对它的所有权——升级时又会装出第二份。主流 CLI（gh / kubectl / helm）都不自带
// install，把安装交给包管理器。
//
// 但「我装的是哪个、配置在哪」确实需要能查到，所以这里只做**只读诊断**：
// 不移动、不删除、不写任何文件。

// doctorReport 是诊断结果，便于 JSON 输出与测试断言。
type doctorReport struct {
	Version      string            `json:"version"`
	GOOS         string            `json:"goos"`
	GOARCH       string            `json:"goarch"`
	Executable   string            `json:"executable"`
	InstallVia   string            `json:"installVia"`
	ConfigPath   string            `json:"configPath"`
	ConfigOK     bool              `json:"configOk"`
	ConfigErr    string            `json:"configError,omitempty"`
	Hosts        int               `json:"hosts"`
	CurrentHost  string            `json:"currentHost,omitempty"`
	Username     string            `json:"username,omitempty"`
	TokenPrefix  string            `json:"tokenPrefix,omitempty"`
	TokenSource  string            `json:"tokenSource"`
	KeyringOK    bool              `json:"keyringAvailable"`
	KeyringNote  string            `json:"keyringNote,omitempty"`
	EnvOverrides map[string]string `json:"envOverrides,omitempty"`
	Warnings     []string          `json:"warnings,omitempty"`
}

// runDoctor 汇总环境信息并打印。
//
// 刻意不做网络请求：doctor 的用途是在「连不上」时排查，此时再去请求 CP
// 只会让命令跟着超时。连通性由 `jm auth status` 与具体命令验证。
func runDoctor(cfg Config, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("用法: %s doctor", progName())
	}

	rep := doctorReport{
		Version:     version.Version,
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		TokenSource: "无",
	}

	// 可执行文件位置：直接回答「我在用哪一份 jm」。
	rep.Executable = executablePath()
	rep.InstallVia = installChannel(rep.Executable)

	// 配置文件。
	if p, err := configPath(); err == nil {
		rep.ConfigPath = p
	}
	if cf, err := loadConfigFile(); err != nil {
		rep.ConfigErr = err.Error()
	} else {
		rep.ConfigOK = true
		rep.Hosts = len(cf.Hosts)
	}

	rep.CurrentHost = cfg.BaseURL
	rep.Username = cfg.Username
	rep.TokenPrefix = cfg.TokenPrefix
	rep.TokenSource = tokenSourceOf(cfg)

	// 钥匙串可用性：不可用不是错误，但要让用户知道 token 存在哪。
	if _, err := keyring.Open(); err == nil {
		rep.KeyringOK = true
	} else {
		rep.KeyringNote = err.Error()
	}

	// 环境变量覆盖：排查「为什么连的不是我配的地址」时的头号原因。
	env := map[string]string{}
	for _, name := range []string{envURL, envToken} {
		if v := os.Getenv(name); v != "" {
			if name == envToken {
				env[name] = maskToken(v)
			} else {
				env[name] = v
			}
		}
	}
	if len(env) > 0 {
		rep.EnvOverrides = env
	}

	// 可操作告警。
	rep.Warnings = buildDoctorWarnings(cfg)
	if !rep.ConfigOK {
		rep.Warnings = append(rep.Warnings, "配置文件读取失败，请检查上方 configPath 的权限与格式")
	}
	if !rep.KeyringOK && cfg.Token == "" {
		rep.Warnings = append(rep.Warnings, "系统钥匙串不可用且未设置令牌：登录前请先解决钥匙串，或改用 "+envToken)
	}

	if cfg.Output == "json" {
		return printJSON(rep)
	}
	printDoctor(rep)
	return nil
}

// buildDoctorWarnings 生成「可操作」的告警。
//
// 抽成纯函数是为了能单测：每条告警都必须指出问题**并给出下一步命令**，
// 只说「有异常」等于没说。钥匙串相关的告警依赖运行期探测结果，
// 由调用方按需追加，不放在这里。
func buildDoctorWarnings(cfg Config) []string {
	var out []string
	if cfg.BaseURL == "" {
		out = append(out, "尚未配置 Control Plane 地址：运行 `"+progName()+" config set-url <url>`")
	}
	if cfg.Token == "" {
		out = append(out, "尚未登录：运行 `"+progName()+" auth login`")
	}
	if os.Getenv(envURL) != "" || os.Getenv(envToken) != "" {
		out = append(out, "检测到环境变量覆盖，其优先级高于配置文件")
	}
	return out
}

// printDoctor 以易读形式打印。
func printDoctor(rep doctorReport) {
	fmt.Fprintf(os.Stdout, "版本:       %s\n", rep.Version)
	fmt.Fprintf(os.Stdout, "平台:       %s/%s\n", rep.GOOS, rep.GOARCH)
	fmt.Fprintf(os.Stdout, "可执行文件: %s\n", orDash(rep.Executable))
	fmt.Fprintf(os.Stdout, "安装方式:   %s\n", rep.InstallVia)
	fmt.Fprintf(os.Stdout, "配置目录:   %s\n", orDash(rep.ConfigPath))
	if rep.ConfigOK {
		fmt.Fprintf(os.Stdout, "已配置主机: %d 个\n", rep.Hosts)
	} else {
		fmt.Fprintf(os.Stdout, "配置状态:   读取失败（%s）\n", rep.ConfigErr)
	}
	fmt.Fprintf(os.Stdout, "当前主机:   %s\n", orDash(rep.CurrentHost))
	fmt.Fprintf(os.Stdout, "登录用户:   %s\n", orDash(rep.Username))
	fmt.Fprintf(os.Stdout, "令牌:       %s（来源：%s）\n", orDash(rep.TokenPrefix), rep.TokenSource)
	if rep.KeyringOK {
		fmt.Fprintf(os.Stdout, "系统钥匙串: 可用\n")
	} else {
		fmt.Fprintf(os.Stdout, "系统钥匙串: 不可用\n            %s\n", rep.KeyringNote)
	}
	if len(rep.EnvOverrides) > 0 {
		fmt.Fprintf(os.Stdout, "环境覆盖:\n")
		for _, k := range []string{envURL, envToken} {
			if v, ok := rep.EnvOverrides[k]; ok {
				fmt.Fprintf(os.Stdout, "  %-10s %s\n", k, v)
			}
		}
	}
	if len(rep.Warnings) > 0 {
		fmt.Fprintf(os.Stdout, "\n需要注意:\n")
		for _, w := range rep.Warnings {
			fmt.Fprintf(os.Stdout, "  · %s\n", w)
		}
	}
}

// executablePath 返回当前二进制的绝对路径（解析符号链接后的真实位置）。
//
// npm / pnpm 的全局命令是包装脚本或符号链接，用户常因此搞不清真正运行的是哪份。
// 解析到真实文件才能回答「我到底在跑哪个版本」。
func executablePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}

// installChannel 按可执行文件所在目录推断安装渠道。
//
// 只做启发式判断，用于给用户一条「该怎么升级」的线索，不参与任何决策。
func installChannel(exe string) string {
	if exe == "" {
		return "未知"
	}
	// 统一成小写正斜杠便于跨平台匹配。
	norm := strings.ToLower(strings.ReplaceAll(exe, "\\", "/"))
	switch {
	case strings.Contains(norm, "/.pnpm/"):
		return "pnpm（全局）"
	case strings.Contains(norm, "/npm-global/"), strings.Contains(norm, "/node_modules/"), strings.Contains(norm, "/npm/"):
		return "npm（全局）"
	case strings.Contains(norm, "/.local/bin/"):
		return "手动安装（~/.local/bin）"
	case strings.Contains(norm, "/go/bin/"):
		return "go install"
	default:
		// 仓库内构建的产物（bin/jm.exe）或用户自行放置。
		return "手动安装或本地构建"
	}
}

// tokenSourceOf 说明当前令牌从哪来，排查「为什么用的是旧令牌」时的关键信息。
func tokenSourceOf(cfg Config) string {
	if cfg.Token == "" {
		return "无"
	}
	if os.Getenv(envToken) != "" && cfg.Token == os.Getenv(envToken) {
		return envToken + " 环境变量"
	}
	return "系统钥匙串"
}

// maskToken 只留前缀与尾部，避免 doctor 输出把完整令牌写进日志或截图。
func maskToken(t string) string {
	if len(t) <= 12 {
		return tokenPrefixOf(t) + "…"
	}
	return t[:8] + "…" + t[len(t)-4:]
}

// orDash 空值显示为短横线，避免输出出现空洞。
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
