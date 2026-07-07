package service

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestFR037OpsConsoleReusesNodeInstanceAndTerminalServices(t *testing.T) {
	db := newFR037OpsConsoleDB(t)
	nodeSvc := NewNodeService(db)
	instanceSvc := NewInstanceService(db, nil, nil)
	t.Cleanup(instanceSvc.Shutdown)
	terminalSvc := NewTerminalService(db, "fr037-terminal-secret", "ws://fallback.invalid")

	alpha := createFR037ServiceNode(t, db, "fr037-alpha")
	beta := createFR037ServiceNode(t, db, "fr037-beta")
	running := createFR037ServiceInstance(t, db, alpha.ID, "fr037-survival", model.InstanceStatusRunning)
	createFR037ServiceInstance(t, db, alpha.ID, "fr037-lobby", model.InstanceStatusStopped)
	createFR037ServiceInstance(t, db, beta.ID, "fr037-creative", model.InstanceStatusCrashed)

	nodes, err := nodeSvc.List()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"fr037-alpha", "fr037-beta"}, []string{nodes[0].Name, nodes[1].Name})

	allInstances, err := instanceSvc.List(InstanceFilter{})
	require.NoError(t, err)
	assert.Len(t, allInstances, 3)

	betaInstances, err := instanceSvc.List(InstanceFilter{NodeID: &beta.ID})
	require.NoError(t, err)
	require.Len(t, betaInstances, 1)
	assert.Equal(t, "fr037-creative", betaInstances[0].Name)
	assert.Equal(t, beta.ID, betaInstances[0].NodeID)

	token, err := terminalSvc.IssueToken(running.ID, "write", "panel.example.test", true)
	require.NoError(t, err)
	assert.Equal(t, "wss://panel.example.test/ws/terminal", token.WSURL)
	assert.Equal(t, 30, token.ExpiresIn)

	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(token.Token, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte("fr037-terminal-secret"), nil
	})
	require.NoError(t, err)
	require.True(t, parsed.Valid)
	assert.Equal(t, running.UUID, claims["instanceId"])
	assert.Equal(t, "write", claims["permission"])
}

func newFR037OpsConsoleDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fr037-ops-console.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.Instance{}))
	return db
}

func createFR037ServiceNode(t *testing.T, db *gorm.DB, name string) model.Node {
	t.Helper()
	node := model.Node{
		UUID:          name + "-uuid",
		Name:          name,
		Host:          "127.0.0.1",
		GRPCPort:      9100,
		WSPort:        9101,
		Secret:        name + "-secret",
		Status:        model.NodeStatusOnline,
		LastHeartbeat: ptrTime(time.Now()),
	}
	require.NoError(t, db.Create(&node).Error)
	return node
}

func createFR037ServiceInstance(t *testing.T, db *gorm.DB, nodeID uint, name string, status model.InstanceStatus) model.Instance {
	t.Helper()
	inst := model.Instance{
		UUID:         name + "-uuid",
		NodeID:       nodeID,
		Name:         name,
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDaemon,
		Status:       status,
		StartCommand: "java -jar server.jar nogui",
	}
	require.NoError(t, db.Create(&inst).Error)
	return inst
}
