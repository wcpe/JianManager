package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_InitializeAndToolsList(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("tools/list 不应打 CP")
	})
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n")
	var out bytes.Buffer
	srv := NewServer(c, in, &out)
	srv.logErr = io.Discard
	require.NoError(t, srv.Run(context.Background()))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2) // initialize + tools/list（notification 无响应）

	var initResp rpcResponse
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &initResp))
	assert.Equal(t, "2.0", initResp.JSONRPC)
	assert.Nil(t, initResp.Error)
	resMap, ok := initResp.Result.(map[string]any)
	// Result 经 json 再解时是 map；此处是直接 marshal 的 struct 路径，Result 是 map[string]any
	if !ok {
		// writeResponse 把 Result 原样 marshal，反序列化后为 map
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &initResp))
	}
	_ = resMap

	var listResp struct {
		Result struct {
			Tools []ToolDef `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &listResp))
	assert.Len(t, listResp.Result.Tools, 10)
}

func TestServer_ToolsCall_SuccessAnd403(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/agent/whoami":
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "agent", "name": "x"})
		case strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "FORBIDDEN", "message": "写白名单/scope 不足或硬拒绝",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"agent_whoami","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"instance_start","arguments":{"id":1}}}`,
	}, "\n") + "\n")
	var out bytes.Buffer
	srv := NewServer(c, in, &out)
	srv.logErr = io.Discard
	require.NoError(t, srv.Run(context.Background()))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 2)

	var okResp struct {
		Result ToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &okResp))
	assert.False(t, okResp.Result.IsError)
	assert.Contains(t, okResp.Result.Content[0].Text, "agent")

	var errResp struct {
		Result ToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &errResp))
	assert.True(t, errResp.Result.IsError)
	assert.Equal(t, "写白名单/scope 不足或硬拒绝", errResp.Result.Content[0].Text)
}

func TestServer_MethodNotFound(t *testing.T) {
	c, _ := mockCP(t, func(w http.ResponseWriter, r *http.Request) {})
	in := strings.NewReader(`{"jsonrpc":"2.0","id":99,"method":"resources/list"}` + "\n")
	var out bytes.Buffer
	srv := NewServer(c, in, &out)
	srv.logErr = io.Discard
	require.NoError(t, srv.Run(context.Background()))

	var resp rpcResponse
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp))
	require.NotNil(t, resp.Error)
	assert.Equal(t, -32601, resp.Error.Code)
}
