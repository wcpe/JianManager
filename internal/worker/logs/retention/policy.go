package retention

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/platform/vlruntime"
)

// 推荐保留期（用户决策 D1）。这套值只在运维「启用保留」后生效；
// 策略本身默认只计算与暴露，不执行删除（见 doc.go 的「为什么默认不执行」）。
const (
	DefaultTTLDebug = 3 * 24 * time.Hour
	DefaultTTLInfo  = 7 * 24 * time.Hour
	DefaultTTLWarn  = 30 * 24 * time.Hour
	DefaultTTLError = 90 * 24 * time.Hour
)

// DefaultHotRetention 复用唯一默认热窗口；到期默认搬运而非裸删。
const DefaultHotRetention = vlruntime.DefaultHotRetention

// DefaultColdRetention 是冷层保留期的默认值（用户最终口径：**冷 730 天**）。
//
// 冷层**不按级别分档**：热层按级别是为了「快查最近的高价值级别 + 尽快腾出热层空间」，
// 而冷层的目的只有一个——**长期留存**。对冷层再做一套级别档位，
// 只会让「同一条日志在两层的保留期不一致」变成常态，而那种不一致没有运维价值。
const DefaultColdRetention = vlruntime.DefaultColdRetention

// 执行器默认值。
const (
	// DefaultSweepInterval 保留扫描间隔。删除是幂等的（同一条日志被删两次无副作用），
	// 所以间隔只影响「过期数据多留多久」，不影响正确性；1 小时足够把额外占用压到 1/24。
	DefaultSweepInterval = time.Hour
	// MinTTL 允许的最短保留期。再短就不是「保留策略」而是「删除策略」了——
	// 按保留期删除日志的合法诉求是省空间，不是让日志消失。
	MinTTL = time.Hour
)

// LevelKey 是保留策略里的级别键；由白名单固定，配置里的未知级别直接拒绝。
//
// 为什么不用自由字符串：级别键会被拼进删除过滤器，而删除不可逆。
// 白名单把「配置能表达什么」限死在「对哪一级设多长保留期」这一个问题上。
var levelKeys = []string{"TRACE", "DEBUG", "INFO", "WARN", "ERROR"}

// LevelKeyOf 把级别名归一为保留策略的级别键。
//
// 归一口径与采集侧一致（TRACE/DEBUG/INFO/WARN/ERROR），未识别返回空串。
// 注意 SEVERE/FATAL 在归一化阶段就已折成 ERROR（normalize.CanonicalLevel），
// 故这里不需要也不应再引入别名——两处别名表迟早会不一致。
func LevelKeyOf(level string) string {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "TRACE":
		return "TRACE"
	case "DEBUG":
		return "DEBUG"
	case "INFO":
		return "INFO"
	case "WARN", "WARNING":
		return "WARN"
	case "ERROR", "SEVERE", "FATAL":
		return "ERROR"
	default:
		return ""
	}
}

// KnownLevelKeys 返回级别白名单副本（供配置层与文档生成）。
func KnownLevelKeys() []string {
	out := make([]string, len(levelKeys))
	copy(out, levelKeys)
	return out
}

// sourceIDRe 是允许出现在删除过滤器里的源标识字符集。
//
// 严格的**字符集白名单**而不是转义：源标识由平台自己生成（inst:<数字ID> / node:<数字ID>），
// 形态完全可枚举。用转义去容纳任意字符，等于让攻击面取决于「转义函数今天写对了吗」；
// 白名单则把「不可能出现在源标识里的字符」直接挡在门外。
var sourceIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// ValidSourceID 报告源标识是否可以安全地用于删除过滤器。
func ValidSourceID(id string) bool { return sourceIDRe.MatchString(id) }

// SourceOverride 是某个来源的保留期覆盖。
type SourceOverride struct {
	// Match 源标识；以 `*` 结尾表示前缀匹配（如 `inst:` 覆盖全部实例源）。
	Match string `mapstructure:"match"`
	// ByLevel 级别 → 保留期；未列出的级别沿用全局默认。
	// 显式写 0 表示**该级别永久保留**（覆盖全局默认），与「未列出」语义不同：
	// 前者是「我确认这一级不能删」，后者是「我没说」。
	ByLevel map[string]time.Duration `mapstructure:"by_level"`
}

