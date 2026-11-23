package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/platform/trafficlimit"
)

// newTestEngine 装配一个只含限流中间件的引擎，在 /api/v1 下挂探针路由：
// 日志查询类（前缀命中 log_query）、默认类（读写各一条）。
func newTestEngine(mw gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(mw)
	api := r.Group("/api/v1")
	api.GET("/logs/federation", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	api.GET("/logs", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	api.GET("/instances", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	// 写操作探针：读写分属不同预算的用例需要一条返回 200 的 POST 路由。
	api.POST("/instances", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	return r
}

// doReq 发起一次 GET，返回状态码、Retry-After 头与响应体。
func doReq(t *testing.T, r *gin.Engine, path string) (int, string, string) {
	t.Helper()
	return doReqMethod(t, r, http.MethodGet, path)
}

// doReqMethod 与 doReq 同义，但可指定 HTTP 方法。
func doReqMethod(t *testing.T, r *gin.Engine, method, path string) (int, string, string) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w.Code, w.Header().Get("Retry-After"), w.Body.String()
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

// testLimiters 以显式预算构造限流器集合（测试用，避免依赖平台设置）。
func testLimiters(classOf RateLimitClassOfFunc, s trafficlimit.Settings) *RateLimiters {
	return NewRateLimiters(classOf, s)
}

// logsClass 只把 /api/v1/logs* 归入日志查询类。
func logsClass() RateLimitClassOfFunc {
	return RateLimitClassByPrefix(
		RateLimitPrefixRule{Prefix: "/api/v1/logs/federation", Class: RateLimitClassLogQuery},
		RateLimitPrefixRule{Prefix: "/api/v1/logs", Class: RateLimitClassLogQuery},
	)
}

// TestRateLimitByRouteClass_ClassIsolation 日志类打满不得连坐写操作。
func TestRateLimitByRouteClass_ClassIsolation(t *testing.T) {
	l := testLimiters(RateLimitClassByMethod(logsClass()), trafficlimit.Settings{
		Read:     trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
		Write:    trafficlimit.Spec{RatePerSecond: 1, Capacity: 2},
		LogQuery: trafficlimit.Spec{RatePerSecond: 1, Capacity: 2},
		Global:   trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
	})
	r := newTestEngine(RateLimitByRouteClass(l))

	// 打满日志类（容量 2 ⇒ 第 3 次被拒）。
	for i := 0; i < 2; i++ {
		code, _, _ := doReq(t, r, "/api/v1/logs/federation")
		require.Equal(t, http.StatusOK, code, "日志类第 %d 次应放行", i+1)
	}
	code, retry, body := doReq(t, r, "/api/v1/logs/federation")
	assert.Equal(t, http.StatusTooManyRequests, code, "日志类第 3 次应被拒")
	assert.Equal(t, "RATE_LIMITED", parseErrCode(t, body))
	assert.Equal(t, "1", retry, "速率为 1/s ⇒ 退避 1 秒")

	// 关键：写操作自有预算，不受日志类耗尽影响。
	code, _, _ = doReqMethod(t, r, http.MethodPost, "/api/v1/instances")
	assert.Equal(t, http.StatusOK, code, "写操作必须与日志类互不挤占")
}

// TestRateLimitByRouteClass_GlobalCeiling 分类隔离不得让每 IP 总量变成无界。
func TestRateLimitByRouteClass_GlobalCeiling(t *testing.T) {
	l := testLimiters(RateLimitClassByMethod(logsClass()), trafficlimit.Settings{
		// 分类给足，只有全局桶（容量 3）构成约束。
		Read:     trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
		Write:    trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
		LogQuery: trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
		Global:   trafficlimit.Spec{RatePerSecond: 1, Capacity: 3},
	})
	r := newTestEngine(RateLimitByRouteClass(l))

	okAll := 0
	for _, p := range []string{"/api/v1/instances", "/api/v1/logs/federation", "/api/v1/instances"} {
		code, _, _ := doReq(t, r, p)
		if code == http.StatusOK {
			okAll++
		}
	}
	assert.Equal(t, 3, okAll, "全局桶容量 3 ⇒ 前 3 次放行")

	code, retry, _ := doReq(t, r, "/api/v1/logs/federation")
	assert.Equal(t, http.StatusTooManyRequests, code, "跨分类总量超限必须被拒")
	assert.NotEqual(t, "", retry, "全局桶被拒同样要带 Retry-After")
}

// TestRateLimitByRouteClass_ClassOfUsesRouteTemplate 分类取自路由模板，未注册路径不能自造分类。
func TestRateLimitByRouteClass_ClassOfUsesRouteTemplate(t *testing.T) {
	l := testLimiters(RateLimitClassByMethod(logsClass()), trafficlimit.Settings{
		// 读预算容量 1：用来验证「未注册路径归读类」而不是日志类。
		Read:     trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		Write:    trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		LogQuery: trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
		Global:   trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
	})
	r := newTestEngine(RateLimitByRouteClass(l))

	// 未注册路径：FullPath 为空 ⇒ 不能凭 URL 文本伪装成日志类；
	// 方法是 GET，故归读类并消耗读预算（容量 1）。
	code, _, _ := doReq(t, r, "/api/v1/logs-federation-not-registered")
	assert.Equal(t, http.StatusNotFound, code)
	code, _, _ = doReq(t, r, "/api/v1/instances")
	assert.Equal(t, http.StatusTooManyRequests, code,
		"读预算被上面的 404 消耗 ⇒ 证明未注册路径归读类，而非日志类（日志类容量 1000）")
}

// TestRateLimitClassByMethod_ReadWriteSplit 只读与写分属不同预算，且前缀类优先。
func TestRateLimitClassByMethod_ReadWriteSplit(t *testing.T) {
	classOf := RateLimitClassByMethod(logsClass())

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var seen string
	r.Use(func(c *gin.Context) { seen = classOf(c) })
	r.GET("/api/v1/instances", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.POST("/api/v1/instances", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/api/v1/logs/federation", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	cases := []struct{ method, path, want string }{
		{http.MethodGet, "/api/v1/instances", RateLimitClassRead},
		{http.MethodPost, "/api/v1/instances", RateLimitClassDefault},
		// 日志查询即便方法是 GET 也仍走自己的重后端分类，不能被读类吞掉。
		{http.MethodGet, "/api/v1/logs/federation", RateLimitClassLogQuery},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		assert.Equal(t, tc.want, seen, "%s %s 应归入 %s", tc.method, tc.path, tc.want)
	}
}

// TestRateLimitByRouteClass_ReadBudgetIndependent 读类打满不得连坐写操作。
func TestRateLimitByRouteClass_ReadBudgetIndependent(t *testing.T) {
	l := testLimiters(RateLimitClassByMethod(nil), trafficlimit.Settings{
		Read:     trafficlimit.Spec{RatePerSecond: 1, Capacity: 2},
		Write:    trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		LogQuery: trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
		Global:   trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
	})
	r := newTestEngine(RateLimitByRouteClass(l))

	for i := 0; i < 2; i++ {
		code, _, _ := doReq(t, r, "/api/v1/instances")
		require.Equal(t, http.StatusOK, code, "读第 %d 次应放行", i+1)
	}
	code, _, _ := doReq(t, r, "/api/v1/instances")
	require.Equal(t, http.StatusTooManyRequests, code, "读类容量耗尽后被拒")

	code, _, _ = doReqMethod(t, r, http.MethodPost, "/api/v1/instances")
	assert.Equal(t, http.StatusOK, code, "读类打满不得连坐写操作")
}

// TestRateLimiter_HotReloadViaMiddleware 运行期改预算立即生效（不必重建中间件）。
func TestRateLimiter_HotReloadViaMiddleware(t *testing.T) {
	l := testLimiters(RateLimitClassByMethod(nil), trafficlimit.Settings{
		Read:     trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		Write:    trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		LogQuery: trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		Global:   trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
	})
	r := newTestEngine(RateLimitByRouteClass(l))

	code, _, _ := doReq(t, r, "/api/v1/instances")
	require.Equal(t, http.StatusOK, code, "容量 1 ⇒ 首次放行")
	code, _, _ = doReq(t, r, "/api/v1/instances")
	require.Equal(t, http.StatusTooManyRequests, code, "容量耗尽 ⇒ 拒绝")

	// 运行期放宽（全局也放宽，否则仍被总闸拦住）。
	//
	// 语义边界：热更新**不重置已有桶**——已耗尽的 key 不会凭空得到一次满额突发
	// （那等于每次改配置都放行一波洪峰）。它立即生效的是「补充速率与容量上限」：
	// 速率从 1/s 提到 1000/s 后，几十毫秒的令牌补充就足以放行一次。
	l.Apply(trafficlimit.Settings{
		Read:     trafficlimit.Spec{RatePerSecond: 1000, Capacity: 100},
		Write:    trafficlimit.Spec{RatePerSecond: 1000, Capacity: 100},
		LogQuery: trafficlimit.Spec{RatePerSecond: 1000, Capacity: 100},
		Global:   trafficlimit.Spec{RatePerSecond: 1000, Capacity: 100},
	})
	time.Sleep(50 * time.Millisecond)
	code, _, _ = doReq(t, r, "/api/v1/instances")
	assert.Equal(t, http.StatusOK, code, "热更新后应按新速率补充并立即放行")
}

// TestRateLimitByRouteClass_UnlimitedSkips 显式关闭分类预算时该分类一律放行。
func TestRateLimitByRouteClass_UnlimitedSkips(t *testing.T) {
	l := testLimiters(RateLimitClassByMethod(nil), trafficlimit.Settings{
		Read:     trafficlimit.Spec{}, // 不限流
		Write:    trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		LogQuery: trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		Global:   trafficlimit.Spec{RatePerSecond: 1000, Capacity: 1000},
	})
	r := newTestEngine(RateLimitByRouteClass(l))
	for i := 0; i < 200; i++ {
		code, _, _ := doReq(t, r, "/api/v1/instances")
		require.Equal(t, http.StatusOK, code, "只读预算被关闭时应一律放行")
	}
}

// TestRateLimiter_Allow 桶容量内的放行与拒绝。
func TestRateLimiter_Allow(t *testing.T) {
	rl := NewRateLimiterForTest(2, 5)
	for i := 0; i < 5; i++ {
		assert.True(t, rl.Allow("ip-1"), "第 %d 次应允许", i+1)
	}
	assert.False(t, rl.Allow("ip-1"), "第 6 次应拒绝")
}

// TestRateLimiter_DifferentKeys 不同 key 独立限流。
func TestRateLimiter_DifferentKeys(t *testing.T) {
	rl := NewRateLimiterForTest(1, 3)
	assert.True(t, rl.Allow("ip-a"))
	assert.True(t, rl.Allow("ip-a"))
	assert.True(t, rl.Allow("ip-a"))
	assert.False(t, rl.Allow("ip-a"))

	assert.True(t, rl.Allow("ip-b"))
	assert.True(t, rl.Allow("ip-b"))
}

// TestRateLimiter_RetryAfterReflectsGap Retry-After 必须反映真实缺口（不得为 0 忙等）。
func TestRateLimiter_RetryAfterReflectsGap(t *testing.T) {
	rl := NewRateLimiterForTest(1, 1)
	spec := trafficlimit.Spec{RatePerSecond: 1, Capacity: 1}
	_, ok := rl.allowWithSpec("k", &spec)
	require.True(t, ok)
	retry, ok := rl.allowWithSpec("k", &spec)
	assert.False(t, ok)
	assert.Equal(t, 1, retry, "1/s 的桶补满 1 个令牌约需 1 秒（向上取整）")

	rl2 := NewRateLimiterForTest(2, 1)
	spec2 := trafficlimit.Spec{RatePerSecond: 2, Capacity: 1}
	_, _ = rl2.allowWithSpec("k", &spec2)
	retry, ok = rl2.allowWithSpec("k", &spec2)
	assert.False(t, ok)
	assert.GreaterOrEqual(t, retry, 1, "不得为 0，否则客户端会忙等")
}

// TestRateLimiter_AllowCleansStaleBuckets 过期桶按其所在分片惰性清理。
func TestRateLimiter_AllowCleansStaleBuckets(t *testing.T) {
	// 高速率/大容量：本用例只验清理，不希望被令牌耗尽干扰判定结果。
	rl := NewRateLimiterForTest(1000, 100)
	rl.cleanup = time.Millisecond
	// 先建立该 key 的桶（同片），确保下一次请求走的是同一分片。
	assert.True(t, rl.Allow("fresh"))

	shard := rl.shards[shardIndex("fresh")]
	shard.mu.Lock()
	// 把该片标记为「早该清理」，并塞入一个陈旧桶。
	shard.lastCleanup = time.Now().Add(-time.Second)
	shard.buckets["stale"] = &bucket{tokens: 1, lastTime: time.Now().Add(-time.Second)}
	shard.mu.Unlock()

	assert.True(t, rl.Allow("fresh"))

	shard.mu.Lock()
	_, exists := shard.buckets["stale"]
	shard.mu.Unlock()
	assert.False(t, exists, "同一分片下一次请求应惰性清理过期桶")
}

// TestRateLimiter_BoundsBuckets 新来源不能无限增长限流状态（按分片均摊）。
func TestRateLimiter_BoundsBuckets(t *testing.T) {
	rl := NewRateLimiterForTest(1, 1)
	// 片内配额 = maxBuckets/shardCount；设成恰好 1 个 key 可入表。
	rl.maxBuckets = shardCount
	// 找一个落在同一分片上的两个 key，才能验证「该片满则拒」。
	first := "ip-a"
	target := shardIndex(first)
	var second string
	for i := 0; i < 10000; i++ {
		candidate := "ip-" + string(rune('b'+i%24)) + string(rune('a'+i/24))
		if shardIndex(candidate) == target && candidate != first {
			second = candidate
			break
		}
	}
	require.NotEmpty(t, second, "应能找到一个同片 key")

	assert.True(t, rl.Allow(first))
	assert.False(t, rl.Allow(second), "同片超额 ⇒ 新来源被拒")
}

// TestRateLimitConcurrentThroughput 高并发下不丢判定：N 个 key 并发请求，
// 每个 key 的放行次数严格等于容量（分片锁不串行化不同 key，也不破坏同一 key 的计数）。
func TestRateLimitConcurrentThroughput(t *testing.T) {
	const keys = 128
	const capacity = 10
	rl := NewRateLimiterForTest(1, capacity)

	var wg sync.WaitGroup
	results := make([]int, keys)
	for i := 0; i < keys; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := "client-" + string(rune('a'+idx%26)) + string(rune('a'+idx/26))
			for j := 0; j < capacity*2; j++ {
				if rl.Allow(key) {
					results[idx]++
				}
			}
		}(i)
	}
	wg.Wait()
	for i, v := range results {
		if v != capacity {
			t.Fatalf("key %d 放行次数应为容量 %d，实际 %d", i, capacity, v)
		}
	}
}

// BenchmarkRateLimiter_Parallel 分片后的并行吞吐基准（不同 IP 并发）。
func BenchmarkRateLimiter_Parallel(b *testing.B) {
	rl := NewRateLimiterForTest(1_000_000, 1_000_000)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := "client-" + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
			rl.Allow(key)
			i++
		}
	})
}

