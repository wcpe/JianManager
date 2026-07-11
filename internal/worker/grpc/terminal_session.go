package grpc

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// SetTerminalWSAddr 注入本机终端 WS 服务地址（如 ws://127.0.0.1:9102/ws/terminal），
// 供 TerminalSession 回环桥接（FR-281 M2，见 ADR-066）。空值表示未装配，RPC 返回 Unavailable。
func (s *Server) SetTerminalWSAddr(addr string) { s.terminalWSAddr = addr }

// TerminalSession 终端会话隧道桥（FR-281 M2，见 ADR-066）。
// 首帧必须为 open（携带 CP 签发的一次性终端令牌）；本方法在本机回环拨自身 WS 终端服务
// 并把 gRPC 流 ⇄ 本机 WS 双向原样泵帧——令牌校验与会话层的单一真源仍是 ws.TerminalServer
//（ADR-061 信任模型不变，本方法零复制校验/会话逻辑）。经反向隧道承载时 CP→Worker 终端零入站。
func (s *Server) TerminalSession(stream workerpb.WorkerService_TerminalSessionServer) error {
	if s.terminalWSAddr == "" {
		return status.Error(codes.Unavailable, "终端 WS 服务未装配")
	}

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	open := first.GetOpen()
	if open == nil || open.Token == "" {
		return status.Error(codes.InvalidArgument, "首帧必须为携带令牌的 open")
	}

	// 回环拨自身 WS 终端服务：token 原样透传，校验方仍是本机 WS 服务。
	wsConn, resp, err := websocket.DefaultDialer.Dial(
		fmt.Sprintf("%s?token=%s", s.terminalWSAddr, url.QueryEscape(open.Token)), nil)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				// 令牌被拒以 PermissionDenied 透传，让 CP 侧 FR-276 定向诊断在 gRPC 路径同样成立。
				return status.Errorf(codes.PermissionDenied, "终端令牌被拒绝（HTTP %d）", resp.StatusCode)
			}
		}
		return status.Errorf(codes.Unavailable, "回环连接终端 WS 失败: %v", err)
	}
	defer wsConn.Close()

	// 回环拨通即回「就绪 ack」（空 open）：CP 据此确定性区分「就绪 / 令牌被拒 / 老 Worker
	// Unimplemented（回退直拨 WS）」，不必等首个数据帧。
	if err := stream.Send(&workerpb.TerminalFrame{
		Kind: &workerpb.TerminalFrame_Open{Open: &workerpb.TerminalOpen{}},
	}); err != nil {
		return err
	}

	// 任一方向断开即结束会话：handler 返回后 gRPC 取消流、defer 关 WS，两个泵 goroutine 随之退出。
	done := make(chan struct{}, 2)

	// gRPC → WS（stdin/resize 等浏览器上行帧）
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			in, err := stream.Recv()
			if err != nil {
				return
			}
			f := in.GetFrame()
			if f == nil {
				continue // 重复 open / 空帧：忽略
			}
			if err := wsConn.WriteMessage(int(f.MsgType), f.Payload); err != nil {
				return
			}
		}
	}()

	// WS → gRPC（stdout/state 等下行帧）
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msgType, payload, err := wsConn.ReadMessage()
			if err != nil {
				return
			}
			if err := stream.Send(&workerpb.TerminalFrame{
				Kind: &workerpb.TerminalFrame_Frame{Frame: &workerpb.TerminalWSFrame{
					MsgType: int32(msgType),
					Payload: payload,
				}},
			}); err != nil {
				return
			}
		}
	}()

	<-done
	slog.Debug("终端会话隧道桥已关闭")
	return nil
}
