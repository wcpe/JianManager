package vlsup

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Namespace 标识受管 VL 实例类别（FR-476：Worker supervisor 分别管理 HOT/COLD/Rehydrate）。
type Namespace string

const (
	NamespaceHot       Namespace = "hot"
	NamespaceCold      Namespace = "cold"
	NamespaceRehydrate Namespace = "rehydrate"
)

// ValidNamespace 报告 ns 是否为受支持的命名空间。
func ValidNamespace(ns Namespace) bool {
	switch ns {
	case NamespaceHot, NamespaceCold, NamespaceRehydrate:
		return true
	default:
		return false
	}
}

// 默认监听端口（与 FR-473 C 组 supervisor-equivalent smoke 一致，可在 Options 覆盖）。
const (
	DefaultPortHot       = 19441
	DefaultPortCold      = 19442
	DefaultPortRehydrate = 19443
)

// DefaultHotCacheBytes 是 HOT 实例 -memory.allowedBytes 默认模板（512MiB）。
// 依据 FR-473 §6.5/§7 与 FR-476 §3.1：512MiB 只代表 HOT cache 模板，不等于 RSS 上限，
// 也不是 Worker 日志总预算。
const DefaultHotCacheBytes int64 = 512 * 1024 * 1024

// DefaultRetentionPeriod 是受管 VL 的 runtime retention 默认值。
// 产品 online_retention / move_after_age 与 runtime retention 分离映射（契约 §7）。
//
// **为什么是 120d 而不是 30d**（2026-10-21 架构复审）：
// 保留策略的热层窗口默认是 `retention.DefaultHotRetention` = 90d（数据在热层待 90 天后
// 搬到冷层）。而 VL 的 retention 到期是**直接删**，绕过平台侧「删前归档」。
// 若 VL retention（30d）短于热窗口（90d），数据在「该搬走」之前就被删掉；
// 更糟的是 `validateRuntimeRetention` 会因此拒绝启动 —— 即用默认值开启保留策略
// 会让 Worker 起不来。
//
// 取 120d = 90d 热窗口 + 30d 搬运余量：搬运驱动器有充裕时间把分区拷到冷层并校验，
// 不会出现「还没搬完就被删」的竞态。两个默认值因此自洽。
//
// 注意：这是**默认值**。若运维显式配置 `log_vl.retention_period`，仍以配置为准，
// 并由 `validateRuntimeRetention` 校验与热窗口的余量关系。
const DefaultRetentionPeriod = "120d"

// DefaultAuthUsername 默认本地鉴权用户名。
const DefaultAuthUsername = "jm"

// LocalhostBind 仅允许的监听主机（localhost bind only）。
const LocalhostBind = "127.0.0.1"

// InstanceConfig 描述单个 VL 实例的启动参数。
type InstanceConfig struct {
	Namespace          Namespace
	Port               int
	StorageDataPath    string
	RetentionPeriod    string
	MemoryAllowedBytes int64 // 0：HOT 使用 DefaultHotCacheBytes；其他 namespace 省略该 flag
	AuthUsername       string
	AuthPassword       string
	// EnableDelete 打开 VL 的 -delete.enable，使 /delete/* API 可用。
	//
	// 为什么需要它（2026-10-21 架构复审）：`projection_generation` 白名单只让旧代次
	// 「看不见」，**不会让旧行不存在**；而 VL 只追加。在缺删除能力的前提下，任何
	// 「整窗重写 + 换代次」的设计都必然线性放大体积（生产实测单日 75×、880MB）。
	// 打开后可由孤儿代次 GC 按 `projection_generation NOT IN (已发布白名单)` 回收。
	//
	// 默认关闭（零值）：删除是不可逆操作，必须由运维显式承担；关闭时行为与改动前逐字一致。
	EnableDelete bool
}

// DefaultPort 返回 namespace 的默认监听端口。
func DefaultPort(ns Namespace) int {
	switch ns {
	case NamespaceCold:
		return DefaultPortCold
	case NamespaceRehydrate:
		return DefaultPortRehydrate
	default:
		return DefaultPortHot
	}
}

// ListenAddr 返回强制 localhost 的监听地址。
func ListenAddr(port int) string {
	return net.JoinHostPort(LocalhostBind, strconv.Itoa(port))
}

// EffectiveCacheBytes 返回将写入 -memory.allowedBytes 的值；0 表示省略该 flag。
// parseRetentionDuration 解析 VL 的 retention 字面量（`120d` / `720h` / `30d`）。
//
// `time.ParseDuration` **不认识 `d`**（Go 没有 day 单位），而本项目与 VL 的
// retention 配置一直用 `120d` 这种写法 ⇒ 必须自己剥离 `d` 前缀再交给
// ParseDuration，否则所有 `Nd` 形式都会被判为非法、静默回落到默认值。
func parseRetentionDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty retention")
	}
	// `Nd` / `N.Md`（VL 允许小数天）；只处理单个 d 后缀，其余交给 ParseDuration。
	if strings.HasSuffix(s, "d") {
		num := strings.TrimSuffix(s, "d")
		f, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, fmt.Errorf("parse retention %q: %w", raw, err)
		}
		if f <= 0 {
			return 0, fmt.Errorf("non-positive retention %q", raw)
		}
		return time.Duration(f * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("parse retention %q: %w", raw, err)
	}
	return d, nil
}

