// Package config 提供 Worker Node 的配置加载（worker.yml + 环境变量覆盖）。
//
// 配置真正落盘到 worker.yml 而非堆砌 JIANMANAGER_* 环境变量（FR-080，见 ADR-020）；
// 所有项有合理默认，零配置即可启动开发环境。环境变量以 JIANMANAGER_ 前缀按路径覆盖
// （如 server gRPC 端口 → JIANMANAGER_GRPC_PORT），与 Control Plane 配置惯例一致。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// Config Worker Node 配置。
type Config struct {
	Name         string     `mapstructure:"name"`
	ControlPlane string     `mapstructure:"control_plane"`
	NodeSecret   string     `mapstructure:"node_secret"`
	GRPC         GRPCConfig `mapstructure:"grpc"`
	WS           WSConfig   `mapstructure:"ws"`
	// DataDir 是项目自包含数据根（默认 ./data，可经 JIANMANAGER_DATA_DIR 覆盖）。
	// JDK、服务器工作目录等运行态数据统一收口到此根。参见 ADR-010。
	DataDir string `mapstructure:"data_dir"`
	// ServersDir 兼容旧配置：显式指定时覆盖数据根派生的 var/servers。
	// 留空则由数据根派生为 <DataDir>/var/servers。
	ServersDir string `mapstructure:"servers_dir"`
	// Host 注册上报给 CP 的本机地址；留空则自动探测出口 IP（供 CP 反向连接）。
	Host string `mapstructure:"host"`
	// JWTSecret WS 终端/插件桥一次性 token 校验密钥；与 CP 共享。
	JWTSecret string `mapstructure:"jwt_secret"`
	// EnrollToken 一次性 enrollment token 明文（FR-080，见 ADR-020）。
	// 仅经环境变量/命令行传入、绝不写入 worker.yml（一次性凭据不留盘）；
	// 首次注册（无本地身份文件）时携带，注册成功后即作废。
	EnrollToken string           `mapstructure:"enroll_token"`
	Log         LogConfig        `mapstructure:"log"`
	Decompiler  DecompilerConfig `mapstructure:"decompiler"`
	// Search 全文搜索索引配置（FR-074，见 ADR-017）。
	Search SearchConfig `mapstructure:"search"`
	// ArtifactCache 节点本地制品缓存配置（FR-178）：按 sha256 缓存下载过的核心 jar，建实例命中即秒拷。
	ArtifactCache ArtifactCacheConfig `mapstructure:"artifact_cache"`
	// Proxy 本节点出站代理配置（FR-174，见 ADR-037）：所有出站下载（自更新/JDK/CFR）
	// 经此代理。url 留空=直连（沿用环境变量代理）。各 Worker 在不同机器各配各的。
	Proxy     httpclient.Config `mapstructure:"proxy"`
	Heartbeat time.Duration     `mapstructure:"-"`
}

// SearchConfig 全文搜索索引配置（FR-074，见 ADR-017）。
type SearchConfig struct {
	// Ignore 追加到内置默认忽略集的 glob 规则（相对实例工作目录，/ 分隔）。
	// 形如 logs/（目录前缀）、*.bak（basename glob）、vendor（路径段）。默认集已覆盖常见
	// 日志/缓存/二进制/归档/MC 世界数据，零配置即可用；此处仅做加性补充。
	Ignore []string `mapstructure:"ignore"`
}

// ArtifactCacheConfig 节点本地制品缓存配置（FR-178）。
type ArtifactCacheConfig struct {
	// MaxBytes 缓存容量上限（字节，0=不限）。存入新项后若超限按 lastUsedAt 升序 LRU 淘汰。
	// 可经 CP 端点 PUT /nodes/:id/artifact-cache/cap 运行时下发覆盖。
	MaxBytes int64 `mapstructure:"max_bytes"`
}

// DecompilerConfig 反编译能力配置（FR-075，见 ADR-018）。
type DecompilerConfig struct {
	// CFRPath 显式 CFR 反编译器 jar 路径（最高优先级，可空）。
	// 运维离线放置时直接指定；空则回退内嵌/数据根缓存/按需下载。
	CFRPath string `mapstructure:"cfr_path"`
	// AllowDownload 是否允许从 Maven Central 按需下载 CFR jar（sha256 pin 校验后落数据根缓存）。
	// 默认开启；离线环境可关并用 CFRPath/内嵌。
	AllowDownload bool `mapstructure:"allow_download"`
}

