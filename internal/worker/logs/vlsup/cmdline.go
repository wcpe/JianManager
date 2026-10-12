package vlsup

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
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

// DefaultRetentionPeriod 是 foundation 阶段的 VL runtime retention 模板。
// 产品 online_retention / move_after_age 与 runtime retention 分离映射（契约 §7）。
const DefaultRetentionPeriod = "30d"

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
		"-search.maxQueryTimeRange=31d",
		"-search.maxQueueDuration=2s",
	}
	if cache := cfg.EffectiveCacheBytes(); cache > 0 {
		args = append(args, "-memory.allowedBytes="+strconv.FormatInt(cache, 10))
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
