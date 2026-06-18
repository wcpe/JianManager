package config

import (
	"time"
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
	ServersDir string        `mapstructure:"servers_dir"`
	Log        LogConfig     `mapstructure:"log"`
	Heartbeat  time.Duration `mapstructure:"-"`
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
