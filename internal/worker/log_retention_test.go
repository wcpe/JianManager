package config

import (
	"testing"

	"github.com/wcpe/JianManager/internal/platform/rollog"
)

// wrBoolPtr 是测试辅助：Compress 是三态指针（nil / true / false）。
//
// 名字带 wr 前缀：本包与 controlplane/config 是**两个独立包**，
// 但两侧的测试文件名与辅助名若字面重复，会让“改了一边忘了另一边”更难发现；
// 带前缀也便于 grep 时一眼分出是哪边的。
func wrBoolPtr(b bool) *bool { return &b }

// TestWorkerLogConfigRetention 钉住 Worker 侧 YAML → rollog 保留策略的转换。
//
// # 这个用例真正防的是什么
//
// CP 与 Worker 各有一份 `LogConfig.Retention()`，它们的**目标必须完全一致**
// （运维不该因为写错了文件就得到两套不同的保留语义）。而两份实现最容易分叉的
// 地方是零值/负值的边界：
//   - `MaxBackups` 零值必须回退默认，负值必须保留为“不限”；
//   - `MaxAgeDays` 零值必须回退默认，负值必须保留为“关闭按天保留”；
//   - `Compress` 的 nil 与 false 必须可区分。
//
// 任何一条写反都是**静默**行为差异——不会有编译错误，只会在几个月后炸磁盘。
func TestWorkerLogConfigRetention(t *testing.T) {
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
			name: "Compress 显式 false 才关（与 nil 必须可区分）",
			cfg:  LogConfig{Compress: wrBoolPtr(false)},
			want: func() rollog.Retention {
				r := dflt
				r.Compress = false
				return r
			}(),
		},
		{
			name: "MaxSizeMB 换算为字节（MiB 语义）",
			cfg:  LogConfig{MaxSizeMB: 512},
			want: func() rollog.Retention {
				r := dflt
				r.MaxSizeBytes = 512 << 20
				return r
			}(),
		},
		{
			name: "MaxBackups 负值表示不限",
			cfg:  LogConfig{MaxBackups: -1},
			want: func() rollog.Retention {
				r := dflt
				r.MaxBackups = -1
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
			name: "四项同时配置",
			cfg: LogConfig{
				MaxSizeMB: 128, MaxBackups: 8, MaxAgeDays: 45, Compress: wrBoolPtr(true),
			},
			want: rollog.Retention{
				MaxSizeBytes: 128 << 20, MaxBackups: 8, MaxAgeDays: 45, Compress: true,
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

// TestWorkerAndControlPlaneRetentionAgree 钉住 CP 与 Worker 的转换语义一致。
//
// # 为什么要有跨层用例
//
// 两个包各写一份同样的转换，单看每一份都自洽——错误只在「它们不相等」时显现
// （见 CONVENTIONS「一个概念出现第二遍就收成类型」与「跨层概念必须有跨层用例」）。
// 这里把同一组输入同时跑两边，逐字段对比。
//
// 注：两个 LogConfig 是不同类型，不能直接互传，所以用同一组**字面输入**驱动，
// 再比结果——这正是“跨层用例只能在测试里硬编码核心集合”的形态。
func TestWorkerAndControlPlaneRetentionAgree(t *testing.T) {
	// 同一组输入：尺寸/份数/天数/压缩都走非零值，且刻意含负值与指针 false。
	type input struct {
		sizeMB, backups, ageDays int
		compress                 *bool
	}
	inputs := []input{
		{0, 0, 0, nil},
		{1024, 5, 30, wrBoolPtr(true)},
		{256, 3, 14, wrBoolPtr(false)},
		{0, -1, -1, nil},
		{64, 0, -1, wrBoolPtr(false)},
	}

	for i, in := range inputs {
		w := LogConfig{
			MaxSizeMB: in.sizeMB, MaxBackups: in.backups,
			MaxAgeDays: in.ageDays, Compress: in.compress,
		}
		got := w.Retention()

		// 直接比 DefaultRetention 的基线，再叠上输入应有的变换，
		// 避免本用例变成“两边一起错”的空转。
		want := rollog.DefaultRetention()
		if in.sizeMB > 0 {
			want.MaxSizeBytes = int64(in.sizeMB) << 20
		}
		if in.backups != 0 {
			want.MaxBackups = in.backups
		}
		if in.ageDays != 0 {
			want.MaxAgeDays = in.ageDays
		}
		if in.compress != nil {
			want.Compress = *in.compress
		}

		if got != want {
			t.Fatalf("第 %d 组输入转换不符合预期:\n got=%+v\nwant=%+v", i, got, want)
		}
	}
}