// BenchmarkRateLimiter_SameKey 单一 IP 的基准（同片串行，用于对照分片收益）。
func BenchmarkRateLimiter_SameKey(b *testing.B) {
	rl := NewRateLimiterForTest(1_000_000, 1_000_000)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			rl.Allow("same-client")
		}
	})
}

// TestRateLimitByRouteClass_SettingsExemptFromLimit 管理自救通道不被限流拦住。
//
// 缺陷现场（2026-11-13 实测）：把只读预算调成 1/s 后，6 次连读得到
// `200,429,429,429,429,429`，其中「把预算改回来」的 PUT /api/v1/settings
// 也是 429 —— 管理员被自己的配置锁在门外，只能重启或改库。
// 限流应当保护后端，不能连唯一的自救通道一起锁死。
func TestRateLimitByRouteClass_SettingsExemptFromLimit(t *testing.T) {
	l := testLimiters(RateLimitClassByMethod(nil), trafficlimit.Settings{
		Read:     trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		Write:    trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		LogQuery: trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		Global:   trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
	})
	// 引擎里补一条 settings 路由，验证豁免生效。
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RateLimitByRouteClass(l))
	api := r.Group("/api/v1")
	api.GET("/instances", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	api.GET("/settings", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	api.PUT("/settings", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	// 先把所有预算打满。
	for i := 0; i < 3; i++ {
		_, _, _ = doReq(t, r, "/api/v1/instances")
	}
	code, _, _ := doReq(t, r, "/api/v1/instances")
	require.Equal(t, http.StatusTooManyRequests, code, "普通接口应已被限流")

	// 自救通道不受影响：连续多次仍然 200。
	for i := 0; i < 5; i++ {
		code, _, _ = doReq(t, r, "/api/v1/settings")
		assert.Equal(t, http.StatusOK, code, "读设置不应被限流（第 %d 次）", i+1)
		code, _, _ = doReqMethod(t, r, http.MethodPut, "/api/v1/settings")
		assert.Equal(t, http.StatusOK, code, "写设置不应被限流（第 %d 次）", i+1)
	}
}

// TestRateLimitByRouteClass_ExemptUsesRouteTemplate 未注册路径不会被前缀文本误豁免。
func TestRateLimitByRouteClass_ExemptUsesRouteTemplate(t *testing.T) {
	l := testLimiters(RateLimitClassByMethod(nil), trafficlimit.Settings{
		Read:     trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		Write:    trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		LogQuery: trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
		Global:   trafficlimit.Spec{RatePerSecond: 1, Capacity: 1},
	})
	r := newTestEngine(RateLimitByRouteClass(l))

	// 未注册路径 FullPath 为空 ⇒ 不豁免，照常消耗预算并 429。
	code, _, _ := doReq(t, r, "/api/v1/settings-not-registered")
	assert.Equal(t, http.StatusNotFound, code)
	code, _, _ = doReq(t, r, "/api/v1/instances")
	assert.Equal(t, http.StatusTooManyRequests, code, "未注册路径不得靠前缀文本蹭到豁免")
}
