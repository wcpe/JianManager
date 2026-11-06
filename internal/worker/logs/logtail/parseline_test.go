package logtail

import (
	"testing"
)

// TestParseLogLine 锤死日志行的时间/级别提取，包括**两种并存的包前缀形态**。
//
// # 为什么必须覆盖 BungeeCord 形态
//
// 2026-11-06 真机实测：生产 19 个实例里既有 Paper/MC 原生 `[HH:MM:SS] [thread/LEVEL]:`，
// 也有 BungeeCord 的 `HH:MM:SS [LEVEL] `（`/home/wxys233/servers/*/proxy.log.0`，
// 共 88234 行）。只按第一形态解析时，后者**一行都识别不出**：
//   - `level` 全空 ⇒ `level:="WARN"` 过滤不到任何 proxy 行，按级别保留策略永不生效；
//   - `eventTime` 全部退回采集时刻 ⇒ 补录历史时整段时间轴错位。
//
// 这条用例就是那个回归的抡门：删掉 bungeeTimeLevelRe 立即转红。
func TestParseLogLine(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantTime  string
		wantLevel string
	}{
		{
			name:      "MC 标准 INFO 行",
			line:      `[18:28:02] [Server thread/INFO]: Environment: authHost=...`,
			wantTime:  "18:28:02",
			wantLevel: "INFO",
		},
		{
			name:      "MC ERROR 行",
			line:      `[19:07:39] [Server thread/ERROR]: Could not pass event`,
			wantTime:  "19:07:39",
			wantLevel: "ERROR",
		},
		{
			name:      "MC 带毫秒",
			line:      `[18:28:02,123] [Server thread/WARN]: slow`,
			wantTime:  "18:28:02",
			wantLevel: "WARN",
		},
		{
			name:      "MC 线程名带空格",
			line:      `[01:19:43] [ServerMain/INFO]: Environment: name='PROD'`,
			wantTime:  "01:19:43",
			wantLevel: "INFO",
		},
		{
			// BungeeCord 真实形态：时刻**没有**方括号，级别带。
			name:      "BungeeCord INFO 行",
			line:      `18:12:59 [INFO] Using mbed TLS based native cipher.`,
			wantTime:  "18:12:59",
			wantLevel: "INFO",
		},
		{
			// 级别别名必须归一：保留策略白名单是 WARN/ERROR，不是 WARNING/SEVERE。
			name:      "BungeeCord WARNING 归一为 WARN",
			line:      `12:00:00 [WARNING] Could not save`,
			wantTime:  "12:00:00",
			wantLevel: "WARN",
		},
		{
			name:      "BungeeCord SEVERE 归一为 ERROR",
			line:      `12:00:01 [SEVERE] Exception in thread`,
			wantTime:  "12:00:01",
			wantLevel: "ERROR",
		},
		{
			name:      "同一个方括号内时刻与级别",
			line:      `[12:00:02 INFO]: done`,
			wantTime:  "12:00:02",
			wantLevel: "INFO",
		},
		{
			name:      "只有时刻无级别",
			line:      `[12:00:03] plain body`,
			wantTime:  "12:00:03",
			wantLevel: "",
		},
		{
			name:      "裸时刻无级别",
			line:      `12:00:04 plain body`,
			wantTime:  "12:00:04",
			wantLevel: "",
		},
		{
			// 结构化日志：时间形态千差万别，只取级别（时间由调用方兜底）。
			name:      "slog 结构化行",
			line:      `ts=1234 level=ERROR msg="boom"`,
			wantTime:  "",
			wantLevel: "ERROR",
		},
		{
			// 堆栈行：没有时间也没有级别，两者都应留空（不得瞎猜）。
			name:      "堆栈行（无时间无级别）",
			line:      "\tat AllinMailBox-1.4.0.jar//top.wcpe.TabooLib.run(TabooLib.java:119)",
			wantTime:  "",
			wantLevel: "",
		},
		{
			name:      "非日志格式纯文本",
			line:      `plain text without brackets`,
			wantTime:  "",
			wantLevel: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTime, gotLevel := parseLogLine(tc.line)
			if gotTime != tc.wantTime {
				t.Errorf("时间：期望 %q 实际 %q", tc.wantTime, gotTime)
			}
			if gotLevel != tc.wantLevel {
				t.Errorf("级别：期望 %q 实际 %q", tc.wantLevel, gotLevel)
			}
		})
	}
}

// TestParseLogLineLevelIsRetentionWhitelisted 守住「写入的级别能被保留策略命中」。
//
// 为什么单列一条：`parseLogLine` 返回的级别会被直接写进 VL 的 `level` 字段，
// 而保留策略按 `level:="<白名单键>"` 生成删除过滤器。两边只要对不上，
// 那种级别的日志就**永远不会被清理**（会静默堆积，直到磁盘告警才被发现）。
// 这里用保留侧的白名单反向验证解析结果，而不是再抄一份别名表。
func TestParseLogLineLevelIsRetentionWhitelisted(t *testing.T) {
	allowed := map[string]bool{"TRACE": true, "DEBUG": true, "INFO": true, "WARN": true, "ERROR": true}
	lines := []string{
		`12:00:00 [TRACE] t`, `12:00:00 [DEBUG] d`, `12:00:00 [INFO] i`,
		`12:00:00 [WARN] w`, `12:00:00 [WARNING] w`, `12:00:00 [ERROR] e`,
		`12:00:00 [SEVERE] s`, `12:00:00 [FATAL] f`,
		`[12:00:00] [Server thread/INFO]: i`,
		`[12:00:00] [Server thread/WARN]: w`,
		`[12:00:00] [Server thread/ERROR]: e`,
	}
	for _, line := range lines {
		_, level := parseLogLine(line)
		if level == "" {
			t.Errorf("%q 解析不出级别（会让该行绕过按级别保留策略）", line)
			continue
		}
		if !allowed[level] {
			t.Errorf("%q 解析出 %q，不在保留策略白名单 %v 内（该级别永远不会被清理）",
				line, level, []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR"})
		}
	}
}
