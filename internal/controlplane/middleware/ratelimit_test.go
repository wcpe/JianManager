package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestEngine 装配一个只含限流中间件的引擎，并在 /api/v1 下挂两个探针路由：
// 一个日志查询类（前缀命中 log_query），一个默认类。
func newTestEngine(mw gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw)
	api := r.Group("/api/v1")
	api.GET("/logs/federation", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	api.GET("/logs", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	api.GET("/instances", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return r
}

// doReq 发起一次请求，返回状态码、Retry-After 头与响应体。
func doReq(t *testing.T, r *gin.Engine, path string) (int, string, string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	return w.Code, w.Header().Get("Retry-After"), w.Body.String()
}

// TestRateLimitByRouteClass_ClassIsolation 日志类打满不得连坐默认类（本次缺陷的直接回归）。
func TestRateLimitByRouteClass_ClassIsolation(t *testing.T) {
	mw := RateLimitByRouteClass(
		RateLimitClassByPrefix(
			RateLimitPrefixRule{Prefix: "/api/v1/logs/federation", Class: RateLimitClassLogQuery},
			RateLimitPrefixRule{Prefix: "/api/v1/logs", Class: RateLimitClassLogQuery},
		),
		RateLimitSpec{RatePerSecond: 1, Capacity: 2}, // 默认类
		map[string]RateLimitSpec{
			RateLimitClassLogQuery: {RatePerSecond: 1, Capacity: 2}, // 日志类
		},
		// 全局桶刻意给到远大于两类之和，用于单独验证「分类隔离」本身。
		RateLimitSpec{RatePerSecond: 1000, Capacity: 1000},
	)
	r := newTestEngine(mw)

	// 打满日志类（容量 2 ⇒ 第 3、4 次被拒）。
	for i := 0; i < 2; i++ {
		code, _, _ := doReq(t, r, "/api/v1/logs/federation")
		require.Equal(t, http.StatusOK, code, "日志类第 %d 次应放行", i+1)
	}
	code, retry, body := doReq(t, r, "/api/v1/logs/federation")
	assert.Equal(t, http.StatusTooManyRequests, code, "日志类第 3 次应被拒")
	assert.Equal(t, "RATE_LIMITED", parseErrCode(t, body))
	assert.Equal(t, "1", retry, "速率为 1/s ⇒ 退避 1 秒")

	// 关键断言：默认类不受日志类耗尽影响（旧实现共用一桶 ⇒ 此处会 429）。
	code, _, _ = doReq(t, r, "/api/v1/instances")
	assert.Equal(t, http.StatusOK, code, "默认类必须与日志类互不挤占")

	// 默认类自己的额度同样独立生效，并未被放宽。
	code, _, _ = doReq(t, r, "/api/v1/instances")
	assert.Equal(t, http.StatusOK, code)
	code, _, _ = doReq(t, r, "/api/v1/instances")
	assert.Equal(t, http.StatusTooManyRequests, code, "默认类仍按自己的预算拒绝")
}

// TestRateLimitByRouteClass_GlobalCeiling 分类隔离不得让每 IP 总量变成无界。
func TestRateLimitByRouteClass_GlobalCeiling(t *testing.T) {
	mw := RateLimitByRouteClass(
		RateLimitClassByPrefix(
			RateLimitPrefixRule{Prefix: "/api/v1/logs", Class: RateLimitClassLogQuery},
		),
		RateLimitSpec{RatePerSecond: 1, Capacity: 2},
		map[string]RateLimitSpec{
			RateLimitClassLogQuery: {RatePerSecond: 1, Capacity: 2},
		},
		// 全局容量 3：即使分类桶各自还有余额，跨分类叠加也不得超过 3。
		RateLimitSpec{RatePerSecond: 1, Capacity: 3},
	)
	r := newTestEngine(mw)

	okAll := 0
	for _, p := range []string{"/api/v1/instances", "/api/v1/logs/federation", "/api/v1/instances"} {
		code, _, _ := doReq(t, r, p)
		if code == http.StatusOK {
			okAll++
		}
	}
	assert.Equal(t, 3, okAll, "全局桶容量 3 ⇒ 前 3 次放行")

	// 第 4 次无论走哪个分类都应被全局桶拦下。
	code, retry, _ := doReq(t, r, "/api/v1/logs/federation")
	assert.Equal(t, http.StatusTooManyRequests, code, "跨分类总量超限必须被拒")
	assert.NotEqual(t, "", retry, "全局桶被拒同样要带 Retry-After")
}

// TestRateLimitByRouteClass_GlobalRejectedDoesNotConsumeClassBudget 全局被拒不得扣分类预算。
func TestRateLimitByRouteClass_GlobalRejectedDoesNotConsumeClassBudget(t *testing.T) {
	mw := RateLimitByRouteClass(
		RateLimitClassByPrefix(
			RateLimitPrefixRule{Prefix: "/api/v1/logs", Class: RateLimitClassLogQuery},
		),
		RateLimitSpec{RatePerSecond: 1, Capacity: 10},
		map[string]RateLimitSpec{
			RateLimitClassLogQuery: {RatePerSecond: 1000, Capacity: 1000},
		},
		RateLimitSpec{RatePerSecond: 1, Capacity: 1},
	)
	r := newTestEngine(mw)

	// 第 1 次同时消耗全局与分类。
	code, _, _ := doReq(t, r, "/api/v1/logs/federation")
	require.Equal(t, http.StatusOK, code)
	// 第 2 次被全局拒（分类仍有大量余额）。
	code, _, _ = doReq(t, r, "/api/v1/logs/federation")
	require.Equal(t, http.StatusTooManyRequests, code)

	// 全局补充 1 个令牌后，分类余额必须仍然可用（若被拒时误扣了分类令牌，这里仍会 429）。
	time.Sleep(1100 * time.Millisecond)
	code, _, _ = doReq(t, r, "/api/v1/logs/federation")
	assert.Equal(t, http.StatusOK, code, "全局被拒的请求不得消耗分类桶令牌")
}

// TestRateLimitByRouteClass_ClassOfUsesRouteTemplate 分类必须取自路由模板，URL 拼接无法自造分类。
func TestRateLimitByRouteClass_ClassOfUsesRouteTemplate(t *testing.T) {
	mw := RateLimitByRouteClass(
		RateLimitClassByPrefix(
			RateLimitPrefixRule{Prefix: "/api/v1/logs", Class: RateLimitClassLogQuery},
		),
		RateLimitSpec{RatePerSecond: 1, Capacity: 1},
		map[string]RateLimitSpec{
			RateLimitClassLogQuery: {RatePerSecond: 1, Capacity: 1},
		},
		RateLimitSpec{RatePerSecond: 1000, Capacity: 1000},
	)
	r := newTestEngine(mw)

	// 未注册路径：FullPath 为空 ⇒ 归默认类，不能凭 URL 文本伪装成日志类。
	code, _, _ := doReq(t, r, "/api/v1/logs-federation-not-registered")
	assert.Equal(t, http.StatusNotFound, code)
	// 默认类容量 1 已被上面的 404 请求消耗，故再打一次应 429：
	// 说明该请求确实落在默认类，而非被识别为日志类。
	code, _, _ = doReq(t, r, "/api/v1/instances")
	assert.Equal(t, http.StatusTooManyRequests, code)
}

// TestRateLimitByRouteClass_RetryAfterReflectsGap Retry-After 必须反映真实缺口。
func TestRateLimitByRouteClass_RetryAfterReflectsGap(t *testing.T) {
	// 速率 1/s、容量 1：耗尽后需等待约 1 秒才补满 1 个令牌。
	rl := NewRateLimiter(1, 1)
	_, ok := rl.allow("k")
	require.True(t, ok)

	retry, ok := rl.allow("k")
	assert.False(t, ok)
	assert.Equal(t, 1, retry, "1/s 的桶补满 1 个令牌约需 1 秒（向上取整）")

	// 速率 2/s：补满 1 个令牌约 0.5 秒 ⇒ 向上取整仍为 1（不得为 0，否则客户端忙等）。
	rl2 := NewRateLimiter(2, 1)
	_, _ = rl2.allow("k")
	retry, ok = rl2.allow("k")
	assert.False(t, ok)
	assert.GreaterOrEqual(t, retry, 1)
}

// TestRateLimiter_AllowStillRejects 纯容量闸（速率为 0）仍必须拒绝且给出非零退避。
func TestRateLimiter_AllowStillRejects(t *testing.T) {
	rl := NewRateLimiter(0, 1)
	assert.True(t, rl.Allow("k"), "容量 1 ⇒ 首次放行")
	retry, ok := rl.allow("k")
	assert.False(t, ok, "速率为 0 ⇒ 永不补充，第二次必拒")
	assert.GreaterOrEqual(t, retry, 1, "退避不得为 0")
}

// TestRateLimiter_BucketTableFullRetryAfter 桶表满时拒绝仍须带非零退避。
func TestRateLimiter_BucketTableFullRetryAfter(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	rl.maxBuckets = 1
	require.True(t, rl.Allow("a"))
	retry, ok := rl.allow("b")
	assert.False(t, ok, "桶表满 ⇒ 新来源被拒")
	assert.GreaterOrEqual(t, retry, 1, "桶表满的退避不得为 0")
}

// parseErrCode 从 JSON 响应体取 error 字段。
func parseErrCode(t *testing.T, body string) string {
	t.Helper()
	var payload struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	return payload.Error
}

func TestRateLimiter_Allow(t *testing.T) {
	rl := NewRateLimiter(2, 5) // 2 请求/秒，桶容量 5

	t.Run("桶容量内允许", func(t *testing.T) {
		for i := 0; i < 5; i++ {
			assert.True(t, rl.Allow("ip-1"), "第 %d 次应允许", i+1)
		}
	})

	t.Run("超出桶容量拒绝", func(t *testing.T) {
		assert.False(t, rl.Allow("ip-1"), "第 6 次应拒绝")
	})
}

func TestRateLimiter_DifferentKeys(t *testing.T) {
	rl := NewRateLimiter(1, 3)

	// 不同 key 独立限流
	assert.True(t, rl.Allow("ip-a"))
	assert.True(t, rl.Allow("ip-a"))
	assert.True(t, rl.Allow("ip-a"))
	assert.False(t, rl.Allow("ip-a"))

	assert.True(t, rl.Allow("ip-b"))
	assert.True(t, rl.Allow("ip-b"))
}

func TestRateLimiter_TokenRefill(t *testing.T) {
	rl := NewRateLimiter(100, 2) // 高速率，容量 2

	assert.True(t, rl.Allow("test"))
	assert.True(t, rl.Allow("test"))
	assert.False(t, rl.Allow("test"))

	// 令牌会随时间补充（但由于测试速度极快，这里只验证逻辑正确性）
}

func TestRateLimiter_AllowCleansStaleBuckets(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	rl.cleanup = time.Millisecond
	assert.True(t, rl.Allow("stale"))

	rl.mu.Lock()
	rl.lastCleanup = time.Now().Add(-time.Second)
	rl.buckets["stale"].lastTime = time.Now().Add(-time.Second)
	rl.mu.Unlock()

	assert.True(t, rl.Allow("fresh"))

	rl.mu.Lock()
	_, exists := rl.buckets["stale"]
	rl.mu.Unlock()
	assert.False(t, exists, "下一次请求应惰性清理过期桶")
}

// TestRateLimiter_BoundsBuckets 新来源不能无限增长限流状态。
func TestRateLimiter_BoundsBuckets(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	rl.maxBuckets = 2

	assert.True(t, rl.Allow("ip-a"))
	assert.True(t, rl.Allow("ip-b"))
	assert.False(t, rl.Allow("ip-c"))
	assert.Len(t, rl.buckets, 2)
}
