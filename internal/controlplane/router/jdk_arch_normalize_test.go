package router

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
)

// TestFR289InstallArchAliasNormalized 安装请求 arch 别名归一化（FR-289）：
// API 调用方传 amd64/arm64 等 Go 系写法时，CP 应归一为下载源认的 x64/aarch64 再下发 Worker——
// 真机复现：arch=amd64 直透 adoptium 得下载 404 且错误不指向真因。
func TestFR289InstallArchAliasNormalized(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR033Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	fake := &fakeFR033RouterJDKWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

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
		fake.installReq = nil
		resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks/install", node.ID), map[string]any{
			"vendor": "temurin", "majorVersion": 17, "arch": tc.in,
		}, adminToken)
		require.Equal(t, http.StatusAccepted, resp.Code, "arch=%s: %s", tc.in, resp.Body.String())
		require.NotNil(t, fake.installReq, "arch=%s 未下发 Worker", tc.in)
		require.Equal(t, tc.want, fake.installReq.Arch, "arch=%s 应归一为 %s", tc.in, tc.want)
	}
}

// TestFR289InstallUnknownArchRejected 未知 arch 直接 422 拒绝（FR-289）：
// 不再直透下载源产出误导性的「下载返回 HTTP 404」。
func TestFR289InstallUnknownArchRejected(t *testing.T) {
	db := setupTestDB(t)
	pool := cpgrpc.NewClientPool()
	r := setupFR033Router(t, db, pool)
	adminToken := getAdminToken(t, r)
	node := createTestNode(t, db)
	fake := &fakeFR033RouterJDKWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)

	resp := makeRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/nodes/%d/jdks/install", node.ID), map[string]any{
		"vendor": "temurin", "majorVersion": 17, "arch": "foobar",
	}, adminToken)
	require.Equal(t, http.StatusUnprocessableEntity, resp.Code, resp.Body.String())
	require.Equal(t, "INVALID_ARCH", parseJSON(t, resp)["error"])
	require.Nil(t, fake.installReq, "非法 arch 不应下发 Worker")
}
