package mcp

import "encoding/json"

// MCP 协议常量（JSON-RPC 2.0 子集，对齐 2024-11-05 / Streamable HTTP）。
const (
	JSONRPCVersion   = "2.0"
	ServerName       = "jianmanager-cp-mcp"
	ServerVersion    = "0.1.0"
	ProtocolVersion  = "2024-11-05"
	HeaderSessionID  = "Mcp-Session-Id"
)

// RPCRequest JSON-RPC 2.0 请求。
type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCResponse JSON-RPC 2.0 响应。
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError JSON-RPC 错误对象。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// IsNotification 无 id 或 id=null 视为通知。
func (r *RPCRequest) IsNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

func newResult(id json.RawMessage, result any) RPCResponse {
	return RPCResponse{JSONRPC: JSONRPCVersion, ID: id, Result: result}
}

func newError(id json.RawMessage, code int, msg string) RPCResponse {
	return RPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error:   &RPCError{Code: code, Message: msg},
	}
}

func initializeResult() map[string]any {
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    ServerName,
			"version": ServerVersion,
		},
	}
}
