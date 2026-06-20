package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

const pbTestSecret = "plugin-bridge-test-secret"

func newPluginBridgeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	return db
}

// seedInstance 建一个节点 + 实例，返回实例。
func seedInstance(t *testing.T, db *gorm.DB) *model.Instance {
	t.Helper()
	node := &model.Node{Name: "n1", Host: "10.0.0.5", GRPCPort: 9101, WSPort: 9102, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "lobby", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

func TestPluginBridge_IssueToken(t *testing.T) {
	db := newPluginBridgeTestDB(t)
	inst := seedInstance(t, db)
	svc := NewPluginBridgeService(db, cpgrpc.NewClientPool(), pbTestSecret)
	defer svc.Stop()

	tok, err := svc.IssueToken(inst.ID, "panel.example.com", false)
	require.NoError(t, err)
	assert.Equal(t, inst.UUID, tok.InstanceUUID)
	// wsUrl 直连实例所在节点 host + wsPort（插件与游戏服同机）
	assert.Equal(t, "ws://10.0.0.5:9102/ws/plugin-bridge", tok.WSURL)
	assert.Equal(t, int(pluginTokenTTL.Seconds()), tok.ExpiresIn)

	// 校验 claims：scope=plugin-bridge 且 instanceId 与实例一致
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(tok.Token, claims, func(*jwt.Token) (interface{}, error) {
		return []byte(pbTestSecret), nil
	})
	require.NoError(t, err)
	require.True(t, parsed.Valid)
	assert.Equal(t, pluginTokenScope, claims["scope"])
	assert.Equal(t, inst.UUID, claims["instanceId"])
}

func TestPluginBridge_IssueToken_InstanceNotFound(t *testing.T) {
	db := newPluginBridgeTestDB(t)
	svc := NewPluginBridgeService(db, cpgrpc.NewClientPool(), pbTestSecret)
	defer svc.Stop()
	_, err := svc.IssueToken(999, "", false)
	assert.ErrorIs(t, err, ErrInstanceNotFound)
}

func TestPluginBridge_SendCommand_WorkerNotConnected(t *testing.T) {
	db := newPluginBridgeTestDB(t)
	inst := seedInstance(t, db)
	// 空连接池：Worker 未连接
	svc := NewPluginBridgeService(db, cpgrpc.NewClientPool(), pbTestSecret)
	defer svc.Stop()
	err := svc.SendCommand(inst.ID, "kick", `{"player":"X"}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "未连接")
}

func TestPluginBridge_SendCommand_InstanceNotFound(t *testing.T) {
	db := newPluginBridgeTestDB(t)
	svc := NewPluginBridgeService(db, cpgrpc.NewClientPool(), pbTestSecret)
	defer svc.Stop()
	err := svc.SendCommand(404, "kick", "")
	assert.ErrorIs(t, err, ErrInstanceNotFound)
}

func TestPluginBridge_ConnectionStateLifecycle(t *testing.T) {
	db := newPluginBridgeTestDB(t)
	inst := seedInstance(t, db)
	svc := NewPluginBridgeService(db, cpgrpc.NewClientPool(), pbTestSecret)
	defer svc.Stop()

	// connected 事件 → 状态 connected，并回填实例 id/name
	svc.handleEvent("node-uuid", &workerpb.PluginEvent{InstanceUuid: inst.UUID, Type: "connected", Data: "{}", Timestamp: 100})
	conns := svc.Connections()
	require.Len(t, conns, 1)
	assert.True(t, conns[0].Connected)
	assert.Equal(t, inst.ID, conns[0].InstanceID)
	assert.Equal(t, "lobby", conns[0].InstanceName)
	assert.Equal(t, int64(100), conns[0].LastEventAt)

	// disconnected 事件 → 状态 disconnected（保留记录）
	svc.handleEvent("node-uuid", &workerpb.PluginEvent{InstanceUuid: inst.UUID, Type: "disconnected", Data: "{}", Timestamp: 200})
	conns = svc.Connections()
	require.Len(t, conns, 1)
	assert.False(t, conns[0].Connected)
	assert.Equal(t, int64(200), conns[0].LastEventAt)
}

func TestPluginBridge_SubscribeFanOut(t *testing.T) {
	db := newPluginBridgeTestDB(t)
	inst := seedInstance(t, db)
	svc := NewPluginBridgeService(db, cpgrpc.NewClientPool(), pbTestSecret)
	defer svc.Stop()

	ch, unsub := svc.Subscribe()
	defer unsub()

	svc.handleEvent("node-uuid", &workerpb.PluginEvent{
		InstanceUuid: inst.UUID, Type: "player_join", Data: `{"player":"Steve"}`, Timestamp: 300,
	})

	select {
	case evt := <-ch:
		assert.Equal(t, inst.UUID, evt.InstanceUUID)
		assert.Equal(t, "player_join", evt.Type)
		assert.JSONEq(t, `{"player":"Steve"}`, evt.Data)
		assert.Equal(t, int64(300), evt.Timestamp)
	case <-time.After(time.Second):
		t.Fatal("订阅者未收到事件")
	}
}

func TestPluginBridge_UnsubStopsDelivery(t *testing.T) {
	db := newPluginBridgeTestDB(t)
	svc := NewPluginBridgeService(db, cpgrpc.NewClientPool(), pbTestSecret)
	defer svc.Stop()

	ch, unsub := svc.Subscribe()
	unsub()
	// 取消订阅后再有事件不应阻塞（channel 已关闭，broadcast 跳过已移除订阅者）
	svc.handleEvent("node-uuid", &workerpb.PluginEvent{InstanceUuid: "x", Type: "connected", Data: "{}", Timestamp: 1})
	// 已关闭的 channel：读取应立即返回零值且 ok=false
	_, ok := <-ch
	assert.False(t, ok, "取消订阅后 channel 应已关闭")
}
