package config

import (
	"testing"

	"github.com/wcpe/JianManager/internal/platform/rollog"
)

// boolPtr 是测试辅助：因为 Compress 是三态指针（nil / true / false）。
func boolPtr(b bool) *bool { return &b }

// TestLogConfigRetention 钉住 YAML → rollog 保留策略的转换。
//
// # 为什么需要它
//
// 这段转换有个很容易写错的地方：`Compress` 是 `*bool`，它要区分三态——
// 「没写」（nil，应当默认为开）、「显式写 true」与「显式写 false」。
// 若退化成 `bool`，运维写 `compress: false` 与完全不写将无法区分，
// 而前者是「关压缩」、后者是「开压缩」——一个默认值方向写反就静默变质。
//
// 同时钉住“零值回退内置默认”：老配置文件完全没有这四个键，行为不能变。
func TestLogConfigRetention(t *testing.T) {
	dflt := rollog.DefaultRetention()

	cases := []struct {
		name string
		cfg  LogConfig
		want rollog.Retention
	}{
		{
			name: "全零值回退内置默认（老配置不受影响）",
			cfg:  LogConfig{},
			want: dflt,
		},
		{
			name: "Compress 未配置（nil）默认为开",
			cfg:  LogConfig{Compress: nil},
			want: dflt,
		},
		{
			name: "Compress 显式 true 为开",
			cfg:  LogConfig{Compress: boolPtr(true)},
			want: dflt,
		},
		{
			name: "Compress 显式 false 才关（与 nil 必须可区分）",
			cfg:  LogConfig{Compress: boolPtr(false)},
			want: func() rollog.Retention {
				r := dflt
				r.Compress = false
				return r
			}(),
		},
		{
			name: "MaxSizeMB 换算为字节（MiB 语义）",
			cfg:  LogConfig{MaxSizeMB: 256},
			want: func() rollog.Retention {
				r := dflt
				r.MaxSizeBytes = 256 << 20
				return r
			}(),
		},
		{
			name: "MaxBackups 正数覆盖",
			cfg:  LogConfig{MaxBackups: 12},
			want: func() rollog.Retention {
				r := dflt
				r.MaxBackups = 12
				return r
			}(),
		},
		{
			name: "MaxBackups 负值表示不限（不能被当成零值回退）",
			cfg:  LogConfig{MaxBackups: -1},
			want: func() rollog.Retention {
				r := dflt
				r.MaxBackups = -1
				return r
			}(),
		},
		{
			name: "MaxAgeDays 正数覆盖",
			cfg:  LogConfig{MaxAgeDays: 7},
			want: func() rollog.Retention {
				r := dflt
				r.MaxAgeDays = 7
				return r
			}(),
		},
		{
			name: "MaxAgeDays 负值表示关闭按天保留",
			cfg:  LogConfig{MaxAgeDays: -1},
			want: func() rollog.Retention {
				r := dflt
				r.MaxAgeDays = -1
				return r
			}(),
		},
		{
			name: "MaxSizeMB 负值不覆盖（只有正数才换算）",
			cfg:  LogConfig{MaxSizeMB: -5},
			want: dflt,
		},
		{
			name: "四项同时配置",
			cfg: LogConfig{
				MaxSizeMB: 64, MaxBackups: 3, MaxAgeDays: 14, Compress: boolPtr(false),
			},
			want: rollog.Retention{
				MaxSizeBytes: 64 << 20, MaxBackups: 3, MaxAgeDays: 14, Compress: false,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.Retention()
			if got != tc.want {
				t.Fatalf("Retention() 不匹配:\n got=%+v\nwant=%+v", got, tc.want)
			}
		})
	}
}

// TestLogConfigRetentionDefaultsMatchRollog 钉住“两边默认值同源”。
//
// 为什么不直接写死 1GiB/5/30：那样任何一边改默认值，这里都得跟着改，
// 而真正值得锁的是“它们不能各说各的”（见 CONVENTIONS「一个概念出现第二遍就收成类型」）。
func TestLogConfigRetentionDefaultsMatchRollog(t *testing.T) {
	got := LogConfig{}.Retention()
	want := rollog.DefaultRetention()
	if got != want {
		t.Fatalf("零值 LogConfig 必须完全等于 rollog.DefaultRetention()\n got=%+v\nwant=%+v", got, want)
	}
}
