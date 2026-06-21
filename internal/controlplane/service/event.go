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

// LogSink 接收来自 Worker 事件流的实例进程输出并落库（由 LogService 实现）。
// 以接口注入而非直接依赖 LogService，保持 EventService 对持久化的解耦（FR-049）。
type LogSink interface {
	// IngestInstanceOutput 落库一条实例输出。nodeUUID 标识来源节点，stream 为 stdout/stderr。
	IngestInstanceOutput(nodeUUID, instanceUUID, stream, message string, ts int64)
}

// EventService 订阅所有 Worker 的实例事件流并扇出给前端 SSE 客户端。
type EventService struct {
	pool  *cpgrpc.ClientPool
	mu    sync.RWMutex
	subs  []chan InstanceEvent
	ctx   context.Context
	cancel context.CancelFunc
	// logSink 可选：非 nil 时把 stdout/stderr 事件落库（FR-049）。
	logSink LogSink
}

// NewEventService 创建事件服务。
func NewEventService(pool *cpgrpc.ClientPool) *EventService {
	ctx, cancel := context.WithCancel(context.Background())
	return &EventService{pool: pool, ctx: ctx, cancel: cancel}
}

// SetLogSink 注入日志落库器，使实例 stdout/stderr 经事件流采集入库（FR-049）。
func (es *EventService) SetLogSink(sink LogSink) { es.logSink = sink }

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

			// 实例进程输出（stdout/stderr）落库供日志中心检索（FR-049）；
			// 状态变更仍只走 SSE 扇出。落库与扇出相互独立。
			if es.logSink != nil && (evt.Type == "stdout" || evt.Type == "stderr") {
				es.logSink.IngestInstanceOutput(nodeUUID, evt.InstanceUuid, evt.Type, evt.Data, evt.Timestamp)
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
