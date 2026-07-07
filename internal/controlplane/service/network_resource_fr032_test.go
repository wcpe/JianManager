package service

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newFR032ServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Node{},
		&model.Instance{},
		&model.GroupInstance{},
		&model.GroupQuota{},
		&model.Bot{},
		&model.Backup{},
	))
	return db
}

func TestFR032InstanceCreateAssignsRoleAndSystemWorkDir(t *testing.T) {
	db := newFR032ServiceTestDB(t)
	node := &model.Node{Name: "fr032-node", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "secret", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)

	svc := NewInstanceService(db, nil, cpgrpc.NewClientPool())
	inst, err := svc.Create(CreateInstanceRequest{
		NodeID:       node.ID,
		Name:         "Lobby One",
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "java -jar server.jar nogui",
		WorkDir:      "C:/operator/should-not-win",
		ServerPort:   25565,
		QueryPort:    25565,
		ProbePort:    29940,
	})
	require.NoError(t, err)
	require.Equal(t, model.InstanceRoleBackend, inst.Role)
	require.True(t, strings.HasPrefix(inst.WorkDir, "var/servers/lobby-one-"), "MC 实例工作目录应由系统分配到 var/servers 下，实际=%s", inst.WorkDir)
	require.NotContains(t, inst.WorkDir, "operator")

	invalidRole, err := svc.Create(CreateInstanceRequest{
		NodeID:       node.ID,
		Name:         "Invalid Role",
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRole("owner"),
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "java -jar server.jar nogui",
	})
	require.NoError(t, err)
	require.Equal(t, model.InstanceRoleUniversal, invalidRole.Role)
}
