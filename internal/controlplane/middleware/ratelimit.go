package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter 令牌桶限流器。
type RateLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*bucket
	maxBuckets  int
	rate        int           // 每秒允许的请求数
	capacity    int           // 桶容量
	cleanup     time.Duration // 清理间隔
	lastCleanup time.Time
}

const maxRateLimitBuckets = 10000

type bucket struct {
	tokens    float64
	lastTime  time.Time
	capacity  float64
	ratePerMs float64
}

// NewRateLimiter 创建限流器。
func NewRateLimiter(ratePerSecond, capacity int) *RateLimiter {
	return &RateLimiter{
		buckets:     make(map[string]*bucket),
		maxBuckets:  maxRateLimitBuckets,
		rate:        ratePerSecond,
		capacity:    capacity,
		cleanup:     5 * time.Minute,
		lastCleanup: time.Now(),
	}
}

// Allow 检查指定 key 是否允许请求。
func (rl *RateLimiter) Allow(key string) bool {
	_, ok := rl.allow(key)
	return ok
}

// allow 与 Allow 判定完全同源，额外返回被拒时的**建议退避秒数**（供 Retry-After 头使用）。
//
// 退避值由桶内真实缺口推导：桶速率即稳态补充速度，故「补满一个令牌所需时间」就是客户端
// 下次可被放行的最短等待。判定分支与阈值一处未放宽——被拒请求仍然被拒，令牌仍然照扣，
// 只是把服务端已经知道的等待时长如实告诉客户端，避免其盲目重试把限流窗口越拖越长。
func (rl *RateLimiter) allow(key string) (retryAfterSec int, ok bool) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if now.Sub(rl.lastCleanup) >= rl.cleanup {
		for bucketKey, b := range rl.buckets {
			if now.Sub(b.lastTime) > rl.cleanup {
				delete(rl.buckets, bucketKey)
			}
		}
		rl.lastCleanup = now
	}

	b, exists := rl.buckets[key]
	if !exists {
		if len(rl.buckets) >= rl.maxBuckets {
			// 桶表已满：拒绝新来源。退避值必须非零，否则客户端立即重试、再次撞满表，
			// 形成忙等而把限流状态一直钉在满表。
			return refillSeconds(rl.rate), false
		}
		b = &bucket{
			tokens:    float64(rl.capacity),
			lastTime:  now,
			capacity:  float64(rl.capacity),
			ratePerMs: float64(rl.rate) / 1000.0,
		}
		rl.buckets[key] = b
	}

	// 补充令牌
	elapsed := now.Sub(b.lastTime).Seconds() * 1000
	b.tokens += elapsed * b.ratePerMs
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
	b.lastTime = now

	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	// 缺口按实际令牌补充速度换算；速率为 0 的纯容量闸不会自然补充，给保守的非零退避。
	if b.ratePerMs <= 0 {
		return refillSeconds(0), false
	}
	return ceilSeconds((1 - b.tokens) / b.ratePerMs), false
}

// refillSeconds 补充 1 个令牌所需秒数（向上取整、下限 1 秒）。
func refillSeconds(ratePerSecond int) int {
	if ratePerSecond <= 0 {
		return 1
	}
	return ceilSeconds(1000.0 / float64(ratePerSecond))
}

// ceilSeconds 毫秒向上取整为秒，下限 1（RFC 9110 的 delta-seconds 不表达亚秒粒度）。
func ceilSeconds(ms float64) int {
	if ms <= 0 {
		return 1
	}
	sec := int(ms / 1000)
	if ms-float64(sec*1000) > 0 {
		sec++
	}
	if sec < 1 {
		sec = 1
	}
	return sec
}

// RateLimitSpec 单个限流桶的预算。
type RateLimitSpec struct {
	RatePerSecond int // 稳态速率（请求/秒）
	Capacity      int // 突发容量（桶内最多积累的令牌数）
}

// RateLimitClassDefault 未归类路由的默认分类名。
const RateLimitClassDefault = "default"

// RateLimitClassLogQuery 日志查询类路由的分类名（重后端负载：跨 Worker 扇出 + 缓存 + DB 解析）。
const RateLimitClassLogQuery = "log_query"

// RateLimitClassOfFunc 由请求解析所属限流分类，返回空串表示默认分类。
//
// 实现必须只依赖**路由注册表**（如 c.FullPath()），不得依赖用户可控的原始路径或查询参数：
// 分类数必须是有界常量，否则限流状态会随请求内容膨胀。
type RateLimitClassOfFunc func(*gin.Context) string

// RateLimitPrefixRule 路由前缀 → 分类名的映射规则。
type RateLimitPrefixRule struct {
	Prefix string
	Class  string
}

