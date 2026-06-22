package grpc

import (
	"context"
	"log/slog"
	"time"

	"github.com/wcpe/JianManager/internal/worker/ws"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 插件桥 gRPC 侧（ServerProbe 反向 WS ↔ Worker，FR-065，见 ADR-016）。
//
// 数据流：探针 →(WS) PluginBridgeServer →(SetPluginBridge 注入的事件回调) EmitPluginEvent
//        → 扇出到所有 StreamPluginEvents 订阅者（CP）。
// 指令下行：CP →(SendPluginCommand) Worker → bridge.SendCommand → 探针。
// 地基（FR-065）仅真实承载 connected/disconnected/heartbeat 与通道层；
// 业务事件/治理执行语义留 FR-066/067。

// SetPluginBridge 注入插件桥服务端，并把其冒泡的事件桥接到 StreamPluginEvents 订阅者。
// 由 Worker 主进程在启动时调用。bridge 为 nil 时插件桥相关 RPC 返回未连接/未启用。
func (s *Server) SetPluginBridge(b *ws.PluginBridgeServer) {
	s.bridge = b
	if b == nil {
		return
	}
	b.SetEventHandler(func(e ws.PluginEvent) {
		s.EmitPluginEvent(&workerpb.PluginEvent{
			InstanceUuid: e.InstanceUUID,
			Type:         e.Type,
			Timestamp:    e.Timestamp,
			Platform:     e.Platform,
			Version:      e.Version,
			PlayerName:   e.PlayerName,
			PlayerUuid:   e.PlayerUUID,
			Message:      e.Message,
			Server:       e.Server,
			FromServer:   e.FromServer,
			ToServer:     e.ToServer,
			RawJson:      e.Raw,
		})
	})
}

// EmitPluginEvent 把一条插件事件非阻塞地扇出给所有 StreamPluginEvents 订阅者。
// 订阅者消费太慢则丢弃，绝不阻塞产生方（WS 读循环），与实例事件总线同策略。
func (s *Server) EmitPluginEvent(evt *workerpb.PluginEvent) {
	s.pluginEventMu.Lock()
	subs := make([]chan *workerpb.PluginEvent, len(s.pluginEventSubs))
	copy(subs, s.pluginEventSubs)
	s.pluginEventMu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- evt:
		default:
		}
	}
}

// StreamPluginEvents 订阅某实例（或全部）探针经反向 WS 上报的事件流（FR-065）。
// CP 调用后 Worker 持续推送 connected/disconnected/heartbeat 及（下游）业务事件。
// instance_uuid 为空时订阅所有实例；流关闭时自动取消订阅。
func (s *Server) StreamPluginEvents(req *workerpb.StreamPluginEventsRequest, stream workerpb.WorkerService_StreamPluginEventsServer) error {
	ch := make(chan *workerpb.PluginEvent, 64)
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
			if req.InstanceUuid != "" && evt.InstanceUuid != req.InstanceUuid {
				continue
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		}
	}
}

// pluginCommandTimeout 是治理同步往返（wait=true）等待探针 command_result 的上限。
// 探针与 Worker 同机回环、命令为本地 Bukkit API 调用，5s 足够；放大会拖慢跨多后端聚合。
const pluginCommandTimeout = 5 * time.Second

// SendPluginCommand CP 经 Worker 向探针下发治理/查询指令（FR-067，见 ADR-016）。
//
// wait=true：同步等待探针执行回执 command_result（踢/封/解封/白名单/在线列表），把成功/输出/错误返回；
// wait=false：即发即忘（仅校验已下发）。实例无活动探针会话时返回 success=false 且 error 说明未连接，
// 让 CP 聚合多后端时不因单点失败中断、并向前端展示优雅降级（取代退役的 RCON 路径）。
func (s *Server) SendPluginCommand(_ context.Context, req *workerpb.SendPluginCommandRequest) (*workerpb.SendPluginCommandResponse, error) {
	requestID := ""
	if req.Command != nil {
		requestID = req.Command.RequestId
	}
	if s.bridge == nil {
		return &workerpb.SendPluginCommandResponse{Success: false, Error: "本节点未启用插件桥", RequestId: requestID}, nil
	}
	payload := pluginCommandFrame(req.Command)

	if !req.Wait {
		ok, err := s.bridge.SendCommand(req.InstanceUuid, payload)
		if err != nil {
			return &workerpb.SendPluginCommandResponse{Success: false, Error: err.Error(), RequestId: requestID}, nil
		}
		if !ok {
			return &workerpb.SendPluginCommandResponse{Success: false, Error: "探针未连接", RequestId: requestID}, nil
		}
		return &workerpb.SendPluginCommandResponse{Success: true, RequestId: requestID}, nil
	}

	// 同步往返：下发并等 command_result（按 request_id 关联）。
	res, err := s.bridge.SendCommandAndWait(req.InstanceUuid, requestID, payload, pluginCommandTimeout)
	if err != nil {
		// 未连接/超时：success=false + error，CP 据此优雅降级。
		return &workerpb.SendPluginCommandResponse{Success: false, Error: err.Error(), RequestId: requestID}, nil
	}
	return &workerpb.SendPluginCommandResponse{
		Success:   res.Success,
		Error:     res.Error,
		RequestId: requestID,
		Output:    res.Output,
	}, nil
}

// QueryServerState 查询子服全状态（FR-065 骨架）：地基阶段仅回报探针连接状态，
// 聚合状态（在线列表/世界/TPS）由 FR-066/067 填充 state_json。
func (s *Server) QueryServerState(_ context.Context, req *workerpb.QueryServerStateRequest) (*workerpb.QueryServerStateResponse, error) {
	if s.bridge == nil {
		return &workerpb.QueryServerStateResponse{Success: false, Error: "本节点未启用插件桥", Connected: false}, nil
	}
	connected := s.bridge.IsConnected(req.InstanceUuid)
	return &workerpb.QueryServerStateResponse{Success: true, Connected: connected}, nil
}

// pluginCommandFrame 把 proto PluginCommand 转为下发给探针的 WS 帧（type=command）。
// 字段命名与探针侧约定一致（小写下划线/驼峰由探针解析），地基阶段探针仅记录不执行。
func pluginCommandFrame(cmd *workerpb.PluginCommand) map[string]interface{} {
	frame := map[string]interface{}{"type": "command"}
	if cmd == nil {
		return frame
	}
	frame["action"] = cmd.Action
	frame["target"] = cmd.Target
	frame["reason"] = cmd.Reason
	frame["args"] = cmd.Args
	frame["requestId"] = cmd.RequestId
	return frame
}
