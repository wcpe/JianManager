package config

import (
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// LogLevelVar 是 Control Plane 日志器的动态级别持有者（FR-063 / ADR-015）。
//
// 进程启动时 initLogger 用它构造 slog handler，使日志级别可在运行时切换
// （平台设置写入 log.level 后调用 SetLogLevel 即时生效，无需重启）。
var LogLevelVar = new(slog.LevelVar)

// ParseLogLevel 把日志级别文本解析为 slog.Level。
// 取值：debug | info | warn | error；无法识别时回退 info。
func ParseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// SetLogLevel 运行时切换 Control Plane 日志级别。
// 因为 LogLevelVar 已注入到当前 slog handler，调用后立即影响后续日志输出。
func SetLogLevel(level string) {
	LogLevelVar.Set(ParseLogLevel(level))
}

// ValidLogLevel 报告日志级别文本是否在允许枚举内。
func ValidLogLevel(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "info", "warn", "error":
		return true
	}
	return false
}

// Config 是 Control Plane 的完整配置。
type Config struct {
	Server      ServerConfig      `mapstructure:"server"`
	GRPC        GRPCConfig        `mapstructure:"grpc"`
	Database    DatabaseConfig    `mapstructure:"database"`
	JWT         JWTConfig         `mapstructure:"jwt"`
	Log         LogConfig         `mapstructure:"log"`
	LogStore    LogStoreConfig    `mapstructure:"log_store"`
	FileVersion FileVersionConfig `mapstructure:"file_version"`
}

// ServerConfig HTTP 服务器配置。
type ServerConfig struct {
	Host    string `mapstructure:"host"`
	Port    int    `mapstructure:"port"`
	DevMode bool   `mapstructure:"dev_mode"`
}

// GRPCConfig gRPC 服务器配置。
type GRPCConfig struct {
	Port int `mapstructure:"port"`
}

// DatabaseConfig 数据库配置。
type DatabaseConfig struct {
	Driver string `mapstructure:"driver"`
	DSN    string `mapstructure:"dsn"`
}

// JWTConfig JWT 认证配置。
type JWTConfig struct {
	Secret     string        `mapstructure:"secret"`
	AccessTTL  time.Duration `mapstructure:"access_ttl"`
	RefreshTTL time.Duration `mapstructure:"refresh_ttl"`
}

// LogConfig 日志配置。
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// LogStoreConfig 日志持久化、归档与保留配置（FR-049）。
// 所有字段都有合理默认值，零配置即可工作；归档目录恒为数据根 var/log（不可配，保证便携自洽）。
type LogStoreConfig struct {
	// Enabled 是否启用日志入库与归档。默认 true。
	Enabled bool `mapstructure:"enabled"`
	// PersistPlatform 是否把平台（Control Plane）结构化日志一并落库。默认 true。
	PersistPlatform bool `mapstructure:"persist_platform"`
	// RetentionDays 保留天数：早于此天数的日志在每轮归档时滚动落盘并从表中清理。默认 14。<=0 表示不按时间清理。
	RetentionDays int `mapstructure:"retention_days"`
	// MaxTotalMB 表内日志总量上限（MB）：超出时从最旧开始归档落盘直到回落阈值内。默认 512。<=0 表示不按总量清理。
	MaxTotalMB int `mapstructure:"max_total_mb"`
	// ArchiveIntervalMinutes 后台归档/保留巡检周期（分钟）。默认 30。
	ArchiveIntervalMinutes int `mapstructure:"archive_interval_minutes"`
}

// FileVersionConfig 通用文件版本（FR-051）配置。
type FileVersionConfig struct {
	// MaxPerFile 单文件保留的版本上限；超出时删除最旧版本。<=0 表示不限制。
	MaxPerFile int `mapstructure:"max_per_file"`
	// MaxSizeBytes 单文件触发快照的大小上限；超过此值跳过版本快照，避免大文件（如世界存档）撑爆 DB。<=0 表示不限制。
	MaxSizeBytes int64 `mapstructure:"max_size_bytes"`
}

// Load 从文件和环境变量加载配置。
func Load(path string) (*Config, error) {
	v := viper.New()

	// 默认值
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 8080)
	v.SetDefault("server.dev_mode", false)
	v.SetDefault("grpc.port", 9100)
	v.SetDefault("database.driver", "sqlite")
	v.SetDefault("database.dsn", "data/jianmanager.db")
	v.SetDefault("jwt.secret", "dev-secret-change-me")
	v.SetDefault("jwt.access_ttl", "15m")
	v.SetDefault("jwt.refresh_ttl", "168h")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("log_store.enabled", true)
	v.SetDefault("log_store.persist_platform", true)
	v.SetDefault("log_store.retention_days", 14)
	v.SetDefault("log_store.max_total_mb", 512)
	v.SetDefault("log_store.archive_interval_minutes", 30)
	// 文件版本（FR-051）：默认每文件保留 20 个版本，单文件 ≤5MiB 才快照。
	v.SetDefault("file_version.max_per_file", 20)
	v.SetDefault("file_version.max_size_bytes", 5*1024*1024)

	// 配置文件
	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("control-plane")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("configs")
	}

	// 环境变量
	v.SetEnvPrefix("JIANMANAGER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	_ = v.ReadInConfig() // 配置文件可选

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