// QueryTimeRangeLimit 返回 `-search.maxQueryTimeRange` 的取值：retention + 1d 余量。
//
// **必须由 retention 派生，不能硬编码**（2026-10-22 复审 P1）。历史事故链：
// 该参数原本是起步档的 1d，导致投影代次占用探测被 VL 以 400 `too big time range
// selected` 拒绝 ⇒ 探测「按未占用继续」⇒ 重名防护静默失效。当时把它调到 31d
// （= retention 30d + 1d），注释里已写明这个式子是 retention 的函数；但代码是
// 硬编码，retention 一旦改动就脱钩。2026-10-22 把默认 retention 提到 120d 后，
// 覆盖不到的那部分保留期重新出现同一个缺口。
//
// 解析失败时回落到 DefaultRetentionPeriod（而不是 31d）：宁可放大上限，也不能
// 让「探测覆盖不全」这种静默失败重新出现 —— 上限过大的代价只是少了一层 VL 侧的
// 粗保护，上限过小则是重名防护整条失效。
func (c InstanceConfig) QueryTimeRangeLimit() string {
	const slack = 24 * time.Hour
	d, err := parseRetentionDuration(c.RetentionPeriod)
	if err != nil || d <= 0 {
		d, err = parseRetentionDuration(DefaultRetentionPeriod)
		if err != nil || d <= 0 {
			// 连默认值都解析不了：退回历史上验证过的 31d。
			return "31d"
		}
	}
	// 用 h 而非 d 表示：VL 接受任何 Go duration 格式，h 不会有「月末取整」的歧义。
	return strconv.FormatInt(int64((d+slack)/time.Hour), 10) + "h"
}

func (c InstanceConfig) EffectiveCacheBytes() int64 {
	if c.MemoryAllowedBytes > 0 {
		return c.MemoryAllowedBytes
	}
	if c.Namespace == NamespaceHot {
		return DefaultHotCacheBytes
	}
	return 0
}

// Validate 校验实例配置。
func (c InstanceConfig) Validate() error {
	if !ValidNamespace(c.Namespace) {
		return fmt.Errorf("vlsup: unknown namespace %q", c.Namespace)
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("vlsup: invalid port %d", c.Port)
	}
	if strings.TrimSpace(c.StorageDataPath) == "" {
		return fmt.Errorf("vlsup: storageDataPath is required")
	}
	if strings.TrimSpace(c.RetentionPeriod) == "" {
		return fmt.Errorf("vlsup: retentionPeriod is required")
	}
	if strings.TrimSpace(c.AuthUsername) == "" || strings.TrimSpace(c.AuthPassword) == "" {
		return fmt.Errorf("vlsup: local basic-auth username and password are required")
	}
	if c.MemoryAllowedBytes < 0 {
		return fmt.Errorf("vlsup: memory.allowedBytes must be >= 0")
	}
	return nil
}

