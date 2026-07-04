package router

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func TestRuntimeAssetsOverview_AdminOnlyAndPayload(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	admin := getAdminToken(t, r)
	member := getMemberToken(t, r, "member", "password123")

	node := &model.Node{Name: "node-a", Host: "127.0.0.1", GRPCPort: 9101, WSPort: 9102, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk21", Managed: true}
	require.NoError(t, db.Create(jdk).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "paper-1", Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDaemon, JDKID: jdk.ID, Status: model.InstanceStatusRunning}
	require.NoError(t, db.Create(inst).Error)
	asset := &model.Asset{Type: model.AssetTypeCore, Name: "paper", SHA256: "a", Size: 1234, StorageState: model.AssetStorageHot, StorageBackend: model.AssetBackendLocal}
	require.NoError(t, db.Create(asset).Error)

	unauthorized := makeRequest(r, http.MethodGet, "/api/v1/runtime-assets/overview", nil, "")
	require.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	forbidden := makeRequest(r, http.MethodGet, "/api/v1/runtime-assets/overview", nil, member)
	require.Equal(t, http.StatusForbidden, forbidden.Code)

	ok := makeRequest(r, http.MethodGet, "/api/v1/runtime-assets/overview", nil, admin)
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())

	var overview service.RuntimeAssetsOverview
	require.NoError(t, json.Unmarshal(ok.Body.Bytes(), &overview))
	require.Equal(t, 1, overview.JDKSummary.InstanceRefs)
	require.Len(t, overview.JDKs, 1)
	require.Len(t, overview.JDKs[0].Instances, 1)
	require.Equal(t, "direct", overview.JDKs[0].Instances[0].Binding)
	require.Len(t, overview.Assets, 1)
	require.Equal(t, model.AssetTypeCore, overview.Assets[0].Type)
	require.EqualValues(t, 1234, overview.AssetSummary.TotalSize)
}
