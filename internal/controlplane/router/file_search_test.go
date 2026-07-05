package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestFileSearch_MissingQuery(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	inst := &model.Instance{
		NodeID:       node.ID,
		Name:         "search-missing-query",
		Type:         model.InstanceTypeGeneric,
		Role:         model.InstanceRoleUniversal,
		ProcessType:  model.ProcessTypeDirect,
		StartCommand: "noop",
		Status:       model.InstanceStatusStopped,
		WorkDir:      "/srv/search",
	}
	require.NoError(t, db.Create(inst).Error)

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(inst.ID)+"/files/search", map[string]interface{}{"mode": "content"}, token)
	require.Equal(t, http.StatusBadRequest, w.Code)
	resp := parseJSON(t, w)
	require.Equal(t, "INVALID_REQUEST", resp["error"])
}
