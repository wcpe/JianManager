package router

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type routerInstanceEnvWorker struct {
	workerpb.WorkerServiceClient
}

func (routerInstanceEnvWorker) GetInstanceEnv(_ context.Context, _ *workerpb.GetInstanceEnvRequest, _ ...grpc.CallOption) (*workerpb.GetInstanceEnvResponse, error) {
	return &workerpb.GetInstanceEnvResponse{
		Available: true,
		Env: map[string]string{
			"FOO":       "runtime",
			"JAVA_HOME": "/opt/jdk-21",
			"PATH":      "/opt/jdk-21/bin:/usr/bin",
		},
	}, nil
}

func TestInstanceEnv_ConfiguredRuntimeAndAuthorization(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupTestRouterWithPool(db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	groupID := createGroupViaAPI(t, r, adminToken, "env-readers")

	createBody := map[string]interface{}{
		"nodeId":       node.ID,
		"name":         "env-instance",
		"type":         "minecraft_java",
		"processType":  "direct",
		"startCommand": "java -jar server.jar",
		"groupId":      groupID,
		"envVars":      map[string]string{"FOO": "configured", "ONLY_CONFIGURED": "yes"},
	}
	created := makeRequest(r, http.MethodPost, "/api/v1/instances", createBody, adminToken)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	instanceID := uint(parseJSON(t, created)["id"].(float64))
	pool.SetWorkerClientForTest(node.UUID, routerInstanceEnvWorker{})
	path := "/api/v1/instances/" + itoa(instanceID) + "/env"

	t.Run("未登录拒绝", func(t *testing.T) {
		w := makeRequest(r, http.MethodGet, path, nil, "")
		require.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("无实例作用域按不存在收敛", func(t *testing.T) {
		memberToken := getMemberToken(t, r, "env-outsider", "password123")
		w := makeRequest(r, http.MethodGet, path, nil, memberToken)
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("组成员可读 configured 与 runtime 双区", func(t *testing.T) {
		memberToken := getMemberToken(t, r, "env-reader", "password123")
		memberID := findUserIDByUsername(t, db, "env-reader")
		addMemberViaAPI(t, r, adminToken, groupID, memberID, model.GroupMemberRoleMember)

		w := makeRequest(r, http.MethodGet, path, nil, memberToken)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		body := parseJSON(t, w)
		configured := body["configured"].(map[string]interface{})
		runtime := body["runtime"].(map[string]interface{})
		require.Equal(t, "configured", configured["FOO"])
		require.Equal(t, "yes", configured["ONLY_CONFIGURED"])
		require.Equal(t, "runtime", runtime["FOO"])
		require.Equal(t, "/opt/jdk-21", runtime["JAVA_HOME"])
		require.Equal(t, true, body["runtimeAvailable"])
	})
}
