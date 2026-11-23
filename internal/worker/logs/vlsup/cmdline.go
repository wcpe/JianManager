package vlsup

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wcpe/JianManager/internal/platform/vlruntime"
)

// Namespace 标识受管 VL 实例类别。
//
// 2026-10-29：冷热双层已废除，**只剩一个 VL 实例**（NamespaceHot）。
//
// 为什么保留 Namespace 这个抽象而不把它抹平：Supervisor 的 instances/ports/budgets
// 都是按 Namespace 索引的 map（天然支持 1..N），删掉类型要改几十处签名，收益与风险不划算。
// 现在的形态是：抽象仍在，但只有**一个取值**，且没有任何代码按多层分派数据。
//
// 为什么仍叫 "hot" 而不改名：① 存量数据目录是 <data_root>/hot/data，改名要现场手工搬 21GB；
// ② catalog.OwnerHot 是账本里的数据归属值，两者同名免得读者以为它们是两个概念。
type Namespace string

const (
	// NamespaceHot 是唯一的受管 VL 实例。
	NamespaceHot Namespace = "hot"
)

// ValidNamespace 报告 ns 是否为受支持的命名空间。
func ValidNamespace(ns Namespace) bool { return ns == NamespaceHot }

// 默认监听端口（与C 组 supervisor-equivalent smoke 一致，可在 Options 覆盖）。
const (
	DefaultPortHot = 19441
)

// DefaultHotCacheBytes 是 HOT 实例 -memory.allowedBytes 默认模板（512MiB）。
// 依据§6.5/§7 与§3.1：512MiB 只代表 HOT cache 模板，不等于 RSS 上限，
// 也不是 Worker 日志总预算。
const DefaultHotCacheBytes int64 = 512 * 1024 * 1024

// DefaultRetentionPeriod 来自标准库叶子包的唯一默认源。
// 120d相对90d热窗口提供30d有限余量，超出已声明SLA仍可能被原生TTL淘汰。
const DefaultRetentionPeriod = vlruntime.DefaultRetentionPeriod

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
	MemoryAllowedBytes int64 // 0：使用 DefaultHotCacheBytes
	// MemoryAllowedPercent 非 0 且 MemoryAllowedBytes<=0 时改用 VL 官方推荐的
	// -memory.allowedPercent，让 VL 按宿主机可用内存自适应，避免换机器要改配置。
	MemoryAllowedPercent int
	AuthUsername         string
	AuthPassword         string
	// EnableDelete 打开 VL 的 -delete.enable，使 /delete/* API 可用。
	//
	// 为什么需要它（2026-10-21 架构复审）：`projection_generation` 白名单只让旧代次
	// 「看不见」，**不会让旧行不存在**；而 VL 只追加。在缺删除能力的前提下，任何
	// 「整窗重写 + 换代次」的设计都必然线性放大体积（生产实测单日 75×、880MB）。
	// 打开后可由孤儿代次 GC 按 `projection_generation NOT IN (已发布白名单)` 回收。
	//
	// 默认关闭（零值）：删除是不可逆操作，必须由运维显式承担；关闭时行为与改动前逐字一致。
	EnableDelete bool

	// MaxDiskSpaceUsageBytes 是 `-retention.maxDiskSpaceUsageBytes`：在 -storageDataPath
	// 所在分区上**超过这个字节数**时，VL 自动丢弃**最旧的按日分区**。
	//
	// 为什么要它（2026-10-29 用户要求「加磁盘上限」）：此前只有 `-retentionPeriod`，
	// 即磁盘用量完全由「180 天 × 实际写入量」决定——某个游戏服日志暴增时，磁盘跟着涨，
	// 没有任何上限。VL 提供了两种磁盘上限（**二者互斥**，见下方的 Percent 字段）：
	// 绝对字节数（本字段）与磁盘使用百分比。
	//
	// VL 官方语义：超过上限时**按最旧优先丢整日分区**（不是按字节精确切）。
	// 所以它买的是「磁盘不会被写满」，不是「永远保留恰好 N 字节」。
	MaxDiskSpaceUsageBytes int64

	// MaxDiskUsagePercent 是 `-retention.maxDiskUsagePercent`：按**磁盘使用百分比**（1-100）
	// 触发同一机制。
	//
	// 为什么保留两种而不只选一种（它们互斥，不能同时给）：
	//   - 字节数适合「我给 VL 划了多少盘」这种确定预算；
	//   - 百分比适合「和别的进程共享一块盘」——但共享盘上**任何**进程涨都会触发 VL 丢弃，
	//     包括非日志的进程，所以它只在 VL 独占一块盘时才是好口径。
	// 本项目的生产形态是 VL 与游戏服共享磁盘（/home 71%），因此**默认建议用字节数**。
	MaxDiskUsagePercent int
}

// DefaultPort 返回 namespace 的默认监听端口。
func DefaultPort(ns Namespace) int { return DefaultPortHot }

// ListenAddr 返回强制 localhost 的监听地址。
func ListenAddr(port int) string {
	return net.JoinHostPort(LocalhostBind, strconv.Itoa(port))
}

// parseRetentionDuration 复用无循环依赖的批准版运行时解析。
func parseRetentionDuration(raw string) (time.Duration, error) {
	return vlruntime.ParseRetention(raw)
}

