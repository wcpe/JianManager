// Package trafficlimit 定义控制面 HTTP 入口限流的参数模型与默认值。
//
// 为什么单独成包：这些数值同时被 HTTP 中间件（middleware）与平台设置服务（service）
// 引用。若任一方向互相 import 就会形成环，也说不清「谁是权威」；放在 platform 层，
// 两边都只依赖一个不含副作用的纯数据包——默认值、边界与归一规则只有一份。
//
// 语义约定（务必如实对外表达）：
//   - RatePerSecond 为 0 且 Capacity 为 0 ⇒ 该分类**完全不限流**（超高并发部署可显式关闭）；
//   - 任一为 0 而另一个为正 ⇒ 非法配置（无从推导桶行为），由 Validate 拒绝；
//   - 分类预算与全局预算相互独立，每 IP 实际放行量 = min(全局, 各分类)。
package trafficlimit

import "fmt"

// Spec 单个限流桶的预算。
type Spec struct {
	// RatePerSecond 稳态补充速率（令牌/秒）。0（配合 Capacity=0）表示不限流。
	RatePerSecond int `json:"ratePerSecond"`
	// Capacity 桶容量（突发上限）。0（配合 RatePerSecond=0）表示不限流。
	Capacity int `json:"capacity"`
}

// Unlimited 报告该预算是否被显式关闭（不限流）。
func (s Spec) Unlimited() bool { return s.RatePerSecond == 0 && s.Capacity == 0 }

// Settings 控制面入口的全部限流预算。
//
// 分类的划分依据是「对后端的压力形态」，不是业务域：
//   - Read：只读请求（GET/HEAD/OPTIONS），一页首屏就有十余次扇出，是管理台的主要流量；
//   - Write：启停、改配置、快照等有副作用的请求，必须保持严格；
//   - LogQuery：日志检索/联邦查询，单次请求会跨 Worker 扇出并打 VL，属重后端负载；
//   - Global：跨分类的每 IP 总量上限，防止分类隔离把总量变成无界。
type Settings struct {
	Read     Spec `json:"read"`
	Write    Spec `json:"write"`
	LogQuery Spec `json:"logQuery"`
	Global   Spec `json:"global"`
}

// 上界：避免把配置写成事实上的「不限」却仍保留限流路径的开销，
// 也防止误输入（如 100000000）让令牌桶运算溢出。
const (
	MaxRatePerSecond = 1_000_000
	MaxCapacity      = 10_000_000
)

// Defaults 返回出厂默认预算。
//
// 取值依据（2026-11-12 实测，本机开发环境，同一 IP）：
//   - 实例页单标签首屏首秒峰值 16 次读请求；3 标签页 81 次请求中 30 次 429、
//     6 标签页 191 次中 131 次 429 —— 读写共用一个 20/s 总预算必然误伤正常多开；
//   - 故读 60/s、容量 80（覆盖单页峰值 × 常见并发标签数）；
//   - 写 10/s、容量 20（与改造前一致，不因拆分而放宽有副作用请求）；
//   - 日志查询 10/s、容量 20（重后端负载，维持原值）；
//   - 全局 90/s、容量 160（三类之和，仍是有界硬上限）。
func Defaults() Settings {
	return Settings{
		Read:     Spec{RatePerSecond: 60, Capacity: 80},
		Write:    Spec{RatePerSecond: 10, Capacity: 20},
		LogQuery: Spec{RatePerSecond: 10, Capacity: 20},
		Global:   Spec{RatePerSecond: 90, Capacity: 160},
	}
}

// Validate 校验单桶预算；字段名用于报错定位（设置页会直接把 message 展示给运维）。
func (s Spec) Validate(field string) error {
	if s.RatePerSecond < 0 || s.Capacity < 0 {
		return fmt.Errorf("%s：速率与容量不得为负数", field)
	}
	if s.Unlimited() {
		return nil
	}
	if s.RatePerSecond == 0 || s.Capacity == 0 {
		return fmt.Errorf("%s：速率与容量必须同时为 0（表示不限流），或同时为正数", field)
	}
	if s.RatePerSecond > MaxRatePerSecond {
		return fmt.Errorf("%s：速率不得超过 %d 次/秒", field, MaxRatePerSecond)
	}
	if s.Capacity > MaxCapacity {
		return fmt.Errorf("%s：容量不得超过 %d", field, MaxCapacity)
	}
	return nil
}

// Validate 校验整组预算。
func (s Settings) Validate() error {
	if err := s.Read.Validate("只读请求"); err != nil {
		return err
	}
	if err := s.Write.Validate("写操作"); err != nil {
		return err
	}
	if err := s.LogQuery.Validate("日志查询"); err != nil {
		return err
	}
	return s.Global.Validate("全局上限")
}

// 限流分类名。分类是「对后端的压力形态」而不是业务域：
//   - Read：只读请求（GET/HEAD/OPTIONS），管理台的主要流量；
//   - Write：有副作用的请求，必须保持严格；
//   - LogQuery：日志检索/联邦查询，单次请求会跨 Worker 扇出并打 VL。
const (
	ClassRead     = "read"
	ClassWrite    = "write"
	ClassLogQuery = "log_query"
)

// SpecForClass 返回某分类应使用的预算。
//
// 未知分类**保守归入写预算**：新增分类忘记登记时宁可更严（写 10/s），
// 也不能误落到只读档（60/s）把有副作用的请求放宽六倍。
func SpecForClass(s Settings, class string) Spec {
	switch class {
	case ClassRead:
		return s.Read
	case ClassLogQuery:
		return s.LogQuery
	default:
		return s.Write
	}
}

// Normalize 把零值/非法值收敛到默认预算。
//
// 与「写路径校验」的分工：Validate 面向设置页输入（拒绝而非静默修正），
// Normalize 面向基线读取（YAML/历史落库值可能是空串或超界），保证运行时永远拿到
// 一组可用的预算，不因某个键缺失就把入口限流退化成「全部拒绝」或「全部放行」。
func (s Settings) Normalize() Settings {
	d := Defaults()
	out := Settings{
		Read:     normalizeSpec(s.Read, d.Read),
		Write:    normalizeSpec(s.Write, d.Write),
		LogQuery: normalizeSpec(s.LogQuery, d.LogQuery),
	}
	// 全局桶例外：它必须始终有界，不接受 Unlimited。
	// 「关掉分类限流」是有意义的运维动作（超高并发专场），但「关掉总量闸」等于
	// 让单 IP 可以无限打 CP——那是被限流保护的对象本末倒置，故回落到默认。
	if s.Global.Unlimited() || s.Global.Validate("global") != nil {
		out.Global = d.Global
	} else {
		out.Global = s.Global
	}
	return out
}

func normalizeSpec(spec, def Spec) Spec {
	if spec.Unlimited() {
		return spec // 显式关闭：保留
	}
	if spec.Validate("spec") != nil {
		return def
	}
	return spec
}
