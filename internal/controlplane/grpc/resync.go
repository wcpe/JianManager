package grpc

import (
	"sync"
	"time"
)

// ResyncDeduper 按节点对「实例规格重推」（InstanceService.ResyncNode）做单飞 + 节流（FR-455②）。
//
// 背景：重推触发当前多源化——隧道 onOpen、Register 成功、心跳兜底都会触发同一入口。
// 多源必然带来重复触发（如瞬时 active=2 的两次 onOpen、Register 紧随心跳）；由于 Worker 侧
// ResyncInstances 是「只补不覆盖」的幂等语义，重复触发无副作用，但会产生冗余 RPC。本类型
// 以「同节点单飞 + 冷却窗口」吸收重复，既保证「不漏推」（每次事件都尝试触发），又避免「猛推」。
type ResyncDeduper struct {
	mu       sync.Mutex
	inflight map[string]struct{}
	last     map[string]time.Time
	cooldown time.Duration
	now      func() time.Time
}

// NewResyncDeduper 创建去重器。cooldown<=0 表示仅单飞、不节流。
func NewResyncDeduper(cooldown time.Duration) *ResyncDeduper {
	if cooldown < 0 {
		cooldown = 0
	}
	return &ResyncDeduper{
		inflight: make(map[string]struct{}),
		last:     make(map[string]time.Time),
		cooldown: cooldown,
		now:      time.Now,
	}
}

// Trigger 触发该节点的一次重推：同节点已有在飞、或处于冷却窗口内则跳过（幂等）。
// fn 在独立 goroutine 中执行，避免阻塞触发方（可能位于心跳/隧道回调路径）。
// 返回 true 表示本次真正发起了重推。
func (d *ResyncDeduper) Trigger(nodeUUID string, fn func(string)) bool {
	if d == nil || nodeUUID == "" || fn == nil {
		return false
	}
	now := d.now()
	d.mu.Lock()
	if _, busy := d.inflight[nodeUUID]; busy {
		d.mu.Unlock()
		return false
	}
	if last, ok := d.last[nodeUUID]; ok && now.Sub(last) < d.cooldown {
		d.mu.Unlock()
		return false
	}
	d.inflight[nodeUUID] = struct{}{}
	d.last[nodeUUID] = now
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			delete(d.inflight, nodeUUID)
			d.mu.Unlock()
		}()
		fn(nodeUUID)
	}()
	return true
}
