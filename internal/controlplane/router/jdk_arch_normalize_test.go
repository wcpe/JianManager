package router

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestInstallArchAliasNormalized 安装请求 arch 别名归一化：
// API 调用方传 amd64/arm64 等 Go 系写法时，CP 应归一为下载源认的 x64/aarch64 再下发 Worker——
// 真机复现：arch=amd64 直透 adoptium 得下载 404 且错误不指向真因。
//
// 【为什么每个用例用独立节点】归一化之后 `amd64`/`x86_64`/`X64` 都变成同一个 `x64`，
// 对同一节点而言「装 temurin 17 x64」是**同一件事**——这正是 CP 幂等键要复用的情形
// （第二次会返回 200 + reused，而不是再下发一次下载）。
// 若在同一节点上依次跑这 5 个 arch，除第一个之外都会命中幂等复用，
// 断言 202 就变成在测「幂等不生效」，而不是在测 arch 归一化。
// 每个 case 一个节点，才能让每个 arch 都真的走到「新建任务 + 下发 Worker」这条路径。
func TestInstallArchAliasNormalized(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"amd64", "x64"},
		{"x86_64", "x64"},
		{"X64", "x64"},
		{"arm64", "aarch64"},
		{"aarch64", "aarch64"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			db := setupTestDB(t)
			pool := cpgrpc.NewClientPool()
			r := setupJDKRouter(t, db, pool)
			adminToken := getAdminToken(t, r)
			// 每个子用例一个独立节点：避免「归一化后同一件事」触发幂等复用，
			// 那样测到的就不是归一化了。
			node := createTestNodeWithSuffix(t, db, "arch-"+tc.in)
			fake := &fakeRouteJDKWorker{}
			pool.SetWorkerClientForTest(node.UUID, fake)

			resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks/install", node.ID), map[string]any{
				"vendor": "temurin", "majorVersion": 17, "arch": tc.in,
			}, adminToken)
			require.Equal(t, http.StatusAccepted, resp.Code, "arch=%s: %s", tc.in, resp.Body.String())
			require.NotNil(t, fake.installReq, "arch=%s 未下发 Worker", tc.in)
			require.Equal(t, tc.want, fake.installReq.Arch, "arch=%s 应归一为 %s", tc.in, tc.want)
		})
	}
}

// TestInstallSameParamsReused 同参数重复提交必须复用，不能再下发一次下载。
//
// 这是「连点两次安装」的正确语义：第二次点的不是错误请求，它要做的正是第一次已经在做的事。
// 转红方式：让 AdmitTask 的幂等键恒为空串，第二个请求就会重新建任务并再次下发 Worker。
func TestInstallSameParamsReused(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupJDKRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	fake := &fakeRouteJDKWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	body := map[string]any{"vendor": "temurin", "majorVersion": 17, "arch": "amd64"}
	first := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks/install", node.ID), body, adminToken)
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	firstTaskID := parseJSON(t, first)["taskId"]
	require.NotEmpty(t, firstTaskID)

	// 接口调用方的 arch 写法不同但归一化后是同一个值：也算同一件事。
	second := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks/install", node.ID), map[string]any{
		"vendor": "temurin", "majorVersion": 17, "arch": "x86_64",
	}, adminToken)
	require.Equal(t, http.StatusOK, second.Code,
		"同参数的重复提交应复用（200 + reused），而不是 202 再下发一次: %s", second.Body.String())
	secondBody := parseJSON(t, second)
	require.Equal(t, true, secondBody["reused"], "必须标记为复用")
	require.Equal(t, firstTaskID, secondBody["taskId"], "复用的必须是同一个任务")

	// 只能有一条任务记录（连点不该产生幽灵任务）。
	var count int64
	require.NoError(t, db.Model(&model.Task{}).Where("kind = ?", model.TaskKindJDKInstall).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

// TestInstallDifferentMajorBlocked 不同大版本抢同一块托管目录，必须被拒绝（不是复用）。
//
// 区分这两条语义很重要：同参数是「已经在做了」（复用），不同参数是「抢同一块盘」（拒绝）。
// 转红方式：把互斥组置空，第二个请求就会新建任务并再下发一次下载。
func TestInstallDifferentMajorBlocked(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupJDKRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	fake := &fakeRouteJDKWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	first := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks/install", node.ID), map[string]any{
		"vendor": "temurin", "majorVersion": 17, "arch": "amd64",
	}, adminToken)
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())

	second := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks/install", node.ID), map[string]any{
		"vendor": "temurin", "majorVersion": 21, "arch": "amd64",
	}, adminToken)
	require.Equal(t, http.StatusConflict, second.Code, second.Body.String())
	require.Equal(t, "TASK_EXCLUSIVE_BUSY", parseJSON(t, second)["error"])
}

// TestInstallUnknownArchRejected 未知 arch 直接 422 拒绝：
// 不再直透下载源产出误导性的「下载返回 HTTP 404」。
func TestInstallUnknownArchRejected(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupJDKRouter(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	fake := &fakeRouteJDKWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks/install", node.ID), map[string]any{
		"vendor": "temurin", "majorVersion": 17, "arch": "foobar",
	}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code, resp.Body.String())
	require.Equal(t, "INVALID_ARCH", parseJSON(t, resp)["error"])
	require.Nil(t, fake.installReq, "非法 arch 不应下发 Worker")
}
