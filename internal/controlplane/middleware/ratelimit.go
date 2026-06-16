package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter 令牌桶限流器。
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     int           // 每秒允许的请求数
	capacity int           // 桶容量
	cleanup  time.Duration // 清理间隔
}

type bucket struct {
	tokens    float64
	lastTime  time.Time
	capacity  float64
	ratePerMs float64
}

// NewRateLimiter 创建限流器。
func NewRateLimiter(ratePerSecond, capacity int) *RateLimiter {
	rl := &RateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     ratePerSecond,
		capacity: capacity,
		cleanup:  5 * time.Minute,
	}

	go rl.cleanupLoop()
	return rl
}

// Allow 检查指定 key 是否允许请求。
func (rl *RateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[key]
	if !exists {
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
		return true
	}
	return false
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for key, b := range rl.buckets {
			if now.Sub(b.lastTime) > rl.cleanup {
				delete(rl.buckets, key)
			}
		}
		rl.mu.Unlock()
	}
}

// RateLimit 限流中间件。
// 按 IP 限流，默认 60 请求/分钟，桶容量 10。
func RateLimit(ratePerSecond, capacity int) gin.HandlerFunc {
	limiter := NewRateLimiter(ratePerSecond, capacity)

	return func(c *gin.Context) {
		key := c.ClientIP()

		if !limiter.Allow(key) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":   "RATE_LIMITED",
				"message": "请求过于频繁，请稍后再试",
			})
			return
		}

		c.Next()
	}
}