// RateLimitClassByPrefix 生成按**路由模板前缀**分类的函数。
//
// 用 c.FullPath()（路由树给出的模板，如 "/api/v1/logs/federation"）而非
// c.Request.URL.Path：模板只可能取自注册表，匹配不到时为空串 ⇒ 归默认类，
// 未注册路径无法靠拼接 URL 自造分类。
func RateLimitClassByPrefix(rules ...RateLimitPrefixRule) RateLimitClassOfFunc {
	return func(c *gin.Context) string {
		fp := c.FullPath()
		if fp == "" {
			return RateLimitClassDefault
		}
		for _, r := range rules {
			if r.Prefix != "" && r.Class != "" && strings.HasPrefix(fp, r.Prefix) {
				return r.Class
			}
		}
		return RateLimitClassDefault
	}
}

// RateLimitByRouteClass 按「路由分类 × 客户端 IP」分桶的限流中间件，并保留跨分类的每 IP 总上限。
//
// 为什么需要按分类分桶（2026-10-09 生产实证）：原先整个 /api/v1 共用一个 per-IP 桶
// （10 请求/秒、容量 20）。而应用外壳的一页加载本身就是多请求扇出——实测浏览器 IP 在
// 加载实例页的 2 秒内发出约 30–60 次请求（/instances、/instances/{id}、/instances/aggregate），
// 直接把整桶吃满，落在同一桶里的日志页请求随即被 429 连坐（单秒 320 次请求中 297 次 429）。
// 按分类分桶让「一页的合法扇出」不再与「别的页面的轮询」互相挤占。
//
// 为什么这不是「把数值调大」：分类预算本身**未放宽**——日志类拿到的 10/20 正是旧全局桶
// 的同量级额度，默认类同样是 10/20；变化只在于它们不再共用一个桶。跨分类的每 IP 总量由
// globalSpec 显式钉住（且被分类拒绝的请求同样已扣减全局令牌，方向是更严而非更松），
// 因此每 IP 仍被硬性封顶，洪泛/失控循环照样被拒。
//
// 判定顺序：先全局、再分类。全局被拒即返回，不占用任何分类预算——保证「总量超限」这一
// 事实不会被某个分类的剩余额度掩盖。
func RateLimitByRouteClass(
	classOf RateLimitClassOfFunc,
	defaultSpec RateLimitSpec,
	classSpecs map[string]RateLimitSpec,
	globalSpec RateLimitSpec,
) gin.HandlerFunc {
	global := NewRateLimiter(globalSpec.RatePerSecond, globalSpec.Capacity)
	defaultLimiter := NewRateLimiter(defaultSpec.RatePerSecond, defaultSpec.Capacity)
	classLimiters := make(map[string]*RateLimiter, len(classSpecs))
	for name, spec := range classSpecs {
		// 默认分类由 defaultSpec 承担：同名分类不得出现两个预算，否则判定依赖注册顺序。
		if name == "" || name == RateLimitClassDefault {
			continue
		}
		classLimiters[name] = NewRateLimiter(spec.RatePerSecond, spec.Capacity)
	}

	return func(c *gin.Context) {
		ip := c.ClientIP()

		if retryAfter, ok := global.allow(ip); !ok {
			writeRateLimited(c, retryAfter)
			return
		}

		class := RateLimitClassDefault
		if classOf != nil {
			if v := classOf(c); v != "" {
				class = v
			}
		}
		limiter := defaultLimiter
		if l, ok := classLimiters[class]; ok {
			limiter = l
		}

		// 分类桶键含分类名：同一分类内仍是严格 per-IP 计数（跨分类才相互独立）。
		if retryAfter, ok := limiter.allow(class + "\x1f" + ip); !ok {
			writeRateLimited(c, retryAfter)
			return
		}

		c.Next()
	}
}

// writeRateLimited 统一写出 429 响应。
//
// `Retry-After` 是 RFC 9110 §10.2.3 定义的 delta-seconds 建议退避时长。此前 429 不带该头，
// 客户端只能盲重试，而重试本身继续消耗令牌，把限流窗口越拖越长（生产实证：单 IP 单秒
// 297 次 429 全落在同一路由，并稳定持续 18 秒压在桶上限）。带上该头后客户端可正确退避；
// 判定逻辑不放宽任何一处，被拒的请求仍然被拒。
func writeRateLimited(c *gin.Context, retryAfterSec int) {
	if retryAfterSec < 1 {
		retryAfterSec = 1
	}
	c.Header("Retry-After", strconv.Itoa(retryAfterSec))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error":      "RATE_LIMITED",
		"message":    "请求过于频繁，请稍后再试",
		"retryAfter": retryAfterSec,
	})
}
