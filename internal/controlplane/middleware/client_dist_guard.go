package middleware

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// ClientDistGuardChecker 客户端分发端点 IP 防护检查（FR-096，由 service.ClientIPGuardService 实现）。
// 以接口注入避免 middleware → service 依赖。
type ClientDistGuardChecker interface {
	// Allowed 报告 IP 是否放行（黑白名单）。
	Allowed(ip string) bool
	// MarkDeny / MarkRate / MarkConcurrency 累加防护拦截计数（可观测）。
	MarkDeny()
	MarkRate()
	MarkConcurrency()
}

// ClientDistGuard 面向玩家分发端点的 L7 防护中间件（FR-096，见 ADR-023）：
// ① IP 黑白名单（命中拒 → 403）② per-IP 令牌桶限流（超频 → 429）③ 全局并发信号量（满 → 429）。
// 限流以 **IP 为主键**（机器码不可信，ADR-023）；命中拦截累加计数器（可观测、不写库）。
// L3/L4 容量型 DDoS 不在此（靠 CDN/云清洗）。
func ClientDistGuard(guard ClientDistGuardChecker, ratePerSecond, burst, maxConcurrent int) gin.HandlerFunc {
	limiter := NewRateLimiter(ratePerSecond, burst)
	var sem chan struct{}
	var concurrentByIP map[string]int
	var concurrentMu sync.Mutex
	perIPConcurrent := 0
	if maxConcurrent > 0 {
		sem = make(chan struct{}, maxConcurrent)
		concurrentByIP = make(map[string]int)
		perIPConcurrent = maxConcurrent / 8
		if perIPConcurrent < 1 {
			perIPConcurrent = 1
		}
	}
	return func(c *gin.Context) {
		ip := c.ClientIP()

		// 1. IP 黑白名单。
		if guard != nil && !guard.Allowed(ip) {
			guard.MarkDeny()
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "IP_BLOCKED", "message": "访问被拒绝"})
			return
		}

		// 2. per-IP 令牌桶限流。
		if !limiter.Allow(ip) {
			if guard != nil {
				guard.MarkRate()
			}
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "RATE_LIMITED", "message": "请求过于频繁，请稍后再试"})
			return
		}

		// 3. 全局并发信号量。
		if sem != nil {
			if !acquireClientDistSlot(sem, concurrentByIP, &concurrentMu, ip, perIPConcurrent) {
				if guard != nil {
					guard.MarkConcurrency()
				}
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "BUSY", "message": "服务繁忙，请稍后再试"})
				return
			}
			defer releaseClientDistSlot(sem, concurrentByIP, &concurrentMu, ip)
		}

		c.Next()
	}
}

// acquireClientDistSlot 让单一来源最多占用八分之一并发槽，避免其耗尽全局 BUSY 容量。
func acquireClientDistSlot(sem chan struct{}, byIP map[string]int, mu *sync.Mutex, ip string, perIPLimit int) bool {
	mu.Lock()
	defer mu.Unlock()
	if byIP[ip] >= perIPLimit {
		return false
	}
	select {
	case sem <- struct{}{}:
		byIP[ip]++
		return true
	default:
		return false
	}
}

// releaseClientDistSlot 在请求结束时释放来源并发计数，空来源立即删掉以保持映射有界。
func releaseClientDistSlot(sem chan struct{}, byIP map[string]int, mu *sync.Mutex, ip string) {
	mu.Lock()
	defer mu.Unlock()
	byIP[ip]--
	if byIP[ip] == 0 {
		delete(byIP, ip)
	}
	<-sem
}
