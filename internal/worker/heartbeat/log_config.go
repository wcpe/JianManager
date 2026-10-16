package heartbeat

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/ingest"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// logAcqApplier 据心跳响应携带的日志采集配置热应用到采集运行时（FR-503）。
//
// 与 wsSecretApplier 同型：CP 每拍重发生效值，只有**比对出变化**才应用（避免每拍
// 重复写盘、刷日志）。存在标记用 proto3 optional（LogAcqPresent）——nil = 旧 CP 未下发，
// 不动作，Worker 沿用本地已加载值（向后兼容）。
type logAcqApplier struct {
	// apply 应用新策略（写采集运行时生效值 + 持久化）；由 main 注入。
	apply func(ingest.AcqPolicy) error

	mu sync.Mutex
	// current 是最近一次**成功应用**的策略，用于去重。启动初值取本地持久化快照
	// （无快照则取默认），使首拍不会对「本来就一样」的配置做无谓写盘。
	current ingest.AcqPolicy
}

// newLogAcqApplier 创建采集配置应用器。current 为启动时已生效的策略。
func newLogAcqApplier(current ingest.AcqPolicy, apply func(ingest.AcqPolicy) error) *logAcqApplier {
	return &logAcqApplier{current: current, apply: apply}
}

// applyLogAcqPolicy 应用 CP 下发的日志采集配置（FR-503）。未注入 applier 时不动作。
func (h *Heartbeat) applyLogAcqPolicy(resp *workerpb.HeartbeatResponse) {
	if h == nil {
		return
	}
	h.logAcqApplier.applyReply(resp)
}

// applyReply 处理一次心跳响应里的日志采集配置。
//
// 存在标记为 nil（旧 CP 未下发）时**不动作**：否则各字段按零值解释，会把
// 「规则关闭 + 实时采集关闭」误当成 CP 的意图而停掉采集——这是最危险的一种误读。
func (a *logAcqApplier) applyReply(resp *workerpb.HeartbeatResponse) {
	if a == nil || a.apply == nil || resp == nil || resp.LogAcqPresent == nil {
		return
	}
	next := ingest.AcqPolicy{
		RulesEnabled:    resp.GetLogAcqRulesEnabled(),
		RealtimeTail:    resp.GetLogAcqRealtimeTail(),
		GzImportEnabled: resp.GetLogAcqGzImportEnabled(),
		GzScanInterval:  time.Duration(resp.GetLogAcqGzScanIntervalMs()) * time.Millisecond,
		PathOverrides:   parsePathOverridesJSON(resp.GetLogAcqPathOverridesJson()),
	}

	a.mu.Lock()
	if ingest.AcqPolicyEqual(a.current, next) {
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	if err := a.apply(next); err != nil {
		// 失败只告警、不阻断心跳（配置问题绝不能让节点被判离线），并**不更新 current**：
		// CP 每拍重发，下一拍自然重试完整应用（内存写入幂等，重复调用无副作用）。
		slog.Warn("应用 CP 下发的日志采集配置失败（下一拍心跳将重试）", "error", err)
		return
	}

	a.mu.Lock()
	a.current = next
	a.mu.Unlock()

	slog.Info("日志采集配置已据 CP 下发更新",
		"rulesEnabled", next.RulesEnabled,
		"realtimeTail", next.RealtimeTail,
		"gzImportEnabled", next.GzImportEnabled,
		"gzScanInterval", next.GzScanInterval.String(),
		"pathOverrideCount", len(next.PathOverrides))
}

// parsePathOverridesJSON 解析路径覆盖 JSON 文本。
//
// 非法或空文本一律回退为「无覆盖」而非报错：CP 侧写入时已校验，此处只为兜住
// 脏值（历史遗留/人工改库）。一个坏字段不该让整份采集配置被拒绝应用。
func parsePathOverridesJSON(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		slog.Warn("CP 下发的日志路径覆盖不是合法 JSON，已按无覆盖处理", "raw", raw)
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
