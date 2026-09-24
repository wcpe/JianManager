package vlsup

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// Namespace 标识受管 VL 实例类别（FR-475：Worker supervisor 分别管理 HOT/COLD/Rehydrate）。
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

// 默认监听端口（与 FR-472 C 组 supervisor-equivalent smoke 一致，可在 Options 覆盖）。
const (
	DefaultPortHot       = 19441
	DefaultPortCold      = 19442
	DefaultPortRehydrate = 19443
)

// DefaultHotCacheBytes 是 HOT 实例 -memory.allowedBytes 默认模板（512MiB）。
// 依据 FR-472 §6.5/§7 与 FR-475 §3.1：512MiB 只代表 HOT cache 模板，不等于 RSS 上限，
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
