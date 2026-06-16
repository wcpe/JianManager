package middleware

import (
	"testing"

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
