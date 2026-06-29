package service

import (
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newResyncTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}, &model.NodeJDK{}))
	return db
}

// TestBuildCreateInstanceRequest 验证实例模型→Worker 规格的翻译（ADR-050 重推与单实例补注册共用）：
// EnvVars JSON 解出、绑定 JDK 路径解析、按角色派生优雅停止命令、基础字段透传。
func TestBuildCreateInstanceRequest(t *testing.T) {
	db := newResyncTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	t.Cleanup(svc.Shutdown)

	jdk := &model.NodeJDK{NodeID: 1, MajorVersion: 21, Path: "/opt/jdks/temurin-21"}
	require.NoError(t, db.Create(jdk).Error)

	env := map[string]string{"FOO": "bar"}
	raw, _ := json.Marshal(env)
	inst := &model.Instance{
		NodeID: 1, Name: "lobby", Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleProxy, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "java -jar proxy.jar", JDKID: jdk.ID, WorkDir: "var/servers/lobby-ab12",
		EnvVars: string(raw), ProbePort: 29940, Status: model.InstanceStatusStopped,
	}
	require.NoError(t, db.Create(inst).Error)

	spec, err := svc.buildCreateInstanceRequest(inst)
	require.NoError(t, err)
	assert.Equal(t, inst.UUID, spec.InstanceUuid)
	assert.Equal(t, "lobby", spec.Name)
	assert.Equal(t, "daemon", spec.ProcessType)
	assert.Equal(t, "java -jar proxy.jar", spec.StartCommand)
	assert.Equal(t, "var/servers/lobby-ab12", spec.WorkDir)
	assert.Equal(t, "/opt/jdks/temurin-21", spec.JdkPath, "应解析绑定 JDK 安装路径")
	assert.Equal(t, "end", spec.StopCommand, "代理角色派生优雅停止命令 end")
	assert.Equal(t, int32(29940), spec.ProbePort)
	assert.Equal(t, "bar", spec.EnvVars["FOO"], "EnvVars JSON 应解出")
}

// TestResyncNode_GracefulPreflight 验证 ResyncNode 的前置容错（无 panic、无副作用）：
// 节点不存在 / 节点无实例 / 节点未连接（pool 取不到 client）均安全 no-op。
// 真实重推（需活的 Worker gRPC client）在 worker 侧 grpc 包以 ResyncInstances handler 测试覆盖。
func TestResyncNode_GracefulPreflight(t *testing.T) {
	db := newResyncTestDB(t)
	pool := cpgrpc.NewClientPool() // 空池：任何节点都「未连接」
	svc := NewInstanceService(db, nil, pool)
	t.Cleanup(svc.Shutdown)

	t.Run("节点不存在安全返回", func(t *testing.T) {
		assert.NotPanics(t, func() { svc.ResyncNode("no-such-node-uuid") })
	})

	t.Run("节点无实例安全返回", func(t *testing.T) {
		node := &model.Node{Name: "empty-node", Host: "10.0.0.9", Secret: "s", Status: model.NodeStatusOnline}
		require.NoError(t, db.Create(node).Error)
		assert.NotPanics(t, func() { svc.ResyncNode(node.UUID) })
	})

	t.Run("节点有实例但未连接安全返回", func(t *testing.T) {
		node := &model.Node{Name: "disconnected", Host: "10.0.0.10", Secret: "s", Status: model.NodeStatusOnline}
		require.NoError(t, db.Create(node).Error)
		inst := &model.Instance{
			NodeID: node.ID, Name: "srv", Type: model.InstanceTypeMinecraftJava,
			Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
			StartCommand: "x", WorkDir: "var/servers/srv-1", Status: model.InstanceStatusStopped,
		}
		require.NoError(t, db.Create(inst).Error)
		// 池中无该节点 client → 重推前置即返回，不 panic、不报错。
		assert.NotPanics(t, func() { svc.ResyncNode(node.UUID) })
	})
}
