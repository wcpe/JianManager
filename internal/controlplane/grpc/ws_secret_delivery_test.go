// FR-275（见 ADR-061）：WS 令牌密钥经注册响应（首注册/重注册）与心跳响应下发的覆盖。
package grpc_test

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestRegister_DeliversWSTokenSecret_NewNode 首注册（token 准入新节点）响应携带 WS 令牌密钥：
// 一键安装的新节点开箱终端/插件桥可用。
func TestRegister_DeliversWSTokenSecret_NewNode(t *testing.T) {
	h, _, enrollSvc := newIdentityRegisterHandler(t)
	h.SetWSTokenSecret("cp-ws-secret")

	_, plaintext, err := enrollSvc.Issue("", 30, 1)
	require.NoError(t, err)

	resp, err := h.Register(ctxWithEnrollToken(plaintext), regReqHost("edge-new", "192.168.1.50"))
	require.NoError(t, err)
	require.Equal(t, "cp-ws-secret", resp.WsTokenSecret)
}

// TestRegister_DeliversWSTokenSecret_UUIDReregister 重注册（UUID 身份证明）响应携带 WS 令牌密钥：
// 存量节点升级/重启即拿到密钥（存量部署迁移主路径），无需人工同步。
func TestRegister_DeliversWSTokenSecret_UUIDReregister(t *testing.T) {
	h, db, _ := newIdentityRegisterHandler(t)
	h.SetWSTokenSecret("cp-ws-secret")
	uuid := seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	resp, err := h.Register(ctxWithIdentity(uuid, "secret-a"), regReqHost("edge-a", "10.0.0.1"))
	require.NoError(t, err)
	require.Equal(t, "cp-ws-secret", resp.WsTokenSecret)
}

// TestRegister_DeliversWSTokenSecret_SameHostLegacy 过渡兼容路径（同机 host 命中重注册）同样下发。
func TestRegister_DeliversWSTokenSecret_SameHostLegacy(t *testing.T) {
	h, db, _ := newIdentityRegisterHandler(t)
	h.SetWSTokenSecret("cp-ws-secret")
	seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	resp, err := h.Register(context.Background(), regReqHost("edge-a", "10.0.0.1"))
	require.NoError(t, err)
	require.Equal(t, "cp-ws-secret", resp.WsTokenSecret)
}

// TestRegister_NoWSTokenSecret_WhenUnset 未注入（零值）时注册响应不携带：
// 既有测试装配/旧行为零变化（向后兼容锚点）。
func TestRegister_NoWSTokenSecret_WhenUnset(t *testing.T) {
	h, db, enrollSvc := newIdentityRegisterHandler(t)
	uuid := seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	resp, err := h.Register(ctxWithIdentity(uuid, "secret-a"), regReqHost("edge-a", "10.0.0.1"))
	require.NoError(t, err)
	require.Empty(t, resp.WsTokenSecret)

	_, plaintext, err := enrollSvc.Issue("", 30, 1)
	require.NoError(t, err)
	resp, err = h.Register(ctxWithEnrollToken(plaintext), regReqHost("edge-b", "192.168.1.50"))
	require.NoError(t, err)
	require.Empty(t, resp.WsTokenSecret)
}

// TestHeartbeat_DeliversWSTokenSecret 心跳响应每拍携带 WS 令牌密钥（镜像 FR-185 代理下发）：
// CP 轮换密钥后 Worker 不重启即自愈的通道。
func TestHeartbeat_DeliversWSTokenSecret(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	h.SetWSTokenSecret("cp-ws-secret")
	node := seedHeartbeatNode(t, db, model.NodeStatusOffline, time.Now().Add(-time.Minute))

	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"),
		&workerpb.HeartbeatRequest{NodeUuid: node.UUID})

	err := h.Heartbeat(stream)
	require.True(t, errors.Is(err, io.EOF))
	require.Len(t, stream.sent, 1)
	require.Equal(t, "cp-ws-secret", stream.sent[0].WsTokenSecret)
}

// TestHeartbeat_NoWSTokenSecret_WhenStreamUnauthenticated 未出示 node-secret 的心跳流
//（FR-004 旧版兼容路径）不下发 WS 令牌密钥：该路径跳过鉴权，任何能连 CP gRPC 端口的调用方
// 都能开流，无门槛下发等于把密钥送给陌生人（可据此伪造终端写令牌）。密钥仅对已鉴权
//（首拍 node_secret 校验通过）的流下发；新版 Worker 心跳恒带 node-secret，功能不受影响。
func TestHeartbeat_NoWSTokenSecret_WhenStreamUnauthenticated(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	h.SetWSTokenSecret("cp-ws-secret")
	node := seedHeartbeatNode(t, db, model.NodeStatusOffline, time.Now().Add(-time.Minute))

	// 不带 node-secret metadata 的流（匿名/旧版）：仍能心跳（向后兼容），但响应不携带密钥。
	stream := newHeartbeatTestStream(context.Background(), &workerpb.HeartbeatRequest{NodeUuid: node.UUID})

	err := h.Heartbeat(stream)
	require.True(t, errors.Is(err, io.EOF))
	require.Len(t, stream.sent, 1)
	require.Empty(t, stream.sent[0].WsTokenSecret)
}

// TestHeartbeat_NoWSTokenSecret_WhenUnset 未注入时心跳响应不携带（旧行为零变化）。
func TestHeartbeat_NoWSTokenSecret_WhenUnset(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	node := seedHeartbeatNode(t, db, model.NodeStatusOffline, time.Now().Add(-time.Minute))

	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"),
		&workerpb.HeartbeatRequest{NodeUuid: node.UUID})

	err := h.Heartbeat(stream)
	require.True(t, errors.Is(err, io.EOF))
	require.Len(t, stream.sent, 1)
	require.Empty(t, stream.sent[0].WsTokenSecret)
}