// Sweep 是保留策略的执行器配置。
type Sweep struct {
	// VLSweep 是否真的执行 VL 侧删除。
	//
	// 默认 false：删除不可逆，而本包的默认姿态是「先算出来、看得见，再让运维决定动手」。
	// 打开它还需要受管 VL 进程带 `-delete.enable`（VL 自身默认关），否则调用会被拒（400）。
	//
	// **本版本的接线状态（RET-2）**：`NewSweeper` 在生产没有构造点，`-delete.enable` 也没有
	// 注入点，因此打开本项的实际效果只是让计划进入「只计划」档（措辞见 DescribeDeleteExecutor），
	// 不会产生任何删除调用。这是 fail-closed 方向（不会误删），但**不得**按「已生效」读它。
	VLSweep bool `mapstructure:"vl_sweep"`
	// Interval 扫描间隔；<=0 取 DefaultSweepInterval。
	Interval time.Duration `mapstructure:"interval"`
	// Timeout 单次删除请求超时；<=0 取 DefaultSweepTimeout。
	Timeout time.Duration `mapstructure:"timeout"`
	// VLDeleteEnabled 报告受管 VL 进程是否带 `-delete.enable`（VL 侧那一道闸）。
	//
	// 为什么这一闸要放进本包：它是删除三闸里的第三道，而三闸的判定必须只有一个来源
	// （见 Policy.DeleteGatesOpen）。若把「VL 是否允许删除」留在装配侧自己记着，
	// 迟早会出现「策略说三闸全开、VL 其实没开」的分裂读数。
	//
	// 本字段只**声明事实**，不负责打开 VL 的参数（那是 vlsup 的事）。
	VLDeleteEnabled bool `mapstructure:"-"`
}

// DefaultSweepTimeout 单次删除请求超时。
const DefaultSweepTimeout = 30 * time.Second

// Policy 是保留策略（纯配置）。
type Policy struct {
	// Enabled 是否启用保留策略（决定「保留多久」）。
	// 默认 false = 零行为变化：不计算、不暴露、不删除。
	Enabled bool `mapstructure:"enabled"`
	// ByLevel 级别 → 保留期。缺省时按 D1 推荐值补齐（见 DefaultPolicy）。
	ByLevel map[string]time.Duration `mapstructure:"by_level"`
	// Sources 来源级覆盖。
	Sources []SourceOverride `mapstructure:"sources"`
	Sweep   Sweep
	// Discard 是否允许**直接删除**到期的数据（裸删）。
	//
	// 默认 false，且这是**硬规则**而不是可调偏好：保留期配错在现场只表现为
	// 「日志不见了」，不会有任何报错；把默认动作定成「搬运」（数据仍在、仍可查）
	// 而不是「删除」，使所有配置错误的最坏后果从「数据永久丢失」降级为「盘没省下来」。
	// 只有运维显式选择「知情放弃」时才会走到删除路径。
	Discard bool `mapstructure:"discard"`
	// HotRetention 热层保留窗口；<=0 时按级别 TTL 取最长档推导（见 MaxHotRetention）。
	HotRetention time.Duration `mapstructure:"hot_retention"`
	// Trigger 搬运触发口径：年龄 + 磁盘水位**取先到**（用户决策 D3）。
	Trigger Trigger `mapstructure:"trigger"`
	// ColdRetention 冷层保留期（所有级别统一）。<=0 取 DefaultColdRetention。
	//
	// 本字段承担**声明与校验**：真正的冷层保留由受管 COLD 实例的 `-retentionPeriod`
	// 执行（见 vlsup 的按 namespace retention）。放在策略里是为了让「热多久 / 冷多久」
	// 在同一处可读、可校验，而不是散在两个配置段里各说各话。
	ColdRetention time.Duration `mapstructure:"cold_retention"`
	// ArchiveCopy 是硬规则的**次选**路径：搬不了冷层时导出为独立副本。
	//
	// 默认关闭（零行为变化）：它把数据写到另一个目的地，那是运维必须明确授权的事——
	// 「归档根目录写在哪、写多少、谁来清理」都不是平台能替运维默认的。
	ArchiveCopy ArchiveCopyPolicy `mapstructure:"archive_copy"`
	// ArchiveObjectRetention 是**归档目的地自身**的保留期（桶内生命周期）。
	//
	// 默认关闭，且与 ArchiveCopy 分开：导出的开关回答「要不要写副本」，
	// 本项回答「副本留多久」。合成一个开关会让「想清归档、但不想再导出新的」变成不可能，
	// 也会让「打开导出」顺带获得删除归档的权限——而删除比写入危险得多。
	ArchiveObjectRetention ArchiveObjectRetentionPolicy `mapstructure:"archive_object_retention"`
}

