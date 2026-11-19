package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// doctor 的每项结论都要能被单测断言，避免只靠肉眼看输出。
// 这里覆盖三类：安装渠道推断、令牌来源判定、告警生成。

// 安装渠道按可执行文件路径推断，给用户「该怎么升级」的线索。
func TestInstallChannel(t *testing.T) {
	cases := map[string]string{
		// npm 全局：包在 node_modules 下，Windows 为反斜杠
		`C:\Users\u\AppData\Roaming\npm\node_modules\@jianmanager\jm\bin\jm.js`: "npm（全局）",
		`/usr/lib/node_modules/@jianmanager/jm/bin/jm`:                         "npm（全局）",
		// pnpm：实体在 .pnpm 目录
		`/home/u/.local/share/pnpm/.pnpm/@jianmanager+jm@0.24.0/node_modules/@jianmanager/jm/bin/jm`: "pnpm（全局）",
		// 手动安装
		`/home/u/.local/bin/jm`: "手动安装（~/.local/bin）",
		// go install
		`/home/u/go/bin/jm`: "go install",
		// 仓库内构建
		`/repo/bin/jm`: "手动安装或本地构建",
	}
	for exe, want := range cases {
		assert.Equal(t, want, installChannel(exe), "路径: %s", exe)
	}
	assert.Equal(t, "未知", installChannel(""), "空路径应为未知")
}

// 令牌来源要能区分环境变量与钥匙串——排查「为什么用的是旧令牌」的关键。
func TestTokenSourceOf(t *testing.T) {
	assert.Equal(t, "无", tokenSourceOf(Config{}))

	// 设置环境变量并让 cfg.Token 与之相同：应判为环境变量来源。
	t.Setenv(envToken, "jmut_test_env_abc")
	assert.Equal(t, envToken+" 环境变量", tokenSourceOf(Config{Token: "jmut_test_env_abc"}))

	// cfg.Token 与环境变量不同：说明来自钥匙串。
	assert.Equal(t, "系统钥匙串", tokenSourceOf(Config{Token: "jmut_test_other_xyz"}))
}

// 告警必须可操作：指出问题且给出下一步命令，而不是只说「有异常」。
func TestDoctorWarnings(t *testing.T) {
	// 未配置地址 + 未登录：两条告警都要出现。
	t.Setenv(envURL, "")
	t.Setenv(envToken, "")
	rep := buildDoctorWarnings(Config{})
	joined := strings.Join(rep, "\n")
	assert.Contains(t, joined, "尚未配置 Control Plane 地址")
	assert.Contains(t, joined, "config set-url", "应给出具体命令")
	assert.Contains(t, joined, "尚未登录")
	assert.Contains(t, joined, "auth login", "应给出具体命令")

	// 已配置且已登录：不应再提示这两条。
	rep = buildDoctorWarnings(Config{BaseURL: "http://cp", Token: "jmut_x"})
	joined = strings.Join(rep, "\n")
	assert.NotContains(t, joined, "尚未配置")
	assert.NotContains(t, joined, "尚未登录")
}

// 环境变量覆盖要在 doctor 里可见：这是「连的不是我配的地址」的头号原因。
func TestDoctorEnvOverrides(t *testing.T) {
	t.Setenv(envURL, "http://from-env")
	t.Setenv(envToken, "jmut_test_secret_value_123456")
	rep := buildDoctorWarnings(Config{BaseURL: "http://from-env", Token: "jmut_test_secret_value_123456"})
	joined := strings.Join(rep, "\n")
	assert.Contains(t, joined, "环境变量覆盖")
}

// maskToken 不能在输出里留下完整令牌（截图/日志泄露面）。
func TestMaskToken(t *testing.T) {
	full := "jmut_abcdefghijklmnopqrstuvwxyz"
	masked := maskToken(full)
	assert.NotEqual(t, full, masked, "不得原样输出完整令牌")
	assert.Contains(t, masked, "…", "应含省略标记")
	assert.Less(t, len(masked), len(full), "掩码后应更短")

	// 短令牌也不能露全。
	short := "jmut_ab"
	assert.NotEqual(t, short, maskToken(short))
}
