package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// ADR-107 §3 缺口查询端点测试。
//
// 契约要点（逐条断言）：参数缺失 400、区间过滤、seq 升序、limit 默认/上限、
// **无数据返回空数组**（不是 404、不是 null）、响应里只有位点没有 payload。

// newCursorRouterDB 建独立内存库并迁移位点账本表。
func newCursorRouterDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.BusinessEventCursor{}))
	return db
}

// buildCursorEngine 组装一个只挂缺口查询端点的最小引擎（鉴权用注入主体的桩中间件；
// 真实路由上的鉴权由 TestBusinessCursor_RouteRegistered 覆盖）。
func buildCursorEngine(t *testing.T, svc *service.BusinessCursorService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/api/v1")
	group.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(1))
		c.Set(middleware.CtxUsername, "tester")
		c.Next()
	})
	NewBusinessCursorHandler(svc).RegisterRoutes(group)
	return engine
}

// getCursor 发一次缺口查询请求，返回状态码与原始 body。
func getCursor(t *testing.T, engine *gin.Engine, query url.Values) (int, []byte) {
	t.Helper()
	raw := "/api/v1/business/cursor"
	if len(query) > 0 {
		raw += "?" + query.Encode()
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, raw, nil)
	engine.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

// seedCursorRows 落若干位点（streamSeq 必须以 z 结尾唯一：先用序号占位）。
func seedCursorRows(t *testing.T, db *gorm.DB, instanceID uint, domain string, seqs []int64) {
	t.Helper()
	now := time.Now()
	rows := make([]model.BusinessEventCursor, 0, len(seqs))
	for i, seq := range seqs {
		rows = append(rows, model.BusinessEventCursor{
			StreamSeq: seq*10 + int64(i), InstanceID: instanceID, Domain: domain,
			Seq: seq, DedupKey: "e-" + domain, OccurredAt: 1730000000000, CreatedAt: now,
		})
	}
	require.NoError(t, db.Create(&rows).Error)
}

// TestBusinessCursor_QueryReturnsOrderedCursors 正常查询：区间过滤 + 升序 + 字段齐全。
func TestBusinessCursor_QueryReturnsOrderedCursors(t *testing.T) {
	db := newCursorRouterDB(t)
	seedCursorRows(t, db, 5, "activity", []int64{10, 20, 30, 40})
	engine := buildCursorEngine(t, service.NewBusinessCursorService(db, 7))

	code, body := getCursor(t, engine, url.Values{
		"instanceId": {"5"}, "domain": {"activity"}, "fromSeq": {"20"}, "limit": {"100"},
	})
	require.Equal(t, http.StatusOK, code, "body=%s", body)

	var resp struct {
		Cursors []struct {
			StreamSeq  int64  `json:"streamSeq"`
			Seq        int64  `json:"seq"`
			DedupKey   string `json:"dedupKey"`
			OccurredAt int64  `json:"occurredAt"`
		} `json:"cursors"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Len(t, resp.Cursors, 3, "fromSeq=20 应过滤掉 seq=10")
	require.EqualValues(t, 20, resp.Cursors[0].Seq, "必须升序")
	require.EqualValues(t, 40, resp.Cursors[2].Seq)
	require.NotEmpty(t, resp.Cursors[0].DedupKey)
	require.EqualValues(t, 1730000000000, resp.Cursors[0].OccurredAt)

	// 响应里绝不能出现 payload 字段（哪怕嵌套）。
	require.NotContains(t, string(body), `"payload"`, "缺口查询绝不能返回业务载荷")
}

// TestBusinessCursor_EmptyReturnsEmptyArray 无数据返回 200 + 空数组（不是 404、不是 null）。
func TestBusinessCursor_EmptyReturnsEmptyArray(t *testing.T) {
	db := newCursorRouterDB(t)
	engine := buildCursorEngine(t, service.NewBusinessCursorService(db, 7))

	code, body := getCursor(t, engine, url.Values{"instanceId": {"99"}, "domain": {"activity"}})
	require.Equal(t, http.StatusOK, code, "无数据不是错误，body=%s", body)
	// 关键：必须是 []，不能是 null（null 会让调用方的 for-of 直接抛错）。
	require.JSONEq(t, `{"cursors":[]}`, string(body))
}

// TestBusinessCursor_DefaultAndMaxLimit limit 缺省 1000、超上限钳制 5000。
func TestBusinessCursor_DefaultAndMaxLimit(t *testing.T) {
	db := newCursorRouterDB(t)
	// 1200 条：> 默认 1000，用来验证默认值真的生效（而不是"返回全部"）。
	seqs := make([]int64, 0, 1200)
	for i := int64(1); i <= 1200; i++ {
		seqs = append(seqs, i)
	}
	seedCursorRows(t, db, 1, "activity", seqs)

	svc := service.NewBusinessCursorService(db, 7)
	engine := buildCursorEngine(t, svc)

	var resp struct {
		Cursors []struct {
			Seq int64 `json:"seq"`
		} `json:"cursors"`
	}

	// 不传 limit → 默认 1000
	code, body := getCursor(t, engine, url.Values{"instanceId": {"1"}, "domain": {"activity"}})
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Len(t, resp.Cursors, service.BusinessCursorQueryDefaultLimit)
	require.EqualValues(t, 1, resp.Cursors[0].Seq)
	require.EqualValues(t, 1000, resp.Cursors[999].Seq, "默认返回最小 seq 的前 1000 条")

	// limit 超上限 → 钳制 5000（此处只有 1200 条，故返回 1200）
	code, body = getCursor(t, engine, url.Values{
		"instanceId": {"1"}, "domain": {"activity"}, "limit": {"999999"},
	})
	require.Equal(t, http.StatusOK, code)
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Len(t, resp.Cursors, 1200, "超上限被钳制到 5000，仍只与表内数据量有关")
}

// TestBusinessCursor_BadParams 参数非法一律 400（不静默当 0，否则掩盖调用方 bug）。
func TestBusinessCursor_BadParams(t *testing.T) {
	db := newCursorRouterDB(t)
	engine := buildCursorEngine(t, service.NewBusinessCursorService(db, 7))

	cases := []struct {
		name  string
		query url.Values
	}{
		{"缺 instanceId", url.Values{"domain": {"activity"}}},
		{"instanceId 非数字", url.Values{"instanceId": {"abc"}, "domain": {"activity"}}},
		{"instanceId 为 0", url.Values{"instanceId": {"0"}, "domain": {"activity"}}},
		{"缺 domain", url.Values{"instanceId": {"1"}}},
		{"domain 全空白", url.Values{"instanceId": {"1"}, "domain": {"   "}}},
		{"fromSeq 非数字", url.Values{"instanceId": {"1"}, "domain": {"a"}, "fromSeq": {"x"}}},
		{"fromSeq 负数", url.Values{"instanceId": {"1"}, "domain": {"a"}, "fromSeq": {"-1"}}},
		{"limit 非数字", url.Values{"instanceId": {"1"}, "domain": {"a"}, "limit": {"x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := getCursor(t, engine, tc.query)
			require.Equal(t, http.StatusBadRequest, code, "body=%s", body)
		})
	}
}

// TestBusinessCursor_Unavailable 未装配服务 → 503（等价「未部署该能力」）。
func TestBusinessCursor_Unavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/api/v1")
	NewBusinessCursorHandler(nil).RegisterRoutes(group)
	code, body := getCursor(t, engine, url.Values{"instanceId": {"1"}, "domain": {"a"}})
	require.Equal(t, http.StatusServiceUnavailable, code, "body=%s", body)
}

// TestBusinessCursor_RouteRegistered 在**真实路由**上验证端点存在 + 鉴权。
//
// 鉴权与 SSE 订阅同一套（Agent Token 优先、JWT 兜底）：无凭证必须 401，不会因
// 「只读端点」而放宽——位点表本身会暴露实例的事件节奏。
func TestBusinessCursor_RouteRegistered(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)

	// 未带凭证 → 401。
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/business/cursor?instanceId=1&domain=a", nil)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, "body=%s", w.Body.String())

	// 带 JWT → 到达 handler（此库未装配位点账本，故 503 而不是 404——证明路由与鉴权都通了）。
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/business/cursor?instanceId=1&domain=a", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	require.NotEqual(t, http.StatusNotFound, w.Code, "端点必须已注册，body=%s", w.Body.String())
	require.NotEqual(t, http.StatusUnauthorized, w.Code, "合法 JWT 必须通过鉴权，body=%s", w.Body.String())
}
