package router

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// setupObsRouter 建含观测端点的最小引擎（JWT 平台管理员组）。返回引擎与 DB（供直插快照断言）。
func setupObsRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	jwtCfg := config.JWTConfig{Secret: "test-secret-key-for-testing", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	svcs := &Services{
		Auth:                    service.NewAuthService(db, jwtCfg),
		Authz:                   service.NewAuthzService(db),
		Audit:                   service.NewAuditService(db),
		ClientDistObservability: service.NewClientDistObservabilityService(db),
	}
	return Setup(svcs, jwtCfg.Secret)
}

// seedSnapshot 直插一条观测快照（绕过聚合，专测端点）。
func seedSnapshot(t *testing.T, db *gorm.DB, ch string, bucket time.Time, manifestPulls int64) {
	t.Helper()
	require.NoError(t, db.Create(&model.ClientDistSnapshot{
		ChannelID: ch, BucketTS: bucket.UTC().Truncate(time.Hour),
		ManifestPulls: manifestPulls, UpdateTotal: 10, UpdateSuccess: 9,
		VersionDist:  `{"7":` + itoa64(manifestPulls) + `}`,
		PlatformDist: `{"windows":5}`,
	}).Error)
}

func itoa64(n int64) string { b, _ := json.Marshal(n); return string(b) }

// TestObsEndpoint_Query_HappyPath 管理员查总/单频道返时序+汇总+分布。
func TestObsEndpoint_Query_HappyPath(t *testing.T) {
	db := setupTestDB(t)
	r := setupObsRouter(t, db)
	token := getAdminToken(t, r)
	now := time.Now().UTC()
	seedSnapshot(t, db, "ch1", now.Add(-2*time.Hour), 100)
	seedSnapshot(t, db, "ch2", now.Add(-2*time.Hour), 50)

	// 总（不传 channelId）：两频道同小时合并 manifestPulls=150。
	w := makeRequest(r, "GET", "/api/v1/client-dist/observability?range=24h", nil, token)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var total struct {
		Series  []map[string]any `json:"series"`
		Summary struct {
			ManifestPulls int64 `json:"manifestPulls"`
		} `json:"summary"`
		VersionDist  []map[string]any `json:"versionDist"`
		PlatformDist []map[string]any `json:"platformDist"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &total))
	require.Equal(t, int64(150), total.Summary.ManifestPulls)
	require.Len(t, total.Series, 1)
	require.NotEmpty(t, total.VersionDist)

	// 单频道筛选 ch1。
	w2 := makeRequest(r, "GET", "/api/v1/client-dist/observability?channelId=ch1&range=24h", nil, token)
	require.Equal(t, http.StatusOK, w2.Code)
	var one struct {
		Summary struct {
			ManifestPulls int64 `json:"manifestPulls"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &one))
	require.Equal(t, int64(100), one.Summary.ManifestPulls)
}

// TestObsEndpoint_RBAC 非平台管理员 403。
func TestObsEndpoint_RBAC(t *testing.T) {
	db := setupTestDB(t)
	r := setupObsRouter(t, db)
	_ = getAdminToken(t, r) // 先建管理员，使后续注册的是普通成员
	memberToken := getMemberToken(t, r, "member1", "password123")

	w := makeRequest(r, "GET", "/api/v1/client-dist/observability?range=24h", nil, memberToken)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}

// TestObsEndpoint_InvalidRange 非法 range / from>=to 返 400。
func TestObsEndpoint_InvalidRange(t *testing.T) {
	db := setupTestDB(t)
	r := setupObsRouter(t, db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-dist/observability?range=99x", nil, token)
	require.Equal(t, http.StatusBadRequest, w.Code)

	// from 晚于 to。
	from := time.Now().UTC().Format(time.RFC3339)
	to := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	w2 := makeRequest(r, "GET", "/api/v1/client-dist/observability?from="+from+"&to="+to, nil, token)
	require.Equal(t, http.StatusBadRequest, w2.Code)
}

// TestObsEndpoint_UnknownChannelEmpty 未知频道返 200 空集（不 404）。
func TestObsEndpoint_UnknownChannelEmpty(t *testing.T) {
	db := setupTestDB(t)
	r := setupObsRouter(t, db)
	token := getAdminToken(t, r)

	w := makeRequest(r, "GET", "/api/v1/client-dist/observability?channelId=nope&range=24h", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	var res struct {
		Series  []map[string]any `json:"series"`
		Summary struct {
			ManifestPulls int64 `json:"manifestPulls"`
		} `json:"summary"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	require.Empty(t, res.Series)
	require.Equal(t, int64(0), res.Summary.ManifestPulls)
}
