package config

import (
	"time"
)

// Config Worker Node 配置。
type Config struct {
	Name         string        `mapstructure:"name"`
	ControlPlane string        `mapstructure:"control_plane"`
	NodeSecret   string        `mapstructure:"node_secret"`
	GRPC         GRPCConfig    `mapstructure:"grpc"`
	WS           WSConfig      `mapstructure:"ws"`
	ServersDir   string        `mapstructure:"servers_dir"`
	Log          LogConfig     `mapstructure:"log"`
	Heartbeat    time.Duration `mapstructure:"-"`
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
