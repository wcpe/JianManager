package grpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/wcpe/JianManager/internal/worker/ws"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 插件桥 gRPC 侧（ServerProbe 反向 WS ↔ Worker见 ADR-016）。
//
// 数据流：探针 →(WS) PluginBridgeServer →(SetPluginBridge 注入的事件回调) EmitPluginEvent
//        → 扇出到所有 StreamPluginEvents 订阅者（CP）。
// 指令下行：CP →(SendPluginCommand) Worker → bridge.SendCommand → 探针。
// 地基仅真实承载 connected/disconnected/heartbeat 与通道层；
// 业务事件/治理执行语义留067。

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
			Domain:       e.Domain,   // JBIS 业务域透传给 CP
			DedupKey:     e.DedupKey, // JBIS 去重键透传给 CP
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

// StreamPluginEvents 订阅某实例（或全部）探针经反向 WS 上报的事件流。
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
			// 出站前清洗字符串字段：一条含非法 UTF-8 的事件**不能**打断整条流。
			//
			// ## 为什么必须在这里兜底（这个缺陷从 2026-09-30 一直存在）
			//
			// proto3 的 `string` 在序列化时校验 UTF-8，gRPC-go 直接返回
			// `string field contains invalid UTF-8`。`stream.Send` 一报错就 `return err`，
			// 于是**整条 StreamPluginEvents 断开**，CP 每 ~10 秒重连一次，
			// 累计刷了 6 万余条日志；连带 `QueryBusinessStatus` 这类经同一 gRPC 通道的
			// 查询也一并失败（CP 侧表现为「域状态查询调用失败」）。
			//
			// 脏数据的来源是 `RawJson` 等透传字段：探针把插件上报的原始帧原样透传，
			// 若插件塞进了非 UTF-8 字节（例如按 GBK 编码的玩家聊天/日志文本），
			// 到这里就是非法序列。**一条脏数据不该让整条观测通道瘫痪**。
			//
			// 清洗不丢语义：非法字节替换为 U+FFFD（与「不可显示字符」的语义一致），
			// 其余字节原样保留。同时记 WARN 指明是哪个字段，便于从源头修。
			if err := stream.Send(preparePluginEventForSend(evt)); err != nil {
				return err
			}
		}
	}
}

// preparePluginEventForSend 是**发送路径上**唯一的出站前处理：
// 就地清洗非法 UTF-8 并返回同一个事件对象。
//
// 单独成函数而不是把逻辑写在 `stream.Send` 前面，是为了让测试能锁住
// 「发送路径真的调了清洗」。只测 `sanitizePluginEvent` 本身是不够的——
// 把调用点删掉测试照样通过（那正是这次修 bug 时踩到的坑）。
func preparePluginEventForSend(evt *workerpb.PluginEvent) *workerpb.PluginEvent {
	sanitizePluginEvent(evt)
	return evt
}

// sanitizePluginEvent 就地清除 proto string 字段里的非法 UTF-8（替换为 U+FFFD）。
//
// 只对**真的**含非法序列的字段动手：合法字符串原样保留（`utf8.ValidString` 是 O(n)
// 但全为合法 ASCII 时开销可忽略，且这条路径本来就要序列化一次）。
//
// 记 WARN 时只说字段名与长度，不打印原文内容——原文是非法字节，直接打会污染日志；
// 后缀会带一段**已清洗**的前缀，便于定位是哪个插件塞进来的。
func sanitizePluginEvent(evt *workerpb.PluginEvent) {
	if evt == nil {
		return
	}
	fix := func(name string, s string) string {
		if utf8.ValidString(s) {
			return s
		}
		sanitized := strings.ToValidUTF8(s, "\uFFFD")
		slog.Warn("插件事件含非法 UTF-8，已在出站前清洗",
			"field", name,
			"instanceUuid", evt.InstanceUuid,
			"type", evt.Type,
			"rawLen", len(s),
			// 保留前缀便于定位来源（已清洗，可安全打印）
			"prefix", truncateForLog(sanitized, 80),
		)
		return sanitized
	}
	evt.InstanceUuid = fix("instance_uuid", evt.InstanceUuid)
	evt.Type = fix("type", evt.Type)
	evt.Platform = fix("platform", evt.Platform)
	evt.Version = fix("version", evt.Version)
	evt.PlayerName = fix("player_name", evt.PlayerName)
	evt.PlayerUuid = fix("player_uuid", evt.PlayerUuid)
	evt.Message = fix("message", evt.Message)
	evt.Server = fix("server", evt.Server)
	evt.FromServer = fix("from_server", evt.FromServer)
	evt.ToServer = fix("to_server", evt.ToServer)
	evt.RawJson = fix("raw_json", evt.RawJson)
	evt.Domain = fix("domain", evt.Domain)
	evt.DedupKey = fix("dedup_key", evt.DedupKey)
}

