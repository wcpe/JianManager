package service

import (
	"context"
	"log/slog"
	"sync"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// InstanceEvent 实例事件（前端消费）。
type InstanceEvent struct {
	InstanceUUID string `json:"instanceUuid"`
	Type         string `json:"type"`
	Data         string `json:"data"`
	Timestamp    int64  `json:"timestamp"`
}

// EventService 订阅所有 Worker 的实例事件流并扇出给前端 SSE 客户端。
//
// 历史（2026-10-29）：这里曾有一个可选的 `logSink`，把实例 stdout/stderr
// 同步写进 CP 本地 `logs` 表。删掉它的原因与删表同源：实例日志的权威副本
// 在 Worker 的 VL 里，再往 CP 写一份只是重复存储 + 让业务库背日志量。
// 实例日志现在由 Worker 采集后经 /logs/federation 联邦查询。
type EventService struct {
	pool   *cpgrpc.ClientPool
	mu     sync.RWMutex
	subs   []chan InstanceEvent
	ctx    context.Context
	cancel context.CancelFunc
	// streamMu/streaming 保证每节点只起一条事件流订阅（F4）：重复的 StartWorkerStream
	// 会建立重复长驻订阅，导致 stdout/stderr 双写、事件重复扇出。订阅 goroutine 退出时清除标记，
	// 使节点真正重连后仍能重建订阅。
	streamMu  sync.Mutex
	streaming map[string]struct{}
}

// NewEventService 创建事件服务。
func NewEventService(pool *cpgrpc.ClientPool) *EventService {
	ctx, cancel := context.WithCancel(context.Background())
	return &EventService{pool: pool, ctx: ctx, cancel: cancel}
}

// Subscribe 订阅实例事件，返回只读 channel。取消时调用返回的函数。
func (es *EventService) Subscribe() (<-chan InstanceEvent, func()) {
	ch := make(chan InstanceEvent, 128)
	es.mu.Lock()
	es.subs = append(es.subs, ch)
	es.mu.Unlock()

	unsub := func() {
		es.mu.Lock()
		// 只有「本次真的从 subs 里摘除了」才 close：unsub 可能被多次调用（例如 Stop 与
		// defer 双路径），而对已关闭 channel 再次 close 会 panic —— 2026-09-28 生产实测
		// 每次 CP 停止都 panic: close of closed channel（event.go:68 → alert_triggers.go:52
		// → main.main），导致优雅关闭的后续步骤被跳过。
		found := false
		for i, sub := range es.subs {
			if sub == ch {
				es.subs = append(es.subs[:i], es.subs[i+1:]...)
				found = true
				break
			}
		}
		es.mu.Unlock()
		if found {
			close(ch)
		}
	}

	return ch, unsub
}

// StartWorkerStream 启动到指定 Worker 的事件流订阅（每节点单飞F4）。
// nodeUUID 为空时自动订阅所有已连接节点。
func (es *EventService) StartWorkerStream(nodeUUID string) {
	client, ok := es.pool.Get(nodeUUID)
	if !ok {
		slog.Warn("EventService: 无法获取 Worker 客户端", "nodeUUID", nodeUUID)
		return
	}

	// 每节点单飞：已在订阅则忽略重复触发（瞬时 active=2 的 over-trigger 不得起重复长驻流）。
	es.streamMu.Lock()
	if es.streaming == nil {
		es.streaming = make(map[string]struct{})
	}
	if _, busy := es.streaming[nodeUUID]; busy {
		es.streamMu.Unlock()
		slog.Debug("EventService: 事件流已订阅，忽略重复触发", "nodeUUID", nodeUUID)
		return
	}
	es.streaming[nodeUUID] = struct{}{}
	es.streamMu.Unlock()

	go func() {
		defer func() {
			es.streamMu.Lock()
			delete(es.streaming, nodeUUID)
			es.streamMu.Unlock()
		}()
		es.streamFromWorker(nodeUUID, client)
	}()
}

// streamFromWorker 从单个 Worker 拉取事件并扇出。
func (es *EventService) streamFromWorker(nodeUUID string, client *cpgrpc.Client) {
	slog.Info("EventService: 开始订阅 Worker 事件流", "nodeUUID", nodeUUID)

	for {
		select {
		case <-es.ctx.Done():
			return
		default:
		}

		stream, err := client.Worker.StreamInstanceEvents(es.ctx, &workerpb.StreamInstanceEventsRequest{})
		if err != nil {
			slog.Warn("EventService: 订阅 Worker 事件流失败", "nodeUUID", nodeUUID, "err", err)
			return
		}

		for {
			evt, err := stream.Recv()
			if err != nil {
				slog.Info("EventService: Worker 事件流断开", "nodeUUID", nodeUUID, "err", err)
				break
			}

			// 实例事件仅经 SSE 扇出给前端（实例日志由 Worker 采集、不过 CP）。
			event := InstanceEvent{
				InstanceUUID: evt.InstanceUuid,
				Type:         evt.Type,
				Data:         evt.Data,
				Timestamp:    evt.Timestamp,
			}

			es.broadcast(event)
		}
	}
}

// broadcast 扇出事件给所有订阅者。
func (es *EventService) broadcast(evt InstanceEvent) {
	es.mu.RLock()
	defer es.mu.RUnlock()

	for _, ch := range es.subs {
		select {
		case ch <- evt:
		default:
			// 订阅者处理过慢，丢弃事件
		}
	}
}

// Stop 停止事件服务。
func (es *EventService) Stop() {
	es.cancel()
	es.mu.Lock()
	for _, ch := range es.subs {
		close(ch)
	}
	es.subs = nil
	es.mu.Unlock()
}