// ArchiveCopyPolicy 是「导出归档」这条次选路径的开关。
//
// 为什么只有一个开关：目的地/压缩/manifest 布局都属于实现细节，由装配侧（谁能提供
// 归档 Provider、根目录在哪）决定；策略层只该回答「允不允许把到期数据导出」。
type ArchiveCopyPolicy struct {
	// Enabled 是否允许在搬不了冷层时导出为独立副本（默认 false）。
	//
	// 打开它**不等于**允许删热层：回收还要过删除三闸（见 Policy.DeleteGatesOpen）。
	Enabled bool `mapstructure:"enabled"`
}

// DeleteGatesOpen 报告**删除三闸**是否同时成立，第二返回值是未成立的原因。
//
// 三闸（护栏要求默认全关，且三者**相互独立**）：
//  1. 意图闸 `log_retention.discard`：运维显式声明「我知道这会永久删掉日志」；
//  2. 执行闸 `log_retention.sweep.vl_sweep`：运维显式打开删除执行路径；
//  3. VL 侧闸 `log_vl.delete_enabled`：受管 VL 进程真的带 `-delete.enable`
//     （VL 自身默认关，没这条参数时 /delete/* 一律 400）。
//
// 为什么回收热层也要过这三闸而不能另造一套：`ActionArchiveCopy` 校验通过后的那一步
// 与 `ActionDiscard` 删的是**同一样东西**（一个日分区的热层数据），后果同样是不可逆的。
// 换一条路径就换一套闸门，等于把「三闸」变成「看情况几闸」——而闸门数量正是它的安全论证。
//
// 注意本方法**不做任何副作用判断**（不检查出口是否装配）：那是调用方的第二层判据，
// 两者都要满足才允许删。这里只回答「运维授权是否齐了」这一个问题。
func (p Policy) DeleteGatesOpen() (bool, string) {
	n := p.Normalize()
	if !n.Discard {
		return false, "意图闸 log_retention.discard 未打开"
	}
	if !n.Sweep.VLSweep {
		return false, "执行闸 log_retention.sweep.vl_sweep 未打开"
	}
	if !n.Sweep.VLDeleteEnabled {
		return false, "VL 侧闸 log_vl.delete_enabled 未打开（受管 VL 缺 -delete.enable）"
	}
	return true, ""
}

// DefaultPolicy 返回 D1 推荐的保留策略。
//
// 注意 Enabled 仍为 false：推荐值填好是为了让运维启用时不必自己发明数字，
// 而不是替运维做「现在就开始删」的决定。执行闸（Sweep.VLSweep）同样默认关。
func DefaultPolicy() Policy {
	return Policy{
		Enabled: false,
		ByLevel: map[string]time.Duration{
			"TRACE": DefaultTTLDebug, // TRACE 与 DEBUG 同档：两者都是「排查用的细节」
			"DEBUG": DefaultTTLDebug,
			"INFO":  DefaultTTLInfo,
			"WARN":  DefaultTTLWarn,
			"ERROR": DefaultTTLError,
		},
		Sweep:         Sweep{Interval: DefaultSweepInterval, Timeout: DefaultSweepTimeout},
		Trigger:       Trigger{SweepAtHour: DefaultSweepAtHour},
		HotRetention:  DefaultHotRetention,
		ColdRetention: DefaultColdRetention,
	}
}

