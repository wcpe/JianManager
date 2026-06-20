package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/wxys233/JianManager/proto/workerpb"
)

// pluginBridgeEvent 插件桥事件的内部表示，由 ws 层经回调注入，分发给 StreamPluginEvents 订阅者。
// 与 ws.PluginEvent 字段一致，但 grpc 层不直接引用 ws 类型，便于以接口替身做单测。
type pluginBridgeEvent struct {
	InstanceUUID string
	Type         string
	Data         string
	Timestamp    int64
}

// pluginBridgeBroker 是 grpc 层对插件桥 WS 服务器的最小依赖（指令下发 + 事件回调注入）。
// 由 ws.PluginBridgeServer 实现；以接口形式持有便于单测注入替身。参见 ADR-012。
type pluginBridgeBroker interface {
	// SendCommand 向某实例当前连入的插件下发指令；无插件连入返回 false。
	SendCommand(instanceUUID, id, action string, args json.RawMessage) bool
	// SetEventHandler 注入事件回调，把插件上报事件冒泡给 grpc 层。
	SetEventHandler(func(instanceUUID, eventType, data string, ts int64))
}

// SetPluginBridge 注入插件桥 broker，并把插件事件接到本 Server 的订阅扇出上。
// 由 Worker 主进程在启动时调用。broker 为 nil 表示不启用插件桥。
func (s *Server) SetPluginBridge(broker pluginBridgeBroker) {
	s.pluginBridge = broker
	if broker == nil {
		return
	}
	broker.SetEventHandler(func(instanceUUID, eventType, data string, ts int64) {
		evt := pluginBridgeEvent{InstanceUUID: instanceUUID, Type: eventType, Data: data, Timestamp: ts}
		s.pluginEventMu.Lock()
		subs := make([]chan pluginBridgeEvent, len(s.pluginEventSubs))
		copy(subs, s.pluginEventSubs)
		s.pluginEventMu.Unlock()
		for _, ch := range subs {
			select {
			case ch <- evt:
			default:
				// 订阅者消费太慢，丢弃事件避免阻塞插件 WS 读循环
			}
		}
	})
}

// StreamPluginEvents 订阅本节点插件桥事件流（连接/断开/玩家事件等）。
// CP 调用后 Worker 持续推送插件会话事件；instance_uuid 为空表示订阅所有实例。流关闭时自动取消订阅。
func (s *Server) StreamPluginEvents(req *workerpb.StreamPluginEventsRequest, stream workerpb.WorkerService_StreamPluginEventsServer) error {
	ch := make(chan pluginBridgeEvent, 64)
	s.pluginEventMu.Lock()
	s.pluginEventSubs = append(s.pluginEventSubs, ch)
	s.pluginEventMu.Unlock()

	defer func() {
		s.pluginEventMu.Lock()
		for i, sub := range s.pluginEventSubs {
			if sub == ch {
				s.pluginEventSubs = append(s.pluginEventSubs[:i], s.pluginEventSubs[i+1:]...)
				break
			}
		}
		s.pluginEventMu.Unlock()
		close(ch)
	}()

	slog.Info("StreamPluginEvents 订阅开始", "filter", req.InstanceUuid)

	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			if req.InstanceUuid != "" && evt.InstanceUUID != req.InstanceUuid {
				continue
			}
			if err := stream.Send(&workerpb.PluginEvent{
				InstanceUuid: evt.InstanceUUID,
				Type:         evt.Type,
				Data:         evt.Data,
				Timestamp:    evt.Timestamp,
			}); err != nil {
				return err
			}
		}
	}
}

// SendPluginCommand 经插件桥把指令下发给某实例当前连入的插件。
// 实例无插件连入或本节点未启用插件桥时返回 Success=false + 说明，不返回 gRPC error
// （与其它 *Response{Success,Error} RPC 一致，便于上层区分业务失败与传输失败）。
func (s *Server) SendPluginCommand(ctx context.Context, req *workerpb.SendPluginCommandRequest) (*workerpb.SendPluginCommandResponse, error) {
	if s.pluginBridge == nil {
		return &workerpb.SendPluginCommandResponse{Success: false, Error: "本节点未启用插件桥"}, nil
	}
	if req.Action == "" {
		return &workerpb.SendPluginCommandResponse{Success: false, Error: "缺少 action"}, nil
	}
	var args json.RawMessage
	if req.ArgsJson != "" {
		args = json.RawMessage(req.ArgsJson)
	}
	// 指令 id 用时间无关的简短随机串便于插件回执关联；这里用实例+action 足够（fire-and-forget）。
	id := fmt.Sprintf("%s:%s", req.InstanceUuid, req.Action)
	if ok := s.pluginBridge.SendCommand(req.InstanceUuid, id, req.Action, args); !ok {
		return &workerpb.SendPluginCommandResponse{Success: false, Error: "实例无插件连入"}, nil
	}
	return &workerpb.SendPluginCommandResponse{Success: true}, nil
}
