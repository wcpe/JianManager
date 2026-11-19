package main

import (
	"strings"
	"testing"
)

// 每个已注册的子命令都必须有专属帮助正文。
//
// 这条守的是「新增子命令忘了写 help」：用户执行 `jm newcmd --help` 时会
// 静默走到真实逻辑而不是看到用法，在需要 help 的时刻反而最容易被误导。
func TestSubcommandUsageCovered(t *testing.T) {
	// 与 main() 的 switch 分支保持一致。
	subs := []string{"api", "auth", "config", "skill", "whoami", "list", "instance", "node"}
	for _, sub := range subs {
		if got := subcommandUsage(sub); strings.TrimSpace(got) == "" {
			t.Errorf("子命令 %q 缺少帮助正文", sub)
		}
	}
	if got := subcommandUsage("nosuchcmd"); got != "" {
		t.Errorf("未知子命令应返回空串，得到 %q", got)
	}
}

// 帮助正文必须包含该子命令的真实用法行，而不只是一句空泛说明。
func TestSubcommandUsageContent(t *testing.T) {
	cases := map[string][]string{
		"api":      {"METHOD", "PATH", "--data", "--query"},
		"auth":     {"login", "status", "logout"},
		"config":   {"set-url", "get-url", "list", "use", "remove"},
		"skill":    {"install", "--host", "--dir", "--from", "--dry-run"},
		"list":     {"nodes", "instances"},
		"instance": {"status", "metrics", "restart"},
		"node":     {"maintenance"},
	}
	for sub, wants := range cases {
		body := subcommandUsage(sub)
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s 的帮助缺少 %q\n---\n%s", sub, w, body)
			}
		}
	}
}

// wantsHelp 只认精确的 help 标记，不把值里的内容误判成求助。
func TestWantsHelp(t *testing.T) {
	yes := [][]string{{"--help"}, {"-h"}, {"help"}, {"GET", "/x", "--help"}}
	for _, args := range yes {
		if !wantsHelp(args) {
			t.Errorf("%v 应被判为请求帮助", args)
		}
	}
	no := [][]string{{}, {"GET", "/api/v1/x"}, {"--data", "payload.json"}, {"--helper"}}
	for _, args := range no {
		if wantsHelp(args) {
			t.Errorf("%v 不应被判为请求帮助", args)
		}
	}
}

// 只有「分发容器」型子命令裸执行才导向帮助。
//
// 反过来错的代价更大：`jm list` / `jm whoami` 裸执行有确定语义（列全部 / 查自己），
// 一旦被当成求助，脚本里的 `jm list` 会突然不干活。
func TestIsContainerCommand(t *testing.T) {
	containers := []string{"auth", "config", "skill"}
	for _, s := range containers {
		if !isContainerCommand(s) {
			t.Errorf("%q 应视为容器型子命令", s)
		}
	}
	actions := []string{"list", "whoami", "api", "instance", "node"}
	for _, s := range actions {
		if isContainerCommand(s) {
			t.Errorf("%q 有直接语义，不应视为容器型", s)
		}
	}
}
