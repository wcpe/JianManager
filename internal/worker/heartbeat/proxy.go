package heartbeat

import (
	"sync"

	"github.com/wcpe/JianManager/internal/platform/httpclient"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// proxyApplier 据心跳响应携带的期望代理运行时重建 Worker 出站 client（FR-185，见 ADR-043）。
//
// CP 每拍下发该节点期望代理（custom→节点值 / inherit→全局默认）+ generation（配置哈希）。
// applier 仅当 generation 与上次已应用代不同时才调 rebuild（避免每拍重建 client）。
// generation 为空表示 CP 期望直连（未配代理 / 未下发）：重建为空配，使 Worker 回退本地
// worker.yml/env（httpclient.New 空配语义）。重连/重启天然由后续心跳重发（无需落盘）。
type proxyApplier struct {
	// rebuild 用给定代理配置重建并替换 Worker 出站持有者；由 main 注入（包裹 Provider.Rebuild）。
	rebuild func(httpclient.Config) error

	mu          sync.Mutex
	appliedGen  string // 上次成功应用的 generation（空串=当前为空配/直连）
	hasApplied  bool   // 是否已应用过一次（区分「初始未应用」与「已应用空配」）
}

// newProxyApplier 创建代理应用器。rebuild 为 nil 时不应用（向后兼容：CP 未下发代理）。
func newProxyApplier(rebuild func(httpclient.Config) error) *proxyApplier {
	return &proxyApplier{rebuild: rebuild}
}

// apply 处理一次心跳响应里的期望代理。generation 变化才重建（重建失败不推进、下拍重试）。
func (p *proxyApplier) apply(resp *workerpb.HeartbeatResponse) {
	if p == nil || p.rebuild == nil || resp == nil {
		return
	}
	gen := resp.ProxyGeneration

	p.mu.Lock()
	defer p.mu.Unlock()
	// generation 未变且已应用过 → 无需重建。
	if p.hasApplied && gen == p.appliedGen {
		return
	}

	cfg := httpclient.Config{URL: resp.ProxyUrl, NoProxy: resp.ProxyNoProxy}
	if err := p.rebuild(cfg); err != nil {
		// 重建失败（如下发了非法代理）：不推进 appliedGen，下个心跳仍重试。
		return
	}
	p.appliedGen = gen
	p.hasApplied = true
}
