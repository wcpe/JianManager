package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestFR037OpsConsoleRoutesExposeShellData(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	alpha := createFR037RouterNode(t, db, "fr037-alpha")
	beta := createFR037RouterNode(t, db, "fr037-beta")
	alphaInstance := createFR037RouterInstance(t, db, alpha.ID, "fr037-survival", model.InstanceStatusRunning)
	createFR037RouterInstance(t, db, beta.ID, "fr037-creative", model.InstanceStatusStopped)

	nodesResp := makeRequest(r, http.MethodGet, "/api/v1/nodes", nil, token)
	require.Equal(t, http.StatusOK, nodesResp.Code)
	nodes := parseJSONArray(t, nodesResp)
	require.Len(t, nodes, 2)
	assert.Equal(t, "fr037-alpha", nodes[0].(map[string]interface{})["name"])
	assert.Equal(t, "fr037-beta", nodes[1].(map[string]interface{})["name"])

	allInstancesResp := makeRequest(r, http.MethodGet, "/api/v1/instances", nil, token)
	require.Equal(t, http.StatusOK, allInstancesResp.Code)
	assert.Len(t, parseJSONArray(t, allInstancesResp), 2)

	filteredResp := makeRequest(r, http.MethodGet, "/api/v1/instances?nodeId="+itoa(beta.ID), nil, token)
	require.Equal(t, http.StatusOK, filteredResp.Code)
	filtered := parseJSONArray(t, filteredResp)
	require.Len(t, filtered, 1)
	assert.Equal(t, "fr037-creative", filtered[0].(map[string]interface{})["name"])
	assert.Equal(t, float64(beta.ID), filtered[0].(map[string]interface{})["nodeId"])

	terminalResp := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(alphaInstance.ID)+"/terminal-token", nil, token)
	require.Equal(t, http.StatusOK, terminalResp.Code)
	terminal := parseJSON(t, terminalResp)
	assert.NotEmpty(t, terminal["token"])
	assert.Equal(t, "ws://example.com/ws/terminal", terminal["wsUrl"])
	assert.Equal(t, float64(30), terminal["expiresIn"])
}

func createFR037RouterNode(t *testing.T, db *gorm.DB, name string) model.Node {
	t.Helper()
	node := model.Node{
		UUID:     name + "-uuid",
		Name:     name,
		Host:     "127.0.0.1",
		GRPCPort: 9100,
		WSPort:   9101,
		Secret:   name + "-secret",
		Status:   model.NodeStatusOnline,
	}
	require.NoError(t, db.Create(&node).Error)
	return node
}

func createFR037RouterInstance(t *testing.T, db *gorm.DB, nodeID uint, name string, status model.InstanceStatus) model.Instance {
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
