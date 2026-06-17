package service

import (
	"context"
	"log/slog"
	"sync"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// InstanceEvent 实例事件（前端消费）。
type InstanceEvent struct {
	InstanceUUID string `json:"instanceUuid"`
	Type         string `json:"type"`
	Data         string `json:"data"`
	Timestamp    int64  `json:"timestamp"`
}

// EventService 订阅所有 Worker 的实例事件流并扇出给前端 SSE 客户端。
type EventService struct {
	pool  *cpgrpc.ClientPool
	mu    sync.RWMutex
	subs  []chan InstanceEvent
	ctx   context.Context
	cancel context.CancelFunc
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
		for i, sub := range es.subs {
			if sub == ch {
				es.subs = append(es.subs[:i], es.subs[i+1:]...)
				break
			}
		}
		es.mu.Unlock()
		close(ch)
	}

	return ch, unsub
}

// StartWorkerStream 启动到指定 Worker 的事件流订阅。
// nodeUUID 为空时自动订阅所有已连接节点。
func (es *EventService) StartWorkerStream(nodeUUID string) {
	client, ok := es.pool.Get(nodeUUID)
	if !ok {
		slog.Warn("EventService: 无法获取 Worker 客户端", "nodeUUID", nodeUUID)
		return
	}

	go es.streamFromWorker(nodeUUID, client)
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
