package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDeriveLogRuleKey 验证由实例的 role + launch_spec.coreJar 推导日志采集规则名
// （FR-502 §3.1）。推导结果经 proto 下发给 Worker，决定活动日志的物理路径规则。
func TestDeriveLogRuleKey(t *testing.T) {
	cases := []struct {
		name    string
		role    string
		coreJar string
		want    string
	}{
		// 代理系：按核心 jar 名区分 Bungee / Waterfall / Velocity
		{"bungee 代理", "proxy", "BungeeCord.jar", "bungee"},
		{"bungee 代理（版本化文件名）", "proxy", "BungeeCord-1.20.jar", "bungee"},
		{"waterfall 代理", "proxy", "Waterfall.jar", "waterfall"},
		{"velocity 代理", "proxy", "velocity-3.3.0-SNAPSHOT-123.jar", "velocity"},
		{"代理但 jar 未识别", "proxy", "some-proxy.jar", "bukkit"},
		{"代理且 coreJar 为空", "proxy", "", "bukkit"},

		// 后端：Paper / Spigot / Bukkit 一律 bukkit
		{"paper 后端", "backend", "paper-1.20.1-196.jar", "bukkit"},
		{"spigot 后端", "backend", "spigot-1.20.1.jar", "bukkit"},
		{"后端且 coreJar 为空", "backend", "", "bukkit"},

		// 其他角色：回退 bukkit（不影响非 MC 实例）
		{"通用实例", "universal", "", "bukkit"},
		{"beacon 实例", "beacon", "", "bukkit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := deriveLogRuleKey(c.role, c.coreJar)
			require.Equal(t, c.want, got,
				"role=%q coreJar=%q 应推导为 %q", c.role, c.coreJar, c.want)
		})
	}
}

// TestDeriveLogRuleKeyCaseInsensitive 验证识别不受大小写影响
// （不同核心发行物的文件名大小写并不统一）。
func TestDeriveLogRuleKeyCaseInsensitive(t *testing.T) {
	cases := []struct {
		coreJar string
		want    string
	}{
		{"bungeecord.jar", "bungee"},
		{"BUNGEECORD.JAR", "bungee"},
		{"waterfall-1.20.jar", "waterfall"},
		{"Velocity.jar", "velocity"},
	}
	for _, c := range cases {
		got := deriveLogRuleKey("proxy", c.coreJar)
		require.Equal(t, c.want, got, "coreJar=%q 应推导为 %q", c.coreJar, c.want)
	}
}

// TestDeriveLogRuleKeyWaterfallBeforeBungee 验证 Waterfall 不被误判为 Bungee。
//
// Waterfall 是 BungeeCord 的分支，其 jar 内部可能含 "Bungee" 字样，
// 但文件名以 Waterfall 标识自身——匹配必须优先 Waterfall。
func TestDeriveLogRuleKeyWaterfallBeforeBungee(t *testing.T) {
	got := deriveLogRuleKey("proxy", "Waterfall-BungeeCord-based.jar")
	require.Equal(t, "waterfall", got, "Waterfall 的识别必须优先于 Bungee")
}