// Normalize 补齐缺省值，返回补齐后的副本。
//
// 缺省口径：未列出的级别按 D1 推荐值补齐（而不是「永久保留」）——
// 保留策略一旦启用，运维的意图就是「按推荐档位管起来」；漏配一级就永久保留，
// 会让 78GB 里最占地方的那一级（通常是 DEBUG）静默逃过策略。
func (p Policy) Normalize() Policy {
	out := p
	out.ByLevel = map[string]time.Duration{}
	for _, key := range levelKeys {
		out.ByLevel[key] = defaultTTLFor(key)
	}
	for k, v := range p.ByLevel {
		if key := LevelKeyOf(k); key != "" {
			out.ByLevel[key] = v
		}
	}
	if out.Sweep.Interval <= 0 {
		out.Sweep.Interval = DefaultSweepInterval
	}
	if out.Sweep.Timeout <= 0 {
		out.Sweep.Timeout = DefaultSweepTimeout
	}
	if out.HotRetention <= 0 {
		out.HotRetention = DefaultHotRetention
	}
	if out.ColdRetention <= 0 {
		out.ColdRetention = DefaultColdRetention
	}
	// 归档对象保留期**刻意不在这里补任何默认值**（连 Normalize 都不调）：
	// 默认必须是「无值 = 不启用」。若在这里补一个 730d，那么只要运维打开 enabled，
	// 就会在两年后无条件开始删归档——而「留多久」是合规/成本口径，必须由运维说出口。
	// 小时数的归一化在 ArchiveObjectRetentionPolicy.Normalize 里，由驱动器构造时调用
	// （见 NewArchivePurgeDriver），与「保留期要不要有默认」是两件独立的事。
	return out
}

func defaultTTLFor(key string) time.Duration {
	switch key {
	case "TRACE", "DEBUG":
		return DefaultTTLDebug
	case "INFO":
		return DefaultTTLInfo
	case "WARN":
		return DefaultTTLWarn
	default:
		return DefaultTTLError
	}
}

// Validate 校验策略。非法值启动即拒（与 timezone/charset 同一取舍）：
// 保留期配错的后果是不可逆的数据丢失，现场只会表现为「日志不见了」。
func (p Policy) Validate() error {
	if !p.Enabled {
		return nil
	}
	for _, field := range []struct {
		name  string
		value time.Duration
	}{
		{"hot_retention", p.HotRetention}, {"cold_retention", p.ColdRetention},
		{"sweep.interval", p.Sweep.Interval}, {"sweep.timeout", p.Sweep.Timeout},
		{"trigger.min_age", p.Trigger.MinAge}, {"trigger.interval", p.Trigger.Interval},
	} {
		if field.value < 0 {
			return fmt.Errorf("retention: %s 不能为负，得到 %s", field.name, field.value)
		}
	}
	if math.IsNaN(p.Trigger.DiskPercent) || math.IsInf(p.Trigger.DiskPercent, 0) || p.Trigger.DiskPercent < 0 || p.Trigger.DiskPercent > 100 {
		return fmt.Errorf("retention: trigger.disk_percent 必须在 0..100，得到 %v", p.Trigger.DiskPercent)
	}
	// 每日触发时刻：0..23 是本地整点，-1 是「回退间隔 ticker」的兼容档；其余一律拒绝。
	// 为什么启动即拒而不是归一：时刻配错的后果是「搬运永远在业务高峰跑」（写错 13 与 3 只差一个数字），
	// 现场只会表现为「盘上涨得比预期快」，从日志里几乎看不出来。归一化会把这种配置事故静默吃掉。
	if p.Trigger.SweepAtHour < SweepAtHourIntervalTicker || p.Trigger.SweepAtHour > 23 {
		return fmt.Errorf("retention: trigger.sweep_at_hour 必须在 %d..23（%d = 回退间隔 ticker，0 = 本地午夜），得到 %d",
			SweepAtHourIntervalTicker, SweepAtHourIntervalTicker, p.Trigger.SweepAtHour)
	}
	// 归档对象保留期：负数一律拒（与其它保留期同口径）。
	// 「开了开关却没有保留期」由 config 层拒——那里的措辞要点名具体键
	// `log_retention.archive_object_retention.retention`，运维才能直接定位。
	if p.ArchiveObjectRetention.Retention < 0 {
		return fmt.Errorf("retention: archive_object_retention.retention 不能为负，得到 %s",
			p.ArchiveObjectRetention.Retention)
	}
	for k, v := range p.ByLevel {
		if LevelKeyOf(k) == "" {
			return fmt.Errorf("retention: 未知级别键 %q（支持 %s）", k, strings.Join(levelKeys, "/"))
		}
		if v < 0 {
			return fmt.Errorf("retention: 级别 %s 的保留期不能为负，得到 %s", k, v)
		}
		if v > 0 && v < MinTTL {
			return fmt.Errorf("retention: 级别 %s 的保留期 %s 过短（最小 %s）——短于此就不是保留策略而是删除策略",
				k, v, MinTTL)
		}
	}
	for i, o := range p.Sources {
		if strings.TrimSpace(o.Match) == "" {
			return fmt.Errorf("retention: 第 %d 个来源覆盖的 match 不能为空", i)
		}
		prefix, isPrefix := strings.CutSuffix(o.Match, "*")
		if !ValidSourceID(prefix) {
			return fmt.Errorf("retention: 第 %d 个来源覆盖的 match=%q 含非法字符（源标识只允许字母数字与 _.:-）", i, o.Match)
		}
		if isPrefix && prefix == "" {
			return fmt.Errorf("retention: 第 %d 个来源覆盖的 match=%q 会匹配全部来源；请逐条列出而不是用通配", i, o.Match)
		}
		for k, v := range o.ByLevel {
			if LevelKeyOf(k) == "" {
				return fmt.Errorf("retention: 来源 %s 的未知级别键 %q", o.Match, k)
			}
			if v < 0 {
				return fmt.Errorf("retention: 来源 %s 的级别 %s 保留期不能为负", o.Match, k)
			}
			if v > 0 && v < MinTTL {
				return fmt.Errorf("retention: 来源 %s 的级别 %s 保留期 %s 过短（最小 %s）", o.Match, k, v, MinTTL)
			}
		}
	}
	return nil
}

