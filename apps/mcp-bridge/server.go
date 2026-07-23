package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// MCP 协议常量（子集，stdio + JSON-RPC 2.0 newline framing）。
const (
	jsonRPCVersion = "2.0"
	serverName     = "jianmanager-mcp-bridge"
	serverVersion  = "0.1.0"
	protocolVer    = "2024-11-05"
)

// Server stdio MCP server。
type Server struct {
	client *AgentClient
	in     io.Reader
	out    io.Writer
	// logErr 诊断日志写到 stderr（不得污染 stdout 的 JSON-RPC 流）。
	logErr io.Writer
}

// NewServer 创建 server；in/out 为 nil 时使用 os.Stdin/Stdout。
func NewServer(client *AgentClient, in io.Reader, out io.Writer) *Server {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	return &Server{client: client, in: in, out: out, logErr: os.Stderr}
}

// rpcRequest JSON-RPC 2.0 请求。
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcResponse JSON-RPC 2.0 响应。
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Run 读取 stdin 行，处理直到 EOF。
func (s *Server) Run(ctx context.Context) error {
	sc := bufio.NewScanner(s.in)
	// 允许较大的单行 JSON（工具结果可能较长）。
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 4<<20)

	for sc.Scan() {
		line := sc.Bytes()
		if len(bytesTrimSpace(line)) == 0 {
			continue
		}
		if err := s.handleLine(ctx, line); err != nil {
			fmt.Fprintf(s.logErr, "mcp-bridge: 处理消息失败: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("读取 stdin 失败: %w", err)
	}
	return nil
}

func bytesTrimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 {
		c := b[len(b)-1]
		if c == ' ' || c == '\t' || c == '\r' {
			b = b[:len(b)-1]
			continue
		}
		break
	}
	return b
}

func (s *Server) handleLine(ctx context.Context, line []byte) error {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		// 无法解析时若无 id 则忽略；有可能是损坏帧。
		return s.writeResponse(rpcResponse{
			JSONRPC: jsonRPCVersion,
			Error:   &rpcError{Code: -32700, Message: "Parse error"},
		})
	}
	if req.JSONRPC != "" && req.JSONRPC != jsonRPCVersion {
		return s.replyError(req.ID, -32600, "Invalid Request: jsonrpc 须为 2.0")
	}

	// 通知（无 id）不回响应。
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		return s.writeResponse(rpcResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": protocolVer,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    serverName,
					"version": serverVersion,
				},
			},
		})
	case "notifications/initialized", "initialized":
		// 客户端就绪通知，无需回复。
		return nil
	case "ping":
		if isNotification {
			return nil
		}
		return s.writeResponse(rpcResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result:  map[string]any{},
		})
	case "tools/list":
		if isNotification {
			return nil
		}
		return s.writeResponse(rpcResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result: map[string]any{
				"tools": RegisteredTools(),
			},
		})
	case "tools/call":
		if isNotification {
			return nil
		}
		return s.handleToolsCall(ctx, req)
	case "shutdown":
		if isNotification {
			return nil
		}
		return s.writeResponse(rpcResponse{
			JSONRPC: jsonRPCVersion,
			ID:      req.ID,
			Result:  map[string]any{},
		})
	default:
		if isNotification {
			return nil
		}
		return s.replyError(req.ID, -32601, "Method not found: "+req.Method)
	}
}

func (s *Server) handleToolsCall(ctx context.Context, req rpcRequest) error {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.replyError(req.ID, -32602, "Invalid params: "+err.Error())
		}
	}
	if params.Name == "" {
		return s.replyError(req.ID, -32602, "Invalid params: 缺少 name")
	}
	if params.Arguments == nil {
		params.Arguments = map[string]any{}
	}

	result := CallTool(ctx, s.client, params.Name, params.Arguments)
	return s.writeResponse(rpcResponse{
		JSONRPC: jsonRPCVersion,
		ID:      req.ID,
		Result:  result,
	})
}

func (s *Server) replyError(id json.RawMessage, code int, msg string) error {
	return s.writeResponse(rpcResponse{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	})
}

func (s *Server) writeResponse(resp rpcResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.out.Write(data)
	return err
}
