package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/wcpe/JianManager/internal/platform/trafficlimit"
)

// maxRateLimitBuckets 全局桶表上限（分片后按片均分），防止无界增长。
const maxRateLimitBuckets = 10000

// RateLimiter 令牌桶限流器。
//
// ## 并发模型（2026-11-13 高并发改造）
//
// 旧实现是「一张 map + **一把全局互斥锁**」：每个 API 请求都要在同一个 Mutex 上排队，
// 锁内还要做 map 查找与令牌补充。请求量上来时这把锁先于后端成为瓶颈——限流器本该保护
// 后端，结果自己成了串行点。
//
// 现在两级切分，都只缩小临界区，判定语义一字未变：
//  1. **分片**：按 key 哈希到 shardCount 个分片，各片自带互斥锁。不同 key 几乎不争用；
//     同一 key 仍串行，判定正确性不变。
//  2. **短临界区**：锁内只做「取桶 → 补令牌 → 扣令牌」的算术；过期桶按片惰性清理，
//     只在到达清理周期时触发，不做全表扫描。
//
// ## 规格为什么由调用方传入
//
// 同一个 limiter 实例服务 read / write / log_query 三个分类（桶键已含分类名），
// 三者速率不同（只读 60/s、写 10/s、日志 10/s）。若 limiter 自持一份规格，
// 就必然有一类按别人的速率判定——那会把写操作的严格预算悄悄放宽到只读档。
// 故核心入口 allowWithSpec 显式接受规格；限流规格的全部权归属于 RateLimiters
// （atomic 快照，可运行期热更新）。
type RateLimiter struct {
	shards     [shardCount]*rateLimitShard
	maxBuckets int
	cleanup    time.Duration
	// spec 仅服务 Allow/NewRateLimiterForTest 这类「自带规格」的便利用法；
	// 生产路径由 RateLimiters 按分类传入规格，不读这里。
	spec atomic.Pointer[trafficlimit.Spec]
}

const (
	// shardCount 取 64：常见并发 IP 数量下碰撞概率低，片表自身开销可忽略。
	// 必须是 2 的幂，便于用掩码取模。
	shardCount = 64
	shardMask  = shardCount - 1
)

type rateLimitShard struct {
	mu          sync.Mutex
	buckets     map[string]*bucket
	lastCleanup time.Time
}

type bucket struct {
	tokens   float64
	lastTime time.Time
}

// NewRateLimiter 创建限流器。spec 为便利用法（Allow）的默认规格，可为 nil。
func NewRateLimiter(spec *trafficlimit.Spec) *RateLimiter {
	rl := &RateLimiter{
		maxBuckets: maxRateLimitBuckets,
		cleanup:    5 * time.Minute,
	}
	for i := range rl.shards {
		rl.shards[i] = &rateLimitShard{buckets: make(map[string]*bucket), lastCleanup: time.Now()}
	}
	if spec != nil {
		copied := *spec
		rl.spec.Store(&copied)
	}
	return rl
}

// NewRateLimiterForTest 以「速率/容量」构造（既有测试与内部调用沿用）。
func NewRateLimiterForTest(ratePerSecond, capacity int) *RateLimiter {
	return NewRateLimiter(&trafficlimit.Spec{RatePerSecond: ratePerSecond, Capacity: capacity})
}

// SetSpec 更新便利用法（Allow）的默认规格；nil 表示不限流。
//
// 不重置已有桶：高压时临时放宽不该把已用配额清零（会让排队客户端同时涌入）。
func (rl *RateLimiter) SetSpec(spec *trafficlimit.Spec) {
	if spec == nil {
		rl.spec.Store(nil)
		return
	}
	copied := *spec
	rl.spec.Store(&copied)
}

// Allow 用自带规格检查 key 是否放行（便利用法；生产走 allowWithSpec）。
func (rl *RateLimiter) Allow(key string) bool {
	spec := rl.spec.Load()
	_, ok := rl.allowWithSpec(key, spec)
	return ok
}