// EffectiveTTL 返回某来源某级别的实际保留期。
//
// 解析优先级：来源覆盖（最具体的匹配优先）> 全局级别默认。
// 返回值 0 表示**永久保留**（不删）。
//
// 匹配规则的取舍：只有一个来源覆盖命中时用它；多个命中时用**最长前缀**那个
// （`inst:147` 比 `inst:` 更具体），同样长时用配置里靠前的。这样「全部实例 7 天、
// 但 147 这台 30 天」可以写成一条通配 + 一条例外，而不必逐台列举。
func (p Policy) EffectiveTTL(sourceID, level string) time.Duration {
	key := LevelKeyOf(level)
	if key == "" {
		// 级别无法归一时按最保守处理：**永久保留**。
		// 不能回退到「用全局默认」——那会让一堆无法归类的日志被按最短档删掉。
		return 0
	}
	n := p.Normalize()
	if v, ok := p.resolveOverride(sourceID, key); ok {
		return v
	}
	return n.ByLevel[key]
}

// resolveOverride 返回命中的来源覆盖值。第二个返回值报告 **该来源是否就这一级**
// 写了保留期：写了就返回它（0 也是有效值 = 永久保留），没写就是「沿用全局默认」。
//
// 具体度排序：精确匹配永远比前缀匹配更具体；同类里更长的前缀更具体。
// 于是「全部实例 7 天、但 147 这台 30 天」只需一条通配 + 一条例外，不必逐台列举。
func (p Policy) resolveOverride(sourceID, levelKey string) (time.Duration, bool) {
	if sourceID == "" {
		return 0, false
	}
	best := -1
	bestSpec := -1
	for i, o := range p.Sources {
		prefix, isPrefix := strings.CutSuffix(o.Match, "*")
		if isPrefix {
			if !strings.HasPrefix(sourceID, prefix) {
				continue
			}
		} else if sourceID != prefix {
			continue
		}
		spec := len(prefix)
		if !isPrefix {
			// 精确匹配的「具体度」必须压过任意前缀匹配，用一个远大于任何合法
			// 源标识长度（128）的偏置表达，避免依赖 len 的巧合。
			spec += 1 << 20
		}
		if spec > bestSpec {
			best, bestSpec = i, spec
		}
	}
	if best < 0 {
		return 0, false
	}
	v, ok := p.Sources[best].ByLevel[levelKey]
	return v, ok
}

