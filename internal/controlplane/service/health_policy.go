package service

import (
	"strconv"
	"strings"
	"time"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
)

// 实例健康巡检（FR-459）平台设置默认值（与 Worker 侧 process 包默认对齐）。
const (
	defaultHealthScanInterval       = 30 * time.Second
	defaultHealthSuspicionThreshold = 3
	defaultHealthCircuitThreshold   = 5
	defaultHealthCircuitWindow      = 10 * time.Minute
	// defaultHealthStartupWarmup 启动宽限期默认值（与 process.DefaultStartupWarmup 对齐，FR-459 复审项 2）。
	defaultHealthStartupWarmup = 5 * time.Minute
	// defaultHealthSelfHealMaxRestarts 假死自愈启动风暴护栏默认值（与 process.DefaultSelfHealMaxRestarts 对齐）。
	defaultHealthSelfHealMaxRestarts = 3
)

// HealthScanPolicy 返回实例健康巡检当前生效策略（FR-459），供心跳响应下发 Worker。
// 实现 grpc.HealthPolicyResolver；值非法/未配置回退默认，保证 Worker 侧始终拿到可用策略。
func (s *SettingsService) HealthScanPolicy() cpgrpc.HealthPolicySnapshot {
	return cpgrpc.HealthPolicySnapshot{
		Enabled:                 s.EffectiveValue(SettingKeyHealthScanEnabled) == "true",
		ScanInterval:            parseDurationDefault(s.EffectiveValue(SettingKeyHealthScanInterval), defaultHealthScanInterval),
		ProbeKind:               strings.TrimSpace(strings.ToLower(s.EffectiveValue(SettingKeyHealthProbeKind))),
		SuspicionThreshold:      atoiDefault(s.EffectiveValue(SettingKeyHealthSuspicionThreshold), defaultHealthSuspicionThreshold),
		Action:                  strings.TrimSpace(strings.ToLower(s.EffectiveValue(SettingKeyHealthAction))),
		CircuitBreakerThreshold: atoiDefault(s.EffectiveValue(SettingKeyHealthCircuitThreshold), defaultHealthCircuitThreshold),
		CircuitBreakerWindow:    parseDurationDefault(s.EffectiveValue(SettingKeyHealthCircuitWindow), defaultHealthCircuitWindow),
		StartupWarmup:           parseDurationDefault(s.EffectiveValue(SettingKeyHealthStartupWarmup), defaultHealthStartupWarmup),
		SelfHealMaxRestarts:     atoiDefault(s.EffectiveValue(SettingKeyHealthSelfHealMaxRestarts), defaultHealthSelfHealMaxRestarts),
	}
}

// parseDurationDefault 解析 Go duration 文本：非法/非正回退 fallback。
// 与 parseDurationOr 不同：**不做上界钳制**（巡检周期/熔断窗口不受 MC 直探超时上界约束）。
func parseDurationDefault(raw string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// atoiDefault 解析整数文本：非法回退 fallback。
func atoiDefault(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return n
}
