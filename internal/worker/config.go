// Package config 提供 Worker Node 的配置加载（worker.yaml + 环境变量覆盖）。
//
// 配置真正落盘到 worker.yaml 而非堆砌 JIANMANAGER_* 环境变量（FR-080，见 ADR-020）；
// 所有项有合理默认，零配置即可启动开发环境。环境变量以 JIANMANAGER_ 前缀按路径覆盖
// （如 server gRPC 端口 → JIANMANAGER_GRPC_PORT），与 Control Plane 配置惯例一致。
package config

import (
	"strings"
	"time"

	"github.com/spf13/viper"
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
	// 仅经环境变量/命令行传入、绝不写入 worker.yaml（一次性凭据不留盘）；
	// 首次注册（无本地身份文件）时携带，注册成功后即作废。
	EnrollToken string        `mapstructure:"enroll_token"`
	Log         LogConfig     `mapstructure:"log"`
	Heartbeat   time.Duration `mapstructure:"-"`
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
// path 为空时在工作目录与 configs/ 下查找 worker.yaml（均可选）。
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

	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("worker")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("configs")
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
