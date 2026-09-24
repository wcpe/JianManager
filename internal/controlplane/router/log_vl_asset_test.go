package router

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

func TestLogVLAssetDownloadRequiresNodeScopedToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	node := &model.Node{UUID: "log-vl-test-node", Name: "log-vl-test"}
	require.NoError(t, db.Create(node).Error)
	root, err := dataroot.Resolve(t.TempDir())
	require.NoError(t, err)
	h := NewLogVLAssetHandler(service.NewApprovedVLAssetStore(root),
		service.NewSelfUpdateService(db, nil, service.SelfUpdateConfig{}, root), service.NewNodeService(db))
	assetURL, sha, err := h.PackageURL("http://cp.example", node.UUID, "linux", "amd64")
	require.NoError(t, err)
	require.NotEmpty(t, sha)
	parsed, err := url.Parse(assetURL)
	require.NoError(t, err)

	request := func(token, goos string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "tag", Value: "v1.52.0"}, {Key: "os", Value: goos}, {Key: "arch", Value: "amd64"}}
		c.Request = httptest.NewRequest(http.MethodGet, "/log-vl-assets/v1.52.0/"+goos+"/amd64/package?token="+url.QueryEscape(token), nil)
		h.Download(c)
		return w
	}
	require.Equal(t, http.StatusForbidden, request("", "linux").Code)
	require.Equal(t, http.StatusForbidden, request(parsed.Query().Get("token")+"changed", "linux").Code)
	require.Equal(t, http.StatusForbidden, request(parsed.Query().Get("token"), "windows").Code)
	require.Equal(t, http.StatusNotFound, request(parsed.Query().Get("token"), "linux").Code,
		"valid node-bound token reaches only the approved package cache")

	require.NoError(t, db.Delete(node).Error)
	require.Equal(t, http.StatusForbidden, request(parsed.Query().Get("token"), "linux").Code)
}

func TestSignedVLAssetURLIsExcludedFromGinAccessLog(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/log-vl-assets/v1.52.0/linux/amd64/package?token=sensitive-test-token", nil)
	require.True(t, skipSignedAssetAccessLog(c))
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/log-runtime/assets", nil)
	require.False(t, skipSignedAssetAccessLog(c))
}