// BuildArgs 构造 victoria-logs 命令行参数。
//
// 固定包含：
//
//	-storageDataPath、-httpListenAddr=127.0.0.1:<port>（禁止 0.0.0.0 / 外网绑定）、
//	-retentionPeriod、-httpAuth.username、-httpAuth.password；
//	-memory.allowedBytes 在 EffectiveCacheBytes()>0 时写入（HOT 默认 512MiB 模板）。
func BuildArgs(cfg InstanceConfig) ([]string, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	args := []string{
		"-storageDataPath=" + cfg.StorageDataPath,
		"-httpListenAddr=" + ListenAddr(cfg.Port),
		"-retentionPeriod=" + cfg.RetentionPeriod,
		// 查询边界与资源上限（架构文档 §九 起步档）：并发槽、单查询上限 10s、
		// 查询时间窗上限 1d、排队上限 2s —— 宽窗查询不得抢占整机（30d 保留不受影响）。
		// 2026-10-07 调参（实测 ✓）：worker 的扇出对同一 VL 并发打 N 个目标扫描，
		// 162÷2×~0.3s ≈ 24.1s 与实测完全吻合 ⇒ 并发槽是全量冷查的直接瓶颈 ✗✓；
		// 故由起步档 2 提到 8（内部扇出吃满后，整机仍受切片 CPUQuota=200% 封顶 ✗，
		// 用户侧多查询的排队保护由 CP 侧 TTL/预热与 maxQueueDuration 承担 ✓）。
		// 2026-10-08 再调（实测 ✓）：投影校验/对账的积压重放批次窗口可达 17-22 小时
		// （单查 1-17s），与前台查询争抢 8 槽 ⇒ 面板查询被拖到 2-8s（绕过 worker
		// 直测 VL 同样慢）。配合 log_index.verify 限流（在飞 2）把槽扩到 16：
		// 后台校验仍限 2、前台留 14，不再互抢；VL 内存上限（memory.allowedBytes）
		// 仍在 VL 侧封顶，超额并发由 VL 自行降速而非崩溃。
		//
		// 2026-10-08 三调（查询时间窗 1d → 31d，实测 ✓）：投影代次**占用探测**必须
		// 覆盖该源的全部保留期（同一代次名在任意一天有旧行都会让「整窗重发」的校验
		// 失败），此前不带时间窗的探测被 VL 以 400 `too big time range selected`
		// （[1677-09-21, 2262-04-11] 全时段）拒绝 → 「按未占用继续」→ 重名防护静默
		// 失效（生产日志可查）。31d = retention 30d + 1d 余量；前台查询的窗口由
		// worker 侧合并段切分钳在 ≤24h，不受本次放宽影响。
		//
		// **边界比较符已实测确证**（同版本 VictoriaLogs v1.52.0 起本地实例逐点探测）：
		// 窗口恰好 744h（=31d）返回 200，744h + 10ns / 1µs / 1s 返回 400
		// `too big time range selected ... cannot exceed -search.maxQueryTimeRange=744h0m0s`
		// ⇒ VL 判定是**严格大于**（`>`），故"恰好 31d"被接受。
		// 注意：依赖方（query/vlrange 的 searchMaxWindow、ingest 的 projectionProbeSpan）
		// 恰好取 31d，落在**被接受的一侧**；将来若把 VL 侧上限调小，这些窗口必须同步调小，
		// 否则探测与合并粗筛会整组失败（重名防护静默失效 / 整组 RANGE_UNAVAILABLE）。
		// **依赖方必须保证的是「总跨度 ≤ 31d」而不是「回看量 ≤ 31d」**：ingest 的探测窗
		// 上界因无日期日志行可落在未来而前移到 now+1d，故其起点相应后移（[now-30d, now+1d]）。
		// 2026-10-12 实测踩过这个坑：上界前移 1 天而起点仍按 now-31d ⇒ 总跨度 32d ⇒
		// VL 400 ⇒ 「按未占用继续」⇒ 重名防护整条失效。改动探测窗时请一并复核总跨度。
		"-search.maxConcurrentRequests=16",
		"-search.maxQueryDuration=10s",
		// **查询时间窗上限由 retention 派生，不再硬编码 31d**（2026-10-22 复审 P1）。
		//
		// 上面那段注释写的是「31d = retention 30d + 1d 余量」，即这个上限本来就是
		// retention 的函数。但 2026-10-22 把 `DefaultRetentionPeriod` 从 30d 提到
		// 120d（默认值与 log_retention 声明的 90d/120d 对齐）之后，硬编码 31d 就与
		// retention **脱钩**了：保留期 120 天，而任何跨度 >31d 的查询被 VL 直接 400。
		// 受影响的是**投影代次占用探测** —— 它必须覆盖「同一代次名在保留期内的任意
		// 一天」，覆盖不到就「按未占用继续」⇒ 重名防护静默失效（这正是当初把 1d
		// 调到 31d 的原因，见上）。retention 越长，漏检窗口越大。
		//
		// 故改为 retention + 1d：既保留原来的 1 天余量语义，又让两者不可能再脱钩。
		// **总跨度约束**（见上）仍然由依赖方保证 ≤ 本值，故依赖方的窗口必须一并派生。
		"-search.maxQueryTimeRange=" + cfg.QueryTimeRangeLimit(),
		"-search.maxQueueDuration=2s",
	}
	if cache := cfg.EffectiveCacheBytes(); cache > 0 {
		args = append(args, "-memory.allowedBytes="+strconv.FormatInt(cache, 10))
	}
	if cfg.EnableDelete {
		// 删除 API 默认关闭：它不可逆，且会把「物理回收」这一能力从运维手里交给调用方。
		// 只有显式配置（log_vl.delete_enabled）才打开，用于孤儿代次 GC / 按级别 TTL。
		args = append(args, "-delete.enable")
	}
	args = append(args,
		"-httpAuth.username="+cfg.AuthUsername,
		"-httpAuth.password="+cfg.AuthPassword,
	)
	return args, nil
}

// StoragePathUnder 在 dataRoot 下为 namespace 生成独立数据根（数据根/namespace 不交叉）。
func StoragePathUnder(dataRoot string, ns Namespace) string {
	return filepath.Join(dataRoot, string(ns), "data")
}

// FormatCacheBytesForFlag 将字节数格式化为 VL size flag 可接受的十进制字节串。
// foundation 阶段直接写十进制字节，避免与产品配置单位混淆。
func FormatCacheBytesForFlag(n int64) string {
	return strconv.FormatInt(n, 10)
}
