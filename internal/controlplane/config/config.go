package config

import (
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/wcpe/JianManager/internal/platform/httpclient"
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
	ClientDist  ClientDistConfig  `mapstructure:"client_dist"`
	Enroll      EnrollConfig      `mapstructure:"enroll"`
	Update      UpdateConfig      `mapstructure:"update"`
	// Proxy CP 出站代理配置（FR-174，见 ADR-037）：自更新 feed/二进制、服务端 jar 等
	// 出站下载经此代理。url 留空=直连（沿用环境变量代理）。与各 Worker 各自独立配置。
	Proxy httpclient.Config `mapstructure:"proxy"`
}

// UpdateConfig 面板自更新（CP/Worker 二进制在线升级）配置（FR-081，GitHub 源见 FR-175/ADR-036 §7）。
// 全部可选、有合理默认；github_repo 与 feed_url 均空表示「未配置更新源」，检查更新返回未配置提示而非报错。
type UpdateConfig struct {
	// GitHubRepo owner/repo；非空即启用 GitHub Releases 源（FR-175，权威来源，见 ADR-036 §7）。
	// 默认 wcpe/JianManager（项目官方仓库），开箱即可在线升级。
	GitHubRepo string `mapstructure:"github_repo"`
	// Channel GitHub 源渠道：stable（默认，取 /releases/latest 最新正式）| prerelease（取 latest 滚动预发布，FR-182）。
	Channel string `mapstructure:"channel"`
	// GitHubToken 可选 GitHub API token；非空时请求带 Authorization 提升限流额度（匿名 60 次/时）。
	// 经 ${ENV_VAR} 引用、不硬编码（config-files 规范）。
	GitHubToken string `mapstructure:"github_token"`
	// FeedURL release feed JSON 地址（可选回退）：github_repo 为空且 feed_url 非空时走原 feed 路径（FR-081）。
	// 含私有源凭据时经 ${ENV_VAR} 引用、不硬编码（config-files 规范）。
	FeedURL string `mapstructure:"feed_url"`
	// BinaryBaseURL 私有二进制基址（可选兜底）：无 feed 时按 <base>/<component>-<os>-<arch> 约定拼下载地址。
	BinaryBaseURL string `mapstructure:"binary_base_url"`
	// AllowInsecure 是否允许 http 下载源（默认仅 https，避免二进制被中间人篡改）。本地/内网自测可开。
	AllowInsecure bool `mapstructure:"allow_insecure"`
}

// EnrollConfig 节点 enrollment 一键安装配置（FR-080，见 ADR-020）。
// 用于拼装面板「添加节点」展示的一键安装命令；全部可选、有合理默认。
type EnrollConfig struct {
	// AdvertiseGRPC 对外公布的 CP gRPC 地址（host:port），写入 Worker 一键命令的 --control-plane。
	// 留空则由签发请求的 Host 推断 host、配合 grpc.port 拼装（适配大多数同机/反代场景）。
	AdvertiseGRPC string `mapstructure:"advertise_grpc"`
	// ScriptBaseURL 安装脚本的下载基址（如 https://cp.example.com）。留空则用签发请求的 scheme://Host。
	// 一键命令据此拼 <base>/install-worker.sh 与 <base>/install-worker.ps1。
	ScriptBaseURL string `mapstructure:"script_base_url"`
	// BinaryURL Worker 二进制下载基址/地址，写入一键命令的 --download-url。
	// 默认 GitHub Releases latest（ADR-036 产物命名契约 worker-<os>-<arch>[.exe]），一键命令开箱即下载；
	// 内网/私有源可覆盖；显式置空则一键命令不带 --download-url，运营改用脚本 --binary 本地兜底。
	BinaryURL string `mapstructure:"binary_url"`
}

// ClientDistConfig 客户端分发（OTA）签名配置（FR-087，见 ADR-022）。
type ClientDistConfig struct {
	// SignPrivKey 客户端 manifest 签名私钥（base64 of PKCS#8 DER, Ed25519）。
	// 敏感信息：生产须经环境变量 JIANMANAGER_CLIENT_SIGN_PRIVKEY 注入、不入库（config-files 规范）。
	// 生产态（dev_mode=false）未注入即 fail-closed 拒绝启动；仅 dev_mode=true 回退内置开发密钥
	// （service.DevSignPrivateKeyPKCS8Base64，零配置开发用），裁决见 service.ResolveManifestSigner。
	SignPrivKey string `mapstructure:"sign_priv_key"`
	// SignKeyID 签名公钥版本标识（轮换用，默认 k1，须与客户端内置公钥 keyId 一致）。
	SignKeyID string `mapstructure:"sign_key_id"`
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
	// 客户端分发签名（FR-087）：私钥默认空（main 回退内置开发密钥）；keyId 默认 k1。
	v.SetDefault("client_dist.sign_priv_key", "")
	v.SetDefault("client_dist.sign_key_id", "k1")
	// 节点 enrollment 一键安装（FR-080）：CP 地址/脚本基址默认空，由签发请求 Host 推断。
	// 二进制源默认指向 GitHub Releases latest（ADR-036 产物命名契约 worker-<os>-<arch>[.exe]），
	// 使一键命令开箱即下载、无需 --binary；内网/私有源经 enroll.binary_url 覆盖。
	v.SetDefault("enroll.advertise_grpc", "")
	v.SetDefault("enroll.script_base_url", "")
	v.SetDefault("enroll.binary_url", "https://github.com/wcpe/jianmanager/releases/latest/download")
	// 面板自更新（FR-081 / FR-175）：默认读 GitHub Releases 源（ADR-036 §7），开箱即可在线升级。
	// github_repo 默认官方仓库、channel 默认 stable（取最新正式 release）；token 默认空（匿名 60 次/时够手动用）。
	// feed_url/binary_base_url 为可选回退（github_repo 空且 feed_url 非空时走原 feed 路径）；仅允许 https。
	v.SetDefault("update.github_repo", "wcpe/JianManager")
	v.SetDefault("update.channel", "stable")
	v.SetDefault("update.github_token", "")
	v.SetDefault("update.feed_url", "")
	v.SetDefault("update.binary_base_url", "")
	v.SetDefault("update.allow_insecure", false)
	// 出站代理（FR-174，见 ADR-037）：默认空（直连/沿用环境变量代理），不破坏现状。
	v.SetDefault("proxy.url", "")
	v.SetDefault("proxy.no_proxy", "")
	// 显式绑定任务约定的私钥环境变量名（敏感信息经 env 注入、不入库，config-files 规范）。
	_ = v.BindEnv("client_dist.sign_priv_key", "JIANMANAGER_CLIENT_SIGN_PRIVKEY")

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