// truncateForLog 截断字符串用于日志（按 rune 边界，避免切断多字节字符）。
func truncateForLog(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

// pluginCommandTimeout 是治理同步往返（wait=true）等待探针 command_result 的上限。
// 探针与 Worker 同机回环、命令为本地 Bukkit API 调用，5s 足够；放大会拖慢跨多后端聚合。
const pluginCommandTimeout = 5 * time.Second

// SendPluginCommand CP 经 Worker 向探针下发治理/查询指令（见 ADR-016）。
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

// pluginActionQueryState 是向探针请求全量服务器状态的治理动作（见 ADR-016）。
// 探针侧 BukkitBridgeCommandHandler 收到该 action 时采集全状态并把 JSON 放入 command_result.output。
const pluginActionQueryState = "query_state"

// QueryServerState 经反向 WS 桥按需向探针请求子服全量状态并取回 state_json。
//
// 探针在 Bukkit 子服内异步、非侵入地采集 server/worlds/JVM/class 加载器/调度器/监听器等内部数据，
// 经 query_state 指令的 command_result 回执（output 字段）把整段状态 JSON 带回。Worker 仅做透传：
// 把 output 原样填入 state_json，不解析（探针字段演进不需改 Worker/CP，前端按结构渲染）。
//
// 降级（绝不 5xx，沿用「探针只读优先、绝不成为事故源」）：
//   - 本节点未启用插件桥 → success=false + error。
//   - 探针未连入 / 刚断 → success=true, connected=false, state_json=""（前端提示部署/连接探针）。
//   - 探针在但采集超时 → success=true, connected=true, state_json="" + error（前端提示重试）。
func (s *Server) QueryServerState(_ context.Context, req *workerpb.QueryServerStateRequest) (*workerpb.QueryServerStateResponse, error) {
	if s.bridge == nil {
		return &workerpb.QueryServerStateResponse{Success: false, Error: "本节点未启用插件桥", Connected: false}, nil
	}
	if !s.bridge.IsConnected(req.InstanceUuid) {
		// 探针未连入：连接状态降级，不下发指令（避免无谓等待到超时）。
		return &workerpb.QueryServerStateResponse{Success: true, Connected: false}, nil
	}

	requestID := uuid.NewString()
	frame := pluginCommandFrame(&workerpb.PluginCommand{Action: pluginActionQueryState, RequestId: requestID})
	res, err := s.bridge.SendCommandAndWait(req.InstanceUuid, requestID, frame, pluginCommandTimeout)
	if err != nil {
		// 刚断开：连接状态降级。
		if errors.Is(err, ws.ErrBridgeNotConnected) {
			return &workerpb.QueryServerStateResponse{Success: true, Connected: false}, nil
		}
		// 采集超时或写出错：探针在线但本次状态不可得，降级为 N/A（state_json 空 + error）。
		return &workerpb.QueryServerStateResponse{Success: true, Connected: true, Error: err.Error()}, nil
	}
	return &workerpb.QueryServerStateResponse{
		Success:   res.Success,
		Error:     res.Error,
		Connected: true,
		StateJson: res.Output,
	}, nil
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
	// JBIS 业务命令（ADR-027）：domain 非空即业务帧，探针 BusinessHost 按 domain 路由到 Provider；
	// payloadJson 承载结构化业务参数。仅非空时附带，治理命令帧保持原样不变。
	if cmd.Domain != "" {
		frame["domain"] = cmd.Domain
	}
	if cmd.PayloadJson != "" {
		frame["payloadJson"] = cmd.PayloadJson
	}
	return frame
}