// QueryTimeRangeLimit 保留兼容入口；非法配置返回空，不静默猜测较短查询窗口。
// 启动路径必须调用Checked版本，以错误明确拒绝非法参数。
func (c InstanceConfig) QueryTimeRangeLimit() string {
	limit, err := c.QueryTimeRangeLimitChecked()
	if err != nil {
		return ""
	}
	return limit
}

// QueryTimeRangeLimitChecked 精确派生retention+1d，不按整小时向下取整。
func (c InstanceConfig) QueryTimeRangeLimitChecked() (string, error) {
	d, err := vlruntime.ParseRetention(c.RetentionPeriod)
	if err != nil {
		return "", err
	}
	d += 24 * time.Hour
	if d%time.Hour == 0 {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h", nil
	}
	return d.String(), nil
}

func (c InstanceConfig) EffectiveCacheBytes() int64 {
	if c.MemoryAllowedBytes > 0 {
		return c.MemoryAllowedBytes
	}
	return DefaultNamespaceBudgets()[c.Namespace].CacheBytes
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
	if _, err := c.QueryTimeRangeLimitChecked(); err != nil {
		return fmt.Errorf("vlsup: 非法retentionPeriod: %w", err)
	}
	if strings.TrimSpace(c.AuthUsername) == "" || strings.TrimSpace(c.AuthPassword) == "" {
		return fmt.Errorf("vlsup: local basic-auth username and password are required")
	}
	if c.MemoryAllowedBytes < 0 {
		return fmt.Errorf("vlsup: memory.allowedBytes must be >= 0")
	}
	if c.MemoryAllowedPercent < 0 || c.MemoryAllowedPercent > 100 {
		return fmt.Errorf("vlsup: memory.allowedPercent must be within [0,100]")
	}
	if c.MaxDiskSpaceUsageBytes < 0 {
		return fmt.Errorf("vlsup: retention.maxDiskSpaceUsageBytes must be >= 0")
	}
	if c.MaxDiskUsagePercent < 0 || c.MaxDiskUsagePercent > 100 {
		return fmt.Errorf("vlsup: retention.maxDiskUsagePercent must be within [0,100]")
	}
	// 两者互斥是 VL 自己的约束（官方 help："mutually exclusive with"）。
	// 同时给出时不能“选一个先用”：运维写了两条就说明他自己也不确定要哪个，
	// 静默选一个会让磁盘上限的真值取决于我们而不是他。必须拒绝启动。
	if c.MaxDiskSpaceUsageBytes > 0 && c.MaxDiskUsagePercent > 0 {
		return fmt.Errorf("vlsup: retention.maxDiskSpaceUsageBytes 与 retention.maxDiskUsagePercent 互斥，只能配一个")
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
	queryLimit, err := cfg.QueryTimeRangeLimitChecked()
	if err != nil {
		return nil, err
	}
	args := []string{
		"-storageDataPath=" + cfg.StorageDataPath,
		"-httpListenAddr=" + ListenAddr(cfg.Port),
		"-retentionPeriod=" + cfg.RetentionPeriod,
		// 保留现有16并发/10s执行/2s排队基线，实际前后台没有保留槽。
		// cache预算不是查询或RSS硬上限；并发与verify调优必须另做同负载A/B。
		"-search.maxConcurrentRequests=16",
		"-search.maxQueryDuration=10s",
		// 占用探测总跨度必须覆盖有效保留期；单位和精度与配置校验共用唯一parser。
		"-search.maxQueryTimeRange=" + queryLimit,
		"-search.maxQueueDuration=2s",
	}
	// 官方推荐用 -memory.allowedPercent 让 VL 按宿主机可用内存自适应；
	// 显式配置百分比时优先于字节上限，避免换机器还要改配置。
	// 注意：hot_cache_bytes 仍作为内部 cache 预算记账使用，两者语义不同。
	if cfg.MemoryAllowedPercent > 0 {
		args = append(args, "-memory.allowedPercent="+strconv.Itoa(cfg.MemoryAllowedPercent))
	} else if cache := cfg.EffectiveCacheBytes(); cache > 0 {
		args = append(args, "-memory.allowedBytes="+strconv.FormatInt(cache, 10))
	}
	if cfg.EnableDelete {
		// 删除 API 默认关闭：它不可逆，且会把「物理回收」这一能力从运维手里交给调用方。
		// 只有显式配置（log_vl.delete_enabled）才打开，用于孤儿代次 GC / 按级别 TTL。
		args = append(args, "-delete.enable")
	}
	// 磁盘上限（2026-10-29）：与 -retentionPeriod **同时生效**且语义互补——
	// 保留期是「最老能留多久」的上限，磁盘上限是「总共能占多少」的上限，谁先到谁触发。
	// VL 侧的行为是**丢最旧的按日分区**，所以两者都开时是合理的兜底组合：
	// 日志量稳定时保留期满 180d 才淘汰；日志量暴涨时先撞磁盘上限、老分区提前丢。
	if cfg.MaxDiskSpaceUsageBytes > 0 {
		args = append(args, "-retention.maxDiskSpaceUsageBytes="+strconv.FormatInt(cfg.MaxDiskSpaceUsageBytes, 10))
	}
	if cfg.MaxDiskUsagePercent > 0 {
		args = append(args, "-retention.maxDiskUsagePercent="+strconv.Itoa(cfg.MaxDiskUsagePercent))
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