// allowWithSpec 按给定规格判定；spec 为 nil 或显式不限流时直接放行。
func (rl *RateLimiter) allowWithSpec(key string, spec *trafficlimit.Spec) (retryAfterSec int, ok bool) {
	if spec == nil || spec.Unlimited() {
		return 0, true
	}
	rate := spec.RatePerSecond
	capacity := float64(spec.Capacity)

	shard := rl.shards[shardIndex(key)]
	now := time.Now()

	shard.mu.Lock()
	if now.Sub(shard.lastCleanup) >= rl.cleanup {
		for bucketKey, b := range shard.buckets {
			if now.Sub(b.lastTime) > rl.cleanup {
				delete(shard.buckets, bucketKey)
			}
		}
		shard.lastCleanup = now
	}

	b, exists := shard.buckets[key]
	if !exists {
		if len(shard.buckets) >= rl.shardBucketCap() {
			shard.mu.Unlock()
			// 桶表已满：拒绝新来源。退避必须非零，否则客户端立即重试、再次撞满表，
			// 形成忙等而把限流状态钉在满表。
			return refillSeconds(rate), false
		}
		start := capacity
		if start < 1 {
			start = 1
		}
		b = &bucket{tokens: start, lastTime: now}
		shard.buckets[key] = b
	}

	elapsed := now.Sub(b.lastTime).Seconds() * 1000
	b.tokens += elapsed * (float64(rate) / 1000.0)
	if b.tokens > capacity {
		b.tokens = capacity
	}
	b.lastTime = now

	if b.tokens >= 1 {
		b.tokens--
		shard.mu.Unlock()
		return 0, true
	}
	if rate <= 0 {
		shard.mu.Unlock()
		return refillSeconds(0), false
	}
	need := (1 - b.tokens) / (float64(rate) / 1000.0)
	shard.mu.Unlock()
	return ceilSeconds(need), false
}

// shardBucketCap 单片桶表上限（全额按片均分，至少 1）。
func (rl *RateLimiter) shardBucketCap() int {
	capPerShard := rl.maxBuckets / shardCount
	if capPerShard < 1 {
		capPerShard = 1
	}
	return capPerShard
}

// shardIndex 用 FNV-1a 把 key 映射到分片（不引入额外分配）。
func shardIndex(key string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return h & shardMask
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

// RateLimitSpec 单个限流桶的预算（保留导出名，内部即 trafficlimit.Spec）。
type RateLimitSpec = trafficlimit.Spec

// RateLimitClassDefault 未归类路由的默认分类名（写操作）。
const RateLimitClassDefault = "default"

// RateLimitClassLogQuery 日志查询类路由的分类名（重后端负载：跨 Worker 扇出 + 缓存 + DB 解析）。
const RateLimitClassLogQuery = "log_query"

// RateLimitClassRead 只读请求（GET/HEAD/OPTIONS）的分类名。
//
// 为什么把只读单独拆成一类：管理台一页首屏本身就是十几次扇出（实例搜索、聚合、节点、
// 群组、拓扑、通知、任务…），实测单页首秒峰值已达 16 次/秒。多标签页同开时同一 IP 的
// 合法扇出会相乘——实测 3 标签页 30 次 429、6 标签页 131 次 429。这不是异常流量，
// 而是「读」与「写」共用一份预算造成的误伤；启停、改配置这类写操作才必须严格限制。
const RateLimitClassRead = "read"

// RateLimitClassOfFunc 由请求解析所属限流分类，返回空串表示默认分类。
type RateLimitClassOfFunc func(*gin.Context) string

// RateLimitPrefixRule 路由前缀 → 分类名的映射规则。
type RateLimitPrefixRule struct {
	Prefix string
	Class  string
}

// RateLimitClassByPrefix 生成按路由模板前缀分类的函数。
//
// 用 c.FullPath()（路由树模板）而非原始 URL：模板只可能取自注册表，
// 匹配不到即归默认类，未注册路径无法靠拼接 URL 自造分类。
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

// RateLimitClassByMethod 在既有前缀分类之上，把未被前缀规则命中的只读请求归入 read 类。
//
// 前缀优先：日志查询等重后端路由即便方法是 GET 也仍计入自己的分类。
// 判定只看请求方法与路由模板（非用户可控字符串），分类数仍是有界常量。
func RateLimitClassByMethod(base RateLimitClassOfFunc) RateLimitClassOfFunc {
	return func(c *gin.Context) string {
		if base != nil {
			if cls := base(c); cls != "" && cls != RateLimitClassDefault {
				return cls
			}
		}
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return RateLimitClassRead
		}
		return RateLimitClassDefault
	}
}

// RateLimiters 是限流中间件背后的限流器集合，暴露给设置服务做运行期热更新。
//
// 为什么要把「一组限流器」做成独立类型：预算可在运行期修改，而组装中间件时需要一个
// 稳定句柄（路由表已建好，不能重建中间件）。
type RateLimiters struct {
	// limiter 承载三个分类桶（桶键含分类名），global 承载跨分类总量。
	limiter  *RateLimiter
	global   *RateLimiter
	classOf  RateLimitClassOfFunc
	settings atomic.Pointer[trafficlimit.Settings]
	// exemptPrefixes 免限流路由前缀（自救通道），见 isRateLimitExempt。
	exemptPrefixes []string
}

// Apply 热更新全部预算（设置保存后调用）。
//
// 只换一次不可变快照指针：在途请求要么看到旧规格、要么看到新规格，
// 不会出现「读已放宽、写仍收紧」的撕裂，也不需要改动任何桶状态。
func (l *RateLimiters) Apply(s trafficlimit.Settings) {
	normalized := s.Normalize()
	copied := normalized
	l.settings.Store(&copied)
}

