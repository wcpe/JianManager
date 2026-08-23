// FR-275（见 ADR-061）：WS 令牌密钥经注册响应（首注册/重注册）与心跳响应下发的覆盖。
package grpc_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// TestRegister_MissingIdentityDoesNotDeliverWSTokenSecret 不允许 Host 伪身份路径取得 WS 密钥。
func TestRegister_MissingIdentityDoesNotDeliverWSTokenSecret(t *testing.T) {
	h, db, _ := newIdentityRegisterHandler(t)
	h.SetWSTokenSecret("cp-ws-secret")
	seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	_, err := h.Register(context.Background(), regReqHost("edge-a", "10.0.0.1"))
	require.Equal(t, codes.Unauthenticated, status.Code(err))
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

// TestHeartbeat_MissingSecretDoesNotDeliverWSTokenSecret 匿名心跳在读取请求前即被拒绝。
func TestHeartbeat_MissingSecretDoesNotDeliverWSTokenSecret(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	h.SetWSTokenSecret("cp-ws-secret")
	node := seedHeartbeatNode(t, db, model.NodeStatusOffline, time.Now().Add(-time.Minute))

	// 不带 node-secret metadata 的流（匿名/旧版）不得进入心跳处理。
	stream := newHeartbeatTestStream(context.Background(), &workerpb.HeartbeatRequest{NodeUuid: node.UUID})

	err := h.Heartbeat(stream)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Empty(t, stream.sent)
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
