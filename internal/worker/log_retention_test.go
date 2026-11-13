package config

import (
	"testing"

	cpconfig "github.com/wcpe/JianManager/internal/controlplane/config"
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
//
// # 这个用例曾经名不副实（2026-11-09 修正）
//
// 旧版本只构造了 **Worker 侧**的 `LogConfig` 并调它的 `Retention()`，
// `want` 是把转换规则手抄一遍再算——**CP 侧一行未测**。函数名却叫
// `WorkerAndControlPlaneRetentionAgree`。CP 的 `Retention()` 被改坏它照样绿。
//
// 现在真正 import 两侧（`controlplane/config` 不 import `internal/worker`，无环），
// 用同一个字面输入分别构造两个 **不同类型** 的 LogConfig，各自转换后逐字段比对。
func TestWorkerAndControlPlaneRetentionAgree(t *testing.T) {
	// 字面输入：尺寸/份数/天数覆盖零值、非零、负值，压缩覆盖 nil/true/false。
	// 刻意含零值与负值——那里才是两份实现最容易静默分叉的地方。
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
		// ① Worker 侧
		w := LogConfig{
			MaxSizeMB: in.sizeMB, MaxBackups: in.backups,
			MaxAgeDays: in.ageDays, Compress: in.compress,
		}
		gotWorker := w.Retention()

		// ② CP 侧（真正引用另一个包的类型，而不是复述它的规则）
		c := cpconfig.LogConfig{
			MaxSizeMB: in.sizeMB, MaxBackups: in.backups,
			MaxAgeDays: in.ageDays, Compress: in.compress,
		}
		gotCP := c.Retention()

		// ③ 跨层断言：两侧转换结果必须逐字段相等。
		// 这是本用例的**主要契约**——任一侧改了零值/负值语义，这里立刻红。
		if gotWorker != gotCP {
			t.Errorf("第 %d 组输入两侧不一致（运维写错文件不应该得到两套保留语义）:\n worker=%+v\n     cp=%+v",
				i, gotWorker, gotCP)
		}

		// ④ 再用**显式字面量**钉住期望值，而不是复述实现逻辑。
		// 两侧一起错（比如都把零值当 0 份）时，③ 会同时绿——只有这里能拦住。
		want := rollog.Retention{
			MaxSizeBytes: rollog.DefaultRetention().MaxSizeBytes,
			MaxBackups:   rollog.DefaultRetention().MaxBackups,
			MaxAgeDays:   rollog.DefaultRetention().MaxAgeDays,
			Compress:     true,
		}
		switch i {
		case 1: // {1024,5,30,true}：全等于默认值，结果为默认
		case 2: // {256,3,14,false}：全量覆盖 + 关压缩
			want.MaxSizeBytes = 256 << 20
			want.MaxBackups = 3
			want.MaxAgeDays = 14
			want.Compress = false
		case 3: // {0,-1,-1,nil}：负值是“不限/关闭”，不是零值回退
			want.MaxBackups = -1
			want.MaxAgeDays = -1
		case 4: // {64,0,-1,false}：尺寸覆盖、份数回退默认、天数关闭、压缩关
			want.MaxSizeBytes = 64 << 20
			want.MaxAgeDays = -1
			want.Compress = false
		}
		if gotWorker != want {
			t.Errorf("第 %d 组输入转换结果不符合显式期望（两侧一起错时靠这一条拦住）:\n got=%+v\nwant=%+v",
				i, gotWorker, want)
		}
	}
}
