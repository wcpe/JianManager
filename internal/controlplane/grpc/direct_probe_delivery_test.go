// FR-446（spec §2.6 超时可配）：MC 直探（SLP / Query）超时经心跳响应下发的覆盖。
package grpc_test

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// stubDirectProbeTimeouts 固定返回值的 DirectProbeTimeoutResolver 替身。
type stubDirectProbeTimeouts struct {
	slp, query time.Duration
}

func (s stubDirectProbeTimeouts) DirectProbeTimeouts() (time.Duration, time.Duration) {
	return s.slp, s.query
}

// TestHeartbeat_DeliversDirectProbeTimeouts 心跳响应携带直探超时（毫秒），Worker 据此配置采集链。
func TestHeartbeat_DeliversDirectProbeTimeouts(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	h.SetDirectProbeTimeoutResolver(stubDirectProbeTimeouts{slp: 1500 * time.Millisecond, query: 2 * time.Second})
	node := seedHeartbeatNode(t, db, model.NodeStatusOffline, time.Now().Add(-time.Minute))

	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"),
		&workerpb.HeartbeatRequest{NodeUuid: node.UUID})

	err := h.Heartbeat(stream)
	require.True(t, errors.Is(err, io.EOF))
	require.Len(t, stream.sent, 1)
	require.Equal(t, int32(1500), stream.sent[0].DirectProbeSlpTimeoutMs)
	require.Equal(t, int32(2000), stream.sent[0].DirectProbeQueryTimeoutMs)
}

// TestHeartbeat_NoDirectProbeTimeouts_WhenUnset 未注入时响应不带该字段（Worker 回退内置默认，向后兼容）。
func TestHeartbeat_NoDirectProbeTimeouts_WhenUnset(t *testing.T) {
	h, db := newHeartbeatHandler(t)
	node := seedHeartbeatNode(t, db, model.NodeStatusOffline, time.Now().Add(-time.Minute))

	stream := newHeartbeatTestStream(ctxWithHeartbeatSecret("node-secret-ok"),
		&workerpb.HeartbeatRequest{NodeUuid: node.UUID})

	err := h.Heartbeat(stream)
	require.True(t, errors.Is(err, io.EOF))
	require.Len(t, stream.sent, 1)
	require.Zero(t, stream.sent[0].DirectProbeSlpTimeoutMs)
	require.Zero(t, stream.sent[0].DirectProbeQueryTimeoutMs)
}