// GRPCConfig gRPC 服务器配置。
type GRPCConfig struct {
	Port int `mapstructure:"port"`
}

// WSConfig WebSocket 服务器配置。
type WSConfig struct {
	Port int `mapstructure:"port"`
}

// LogConfig 日志配置。
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// Load 从配置文件和环境变量加载 Worker 配置（FR-080，见 ADR-020）。
//
// path 为空时在工作目录与 configs/ 下查找 worker.yml（均可选），找不到回退 worker.yaml（FR-224 兼容）。
// 所有项有合理默认，零配置即可启动；JIANMANAGER_ 前缀环境变量按路径覆盖配置文件值。
// enrollment token 经 JIANMANAGER_ENROLL_TOKEN 注入、不从 yaml 读取（一次性凭据不留盘）。
func Load(path string) (*Config, error) {
	v := viper.New()

	// 默认值（零配置即可启动开发环境）。
	v.SetDefault("name", "node-01")
	v.SetDefault("control_plane", "localhost:9100")
	v.SetDefault("node_secret", "")
	v.SetDefault("grpc.port", 9101)
	v.SetDefault("ws.port", 9102)
	v.SetDefault("data_dir", "")
	v.SetDefault("servers_dir", "")
	v.SetDefault("host", "")
	v.SetDefault("jwt_secret", "dev-secret-change-me")
	v.SetDefault("enroll_token", "")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "text")
	v.SetDefault("search.ignore", []string{})
	// 节点制品缓存（FR-178）：默认 0=不限（建实例命中即秒拷免重下；按需经 CP 设上限触发 LRU）。
	v.SetDefault("artifact_cache.max_bytes", int64(0))
	// 反编译（FR-075）：默认无显式 CFR 路径、允许按需下载（首次反编译时拉 CFR 落数据根缓存）。
	v.SetDefault("decompiler.cfr_path", "")
	v.SetDefault("decompiler.allow_download", true)
	// 出站代理（FR-174，见 ADR-037）：默认空（直连/沿用环境变量代理），不破坏现状。
	v.SetDefault("proxy.url", "")
	v.SetDefault("proxy.no_proxy", "")

	if path != "" {
		v.SetConfigFile(path)
	} else {
		// .yml 优先、找不到回退 .yaml（FR-224）。viper 默认搜索按 SupportedExts 顺序（yaml 先于 yml），
		// 无法据此让 .yml 优先；故显式按 [.yml, .yaml] 在搜索目录探测，命中即 SetConfigFile。
		// 两者同为 YAML 格式，SetConfigType 固定 yaml 保证解析正确。
		v.SetConfigName("worker")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("configs")
		if found := findConfigFile("worker", ".", "configs"); found != "" {
			v.SetConfigFile(found)
		}
	}

	v.SetEnvPrefix("JIANMANAGER")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	// 显式绑定与历史 main.go 不同名的环境变量，保持向后兼容。
	_ = v.BindEnv("name", "JIANMANAGER_NODE_NAME")
	_ = v.BindEnv("control_plane", "JIANMANAGER_CONTROL_PLANE_GRPC")
	_ = v.BindEnv("data_dir", "JIANMANAGER_DATA_DIR")
	_ = v.BindEnv("servers_dir", "JIANMANAGER_WORK_DIR")
	_ = v.BindEnv("enroll_token", "JIANMANAGER_ENROLL_TOKEN")

	_ = v.ReadInConfig() // 配置文件可选

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// findConfigFile 在给定目录中按 .yml 优先、.yaml 兼容回退的顺序查找 <name>.<ext> 配置文件（FR-224）。
// 返回首个存在的文件路径；都不存在时返回空串（交回 viper 的名字搜索 + 默认值，零配置仍可启动）。
func findConfigFile(name string, dirs ...string) string {
	for _, dir := range dirs {
		for _, ext := range []string{"yml", "yaml"} {
			p := filepath.Join(dir, name+"."+ext)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}
