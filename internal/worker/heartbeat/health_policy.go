package heartbeat

import (
	"time"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// applyHealthPolicy 把 CP 经心跳响应下发的实例健康巡检策略（FR-459）应用到巡检器。
//
// 下发以 HealthScanEnabled（proto3 optional）为「是否下发」的存在标记：nil=老 CP 未下发 →
// 不动作，Worker 沿用本地 worker.yml 配置（向后兼容）；非 nil → 采用本响应全部 health_* 字段。
// 值 <=0 的阈值/周期由进程侧归一取默认（见 process.normalizeHealthPolicy），不会把策略归零。
//
// 每拍都调用、幂等：CP 改设置后 Worker 不重启即在下一拍生效（≤1 心跳周期）。
func (h *Heartbeat) applyHealthPolicy(resp *workerpb.HeartbeatResponse) {
	if h == nil || h.healthApplier == nil || resp == nil || resp.HealthScanEnabled == nil {
		return
	}
	h.healthApplier(process.HealthPolicy{
		Enabled:                 resp.GetHealthScanEnabled(),
		ScanInterval:            time.Duration(resp.GetHealthScanIntervalMs()) * time.Millisecond,
		ProbeKind:               resp.GetHealthProbeKind(),
		SuspicionThreshold:      int(resp.GetHealthSuspicionThreshold()),
		Action:                  resp.GetHealthAction(),
		CircuitBreakerThreshold: int(resp.GetHealthCircuitBreakerThreshold()),
		CircuitBreakerWindow:    time.Duration(resp.GetHealthCircuitBreakerWindowMs()) * time.Millisecond,
		StartupWarmup:           time.Duration(resp.GetHealthStartupWarmupMs()) * time.Millisecond,
		SelfHealMaxRestarts:     int(resp.GetHealthSelfHealMaxRestarts()),
	})
}