// ParseTTL 解析保留期字符串。
//
// 为什么不直接用 time.ParseDuration：Go 的 ParseDuration **不认 `d` 与 `w`**，
// 而保留期天然按天写（用户决策 D1 就是「debug 3d / info 7d / warn 30d / error 90d」）。
// 若沿用原生语法，运维写 `3d` 会在启动时被拒，必须换算成 `72h` 才通过——
// 配置面与直觉不符是配置事故的温床（写错的人不会发现，发现的时候已经在删数据了）。
//
// 支持：
//   - 空串 / "0" → 0，表示**永久保留**；
//   - 原生语法（30m / 24h / 1h30m）；
//   - 追加 d（天）与 w（周）后缀，允许小数（如 0.5d）；
//   - 负数一律拒绝：保留期没有「负」的语义，能写出来就说明配置有问题。
func ParseTTL(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d < 0 {
			return 0, fmt.Errorf("retention: 保留期不能为负：%q", raw)
		}
		return d, nil
	}
	if last := s[len(s)-1]; last == 'd' || last == 'w' {
		base := 24 * time.Hour
		if last == 'w' {
			base = 7 * 24 * time.Hour
		}
		num := strings.TrimSpace(s[:len(s)-1])
		if f, err := strconv.ParseFloat(num, 64); err == nil {
			if f < 0 {
				return 0, fmt.Errorf("retention: 保留期不能为负：%q", raw)
			}
			return time.Duration(f * float64(base)), nil
		}
	}
	return 0, fmt.Errorf("retention: 无法解析保留期 %q（支持 30m / 24h / 1h30m，以及 d=天、w=周，如 3d、2w）", raw)
}

// DeleteExecutorWired 报告**本版本**是否把删除执行器接到了生产（启动日志与观测面用）。
//
// 为什么需要这个常量（RET-2，2026-10-11 复核）：三闸里的执行闸 `sweep.vl_sweep` 只有
// `Sweeper` 消费，而 `NewSweeper` 在生产**没有构造点**（全仓只有测试构造），
// `-delete.enable` 也没有注入点（`vlsup.BuildArgs` 是硬编码参数表，没有扩展位）。
// 于是「把开关打开」在生产既不删、也没有干跑计划出口，而 `Describe()` 原先会打印
// `mode=vl-sweep` 并被写进启动日志——读日志的人会以为删除已生效。
//
// 结论（本版本取「最小正确形态」，不补接线）：把措辞改成 `planned-only`，
// 并把「未接线」显式写进 `Describe()` 的返回串（它由 main.go 的启动日志打印）。
// 接线（构造 Sweeper + 给受管 VL 注入 -delete.enable）后必须把本常量改成 true 并同步措辞。
const DeleteExecutorWired = false

// DescribeDeleteExecutor 返回删除执行器接线状态的一句话说明（启动日志、观测面与文档用）。
func DescribeDeleteExecutor(p Policy) string {
	if !p.Normalize().Sweep.VLSweep {
		return "删除执行器未启用（sweep.vl_sweep=false）：只算计划、零删除调用"
	}
	if !DeleteExecutorWired {
		return "删除执行器未接线：sweep.vl_sweep=true 只让计划进入「只计划」档，" +
			"本版本生产Sweeper不会产生删除调用（开启VL第三闸不能替代执行器装配）"
	}
	return "删除执行器已接线（仍需受管 VL 带 -delete.enable，否则下发被拒）"
}

// Describe 返回策略摘要（启动日志与观测面用）。
//
// 措辞约束（RET-2）：`vl_sweep=true` **不得**被描述成「已生效」——本版本删除执行器未接线，
// 那一档的实际效果只是「计划可审计」。
func (p Policy) Describe() string {
	if !p.Enabled {
		return "disabled；平台搬运/删除关闭不影响VL有限原生TTL独立淘汰，不是永久保留保证"
	}
	n := p.Normalize()
	keys := make([]string, 0, len(n.ByLevel))
	for k := range n.ByLevel {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)+2)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, humanTTL(n.ByLevel[k])))
	}
	mode := "dry-run"
	if n.Sweep.VLSweep {
		// 措辞与 DescribeDeleteExecutor 一致：本版本没有删除执行器的生产构造点。
		mode = "planned-only"
	}
	return fmt.Sprintf("%s overrides=%d (%s)；%s；有限原生TTL仍独立淘汰，平台保留原物不构成无限故障保证",
		mode, len(p.Sources), strings.Join(parts, " "), DescribeDeleteExecutor(n))
}

func humanTTL(d time.Duration) string {
	if d <= 0 {
		return "keep"
	}
	return d.String()
}