// Current 返回当前生效预算。
func (l *RateLimiters) Current() trafficlimit.Settings {
	if s := l.settings.Load(); s != nil {
		return *s
	}
	return trafficlimit.Defaults()
}

// NewRateLimiters 构造限流器集合（路由装配与设置服务共用）。
func NewRateLimiters(classOf RateLimitClassOfFunc, settings trafficlimit.Settings) *RateLimiters {
	normalized := settings.Normalize()
	l := &RateLimiters{
		classOf:        classOf,
		limiter:        NewRateLimiter(nil),
		global:         NewRateLimiter(nil),
		exemptPrefixes: defaultRateLimitExemptPrefixes,
	}
	copied := normalized
	l.settings.Store(&copied)
	return l
}

// SetExemptPrefixes 覆盖免限流前缀（nil/空 = 不豁免任何路由）。
//
// 供测试与特殊部署使用；生产默认豁免平台设置读写（自救通道）。
func (l *RateLimiters) SetExemptPrefixes(prefixes []string) {
	l.exemptPrefixes = append([]string(nil), prefixes...)
}

// RateLimitExemptRule 免限流路由前缀（自救与管理通道）。
//
// 为什么需要：限流预算是可运行期修改的，一旦管理员把预算调得过小（例如只读 1/s），
// 连「把它改回来」的那次 PUT /api/v1/settings 也会被 429 拦下 —— 实测复现过：
// 调小后紧接着的恢复请求直接 429，形成「越限越改不回来」的死锁，只能重启或改库。
// 限流是用来保护后端的，不该把唯一的自救通道也锁死。
//
// 边界（避免把豁免做成后门）：
//   - 只豁免精确前缀，不做通配；
//   - 仍然要求 JWT 与 settings.write 权限（鉴权在 protected 组，不在本中间件）；
//   - 只放行「读设置」与「写设置」两个端点，别的管理端点照旧受限。
type RateLimitExemptRule struct {
	Prefix string
}

// 自救通道前缀：平台设置（含入口限流自身）的读写。
var defaultRateLimitExemptPrefixes = []string{
	"/api/v1/settings",
}

// isRateLimitExempt 报告该请求是否命中免限流路由。
//
// 判定同样只看 c.FullPath()（路由模板），未注册路径不会被误豁免。
func isRateLimitExempt(c *gin.Context, rules []string) bool {
	if len(rules) == 0 {
		return false
	}
	fp := c.FullPath()
	if fp == "" {
		return false
	}
	for _, prefix := range rules {
		if prefix != "" && strings.HasPrefix(fp, prefix) {
			return true
		}
	}
	return false
}

// specForClass 返回某分类当前生效预算。
func specForClass(s trafficlimit.Settings, class string) trafficlimit.Spec {
	switch class {
	case RateLimitClassRead:
		return s.Read
	case RateLimitClassLogQuery:
		return s.LogQuery
	default:
		return s.Write
	}
}

// RateLimitByRouteClass 按「路由分类 × 客户端 IP」分桶的限流中间件，保留跨分类的每 IP 总量上限。
//
// 预算来自 trafficlimit.Settings，可运行期热更新；分类桶之间、以及与全局桶之间相互独立，
// 每 IP 实际放行量 = min(全局, Σ分类)。
//
// 判定顺序：先全局、再分类。全局被拒即返回，不占用分类预算——保证「总量超限」这一事实
// 不会被某个分类的剩余额度掩盖。
func RateLimitByRouteClass(l *RateLimiters) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 自救通道优先：限流预算的读写端点永不被限流拦住。
		// 否则把预算调小之后，连「改回来」的那次请求都会 429（实测复现的死锁）。
		if isRateLimitExempt(c, l.exemptPrefixes) {
			c.Next()
			return
		}

		settings := l.Current()
		ip := c.ClientIP()

		// 全局桶被显式关闭时跳过全局判定，不做任何分片锁操作。
		if !settings.Global.Unlimited() {
			globalSpec := settings.Global
			if retryAfter, ok := l.global.allowWithSpec(ip, &globalSpec); !ok {
				writeRateLimited(c, retryAfter)
				return
			}
		}

		class := RateLimitClassDefault
		if l.classOf != nil {
			if v := l.classOf(c); v != "" {
				class = v
			}
		}

		spec := specForClass(settings, class)
		if spec.Unlimited() {
			c.Next()
			return
		}

		// 分类桶键含分类名：同一分类内严格 per-IP 计数（跨分类才相互独立）。
		if retryAfter, ok := l.limiter.allowWithSpec(class+"\x1f"+ip, &spec); !ok {
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
		"error":   "RATE_LIMITED",
		"message": "请求过于频繁，请稍后重试",
	})
}
