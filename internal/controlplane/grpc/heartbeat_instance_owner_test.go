package grpc

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestHeartbeat_RejectsForeignInstancePayload 已认证节点不能改写别的节点的实例。
//
// 心跳鉴权只绑定节点身份，载荷里的实例 UUID 必须再按 instances.node_id 收敛；
// 否则一个持有合法凭据的节点上报别人节点的实例 UUID，就能改写其状态/健康原因。
func TestHeartbeat_RejectsForeignInstancePayload(t *testing.T) {
	h, db, nodeA := newDriftFixture(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	nodeB := &model.Node{UUID: "node-b", Name: "B", Secret: "test-secret-other-node", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(nodeB).Error)

	foreign := &model.Instance{UUID: "inst-b", NodeID: nodeB.ID, Name: "instance B", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(foreign).Error)
	owned := &model.Instance{UUID: "inst-a", NodeID: nodeA.ID, Name: "instance A", Status: model.InstanceStatusStopped}
	require.NoError(t, db.Create(owned).Error)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{nodeSecretHeader: nodeA.Secret}))
	stream := &driftTestStream{ctx: ctx, reqs: []*workerpb.HeartbeatRequest{{
		NodeUuid: nodeA.UUID,
		Instances: []*workerpb.InstanceState{
			{InstanceUuid: foreign.UUID, State: string(model.InstanceStatusRunning), Health: "suspected", StatusReason: "reported by node A"},
			{InstanceUuid: owned.UUID, State: string(model.InstanceStatusRunning), Health: "healthy"},
		},
		InstanceMetrics: []*workerpb.InstanceMetricSample{{InstanceUuid: foreign.UUID, ProbeAvailable: true, Tps: 999}},
	}}}
	require.True(t, errors.Is(h.Heartbeat(stream), io.EOF))

	var gotForeign model.Instance
	require.NoError(t, db.First(&gotForeign, foreign.ID).Error)
	require.Equal(t, model.InstanceStatusStopped, gotForeign.Status, "其他节点的实例状态不得被改写")
	require.Empty(t, gotForeign.StatusReason)

	var gotOwned model.Instance
	require.NoError(t, db.First(&gotOwned, owned.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, gotOwned.Status, "属于本节点的实例照常同步")
}

// TestHeartbeat_OwnershipQueryFailureDoesNotStopInstances 归属查询失败时只跳过本拍实例处理，
// 不得把本节点运行实例当成“Worker 上报空清单”而置为 STOPPED。
func TestHeartbeat_OwnershipQueryFailureDoesNotStopInstances(t *testing.T) {
	h, db, node := newDriftFixture(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	running := &model.Instance{UUID: "inst-run", NodeID: node.ID, Name: "run", Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(running).Error)

	// 模拟归属查询失败：把 instances 表改名，使 SELECT 出错（心跳本身仍能认证通过）。
	require.NoError(t, db.Exec("ALTER TABLE instances RENAME TO instances_tmp").Error)
	// 直接验证过滤器的 fail-closed 信号：失败时 ownerChecked=false（调用方据此跳过状态同步）。
	_, checked := h.filterForeignInstances(&workerpb.HeartbeatRequest{
		NodeUuid:  node.UUID,
		Instances: []*workerpb.InstanceState{{InstanceUuid: running.UUID}},
	})
	require.False(t, checked, "归属查询失败必须返回 ownerChecked=false")

	require.NoError(t, db.Exec("ALTER TABLE instances_tmp RENAME TO instances").Error)
	var got model.Instance
	require.NoError(t, db.First(&got, running.ID).Error)
	require.Equal(t, model.InstanceStatusRunning, got.Status, "校验失败不得改写实例状态")
}
