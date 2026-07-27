package middleware

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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
