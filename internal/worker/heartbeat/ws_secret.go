package heartbeat

import (
	"log/slog"
	"sync"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// wsSecretApplier 据心跳响应携带的 WS 令牌密钥热应用到 Worker（FR-275，见 ADR-061）。
//
// CP 每拍下发当前 WS 令牌密钥（终端/插件桥校验用）；applier 仅当值与当前生效值不同时才
// 调 apply（热更新两个 WS 服务器 + 持久化身份文件）——CP 轮换密钥后 Worker 不重启即自愈。
// 下发为空表示旧 CP/未注入：不动作（Worker 沿用当前值，向后兼容）。
type wsSecretApplier struct {
	// apply 用新密钥热更新终端/插件桥校验并持久化身份文件；由 main 注入。
	apply func(secret string) error

	mu      sync.Mutex
	current string // 当前生效密钥（初始 = 启动时的生效值，注册下发/身份文件/本地配置）
}

// newWSSecretApplier 创建 WS 令牌密钥应用器。current 为启动时已生效的密钥（用于首拍去重）。
func newWSSecretApplier(current string, apply func(string) error) *wsSecretApplier {
	return &wsSecretApplier{current: current, apply: apply}
}

// applyReply 处理一次心跳响应里的 WS 令牌密钥。值变化才应用；持久化失败仅告警不回滚——
// 内存已热更新（校验即时正确），文件补写靠下次注册下发兜底（重启后重注册必再下发）。
func (a *wsSecretApplier) applyReply(resp *workerpb.HeartbeatResponse) {
	if a == nil || a.apply == nil || resp == nil {
		return
	}
	secret := resp.GetWsTokenSecret()
	if secret == "" {
		return
	}

	a.mu.Lock()
	if secret == a.current {
		a.mu.Unlock()
		return
	}
	a.current = secret
	a.mu.Unlock()

	if err := a.apply(secret); err != nil {
		slog.Warn("应用 CP 下发的 WS 令牌密钥失败（内存可能已部分生效，重启后经注册自愈）", "error", err)
		return
	}
	slog.Info("WS 令牌密钥已据 CP 下发运行时更新（终端/插件桥校验即时生效）")
}
