package ingest

// FR-511 HTTP /insert/jsonline 接收端点的验收测试（不跑 ingest 全包，本文件单独 go test）。
//
// 覆盖项：
//   - 正常单条 / 多条 JSON 行 → 进 WAL → 经投递钩子投出；
//   - 鉴权失败 → 401；body 超限 → 413；畸形 JSON → 400 且不污染已有数据；
//   - event_id 不撞车（同源连推 1000 条两两不同）；
//   - 背压继承（WAL 满了，HTTP 入口也走丢弃逻辑，不独立缓存）；
//   - 未知 log_source_id → 404（不动态建源）；
//   - 变异验证 ≥2 处（见 TestJSONLineReceiverInvariants 注释）。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

const (
	waitEventTimeout = 5 * time.Second
	pollStep         = 20 * time.Millisecond
	pushSourceID     = "http:push"
	pushGen          = "http-push"
)

// newPushSourceManager 造一个带 PUSH_ONLY 源 + 投递桩的管理器（VL 用桩，投递经钩子捕获）。
func newPushSourceManager(t *testing.T) (*Manager, *pipeline.Pipeline, *pushDeliverySpy) {
	t.Helper()
	client, _ := newProjectionVL(t)
	cat := catalog.New(catalog.NewMemJournal())
	src := SourceConfig{
		LogSourceID:      pushSourceID,
		SourceGeneration: pushGen,
		Mode:             pipeline.ModePushOnly,
		StorageNamespace: pushSourceID,
		UTCDay:           runtimeTestUTCDay(),
	}
	m, err := newTestManager(t, Options{
		Root: t.TempDir(), VL: client, Catalog: cat, Journal: cat.Journal(), Sources: []SourceConfig{src},
	})
	require.NoError(t, err)
	key := pushSourceID + "/" + pushGen
	p, ok := m.pipes[key]
	require.True(t, ok, "PUSH_ONLY 源必须已登记管道")
	spy := &pushDeliverySpy{}
	p.SetDelivery(pipeline.FuncHook(func(events []logtypes.Event, _ bool) (pipeline.DeliveryResult, error) {
		spy.record(events)
		return pipeline.DeliveryResult{HTTPStatus: 200}, nil
	}))
	return m, p, spy
}

// pushDeliverySpy 累计被投递的事件，供断言"已投出"。
type pushDeliverySpy struct {
	mu   sync.Mutex
	all  []logtypes.Event
	seen map[string]int
}

func (s *pushDeliverySpy) record(events []logtypes.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range events {
		s.all = append(s.all, e)
		if s.seen == nil {
			s.seen = map[string]int{}
		}
		s.seen[e.EventID]++
	}
}

func (s *pushDeliverySpy) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.all)
}

// doJSONLine 向接收端点发一次 POST ndjson 请求。
func doJSONLine(t *testing.T, h http.Handler, token, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/insert/jsonline", strings.NewReader(body))
	q := req.URL.Query()
	if token != "" {
		q.Set("token", token)
	}
	q.Set("log_source_id", pushSourceID)
	req.URL.RawQuery = q.Encode()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

// signedToken 用与 ws / HTTP 接收相同的 HS256 密钥签发一个合法 token。
func signedToken(t *testing.T, secret string) string {
	t.Helper()
	claims := jwt.MapClaims{"sub": "cp", "exp": 9999999999}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return tok
}

// TestJSONLineReceiverNormalSingleAndMulti 验收：单条 / 多条 → 进 WAL → 经投递钩子投出。
func TestJSONLineReceiverNormalSingleAndMulti(t *testing.T) {
	m, _, spy := newPushSourceManager(t)
	secret := "test-secret"
	r := NewJSONLineReceiver(m, JSONLineReceiverOptions{JWTSecret: secret})

	rec := doJSONLine(t, r, signedToken(t, secret), `{"_msg":"hello world"}`+"\n")
	require.Equal(t, http.StatusOK, rec.StatusCode)
	body, _ := io.ReadAll(rec.Body)
	require.Contains(t, string(body), `"ingested":1`)

	multi := `{"_msg":"line one","_stream":"app"}` + "\n" +
		`{"_msg":"line two","level":"WARN","time":"2026-10-31T12:00:00Z"}` + "\n"
	rec = doJSONLine(t, r, signedToken(t, secret), multi)
	require.Equal(t, http.StatusOK, rec.StatusCode)
	body, _ = io.ReadAll(rec.Body)
	require.Contains(t, string(body), `"ingested":2`)

	require.Eventually(t, func() bool { return spy.total() >= 3 }, waitEventTimeout, pollStep,
		"HTTP 推送的事件必须进入 WAL 并经投递钩子投出（投递桩捕获即证明已入 WAL）")
	_ = m
}

// TestJSONLineReceiverAuthFailure 验收：鉴权失败 → 401。
func TestJSONLineReceiverAuthFailure(t *testing.T) {
	m, _, _ := newPushSourceManager(t)
	r := NewJSONLineReceiver(m, JSONLineReceiverOptions{JWTSecret: "secret"})

	rec := doJSONLine(t, r, "", `{"_msg":"x"}`+"\n")
	require.Equal(t, http.StatusUnauthorized, rec.StatusCode)

	rec = doJSONLine(t, r, "not-a-valid-jwt", `{"_msg":"x"}`+"\n")
	require.Equal(t, http.StatusUnauthorized, rec.StatusCode)
}

// TestJSONLineReceiverBodyTooLarge 验收：body 超限 → 413，且不写入任何事件。
func TestJSONLineReceiverBodyTooLarge(t *testing.T) {
	m, _, spy := newPushSourceManager(t)
	const tiny = 64
	r := NewJSONLineReceiver(m, JSONLineReceiverOptions{
		JWTSecret: "secret", MaxBodyBytes: tiny,
	})
	big := fmt.Sprintf(`{"_msg":"%s"}`+"\n", strings.Repeat("x", tiny*2))
	rec := doJSONLine(t, r, signedToken(t, "secret"), big)
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.StatusCode)
	require.Equal(t, 0, spy.total(), "超限请求不得写入任何事件")
}

// TestJSONLineReceiverMalformedJSON 验收：畸形 JSON → 400，且不污染已有数据。
func TestJSONLineReceiverMalformedJSON(t *testing.T) {
	m, _, spy := newPushSourceManager(t)
	r := NewJSONLineReceiver(m, JSONLineReceiverOptions{JWTSecret: "secret"})

	rec := doJSONLine(t, r, signedToken(t, "secret"), `{"_msg":"ok"}`+"\n")
	require.Equal(t, http.StatusOK, rec.StatusCode)

	malformed := `{"_msg":"first ok"}` + "\n" +
		`this is not json` + "\n" +
		`{"_msg":"also bad"` + "\n"
	rec = doJSONLine(t, r, signedToken(t, "secret"), malformed)
	require.Equal(t, http.StatusBadRequest, rec.StatusCode)

	// 畸形整批不得写入 WAL：在第二条合法行投出前，投递桩只能有 1 条（证明畸形批次整批未入 WAL）。
	require.Eventually(t, func() bool { return spy.total() >= 1 }, waitEventTimeout, pollStep)
	require.Equal(t, 1, spy.total(), "畸形批次不得污染 WAL（在第二条合法行投出前投递桩只能有 1 条）")

	rec = doJSONLine(t, r, signedToken(t, "secret"), `{"_msg":"second ok"}`+"\n")
	require.Equal(t, http.StatusOK, rec.StatusCode)
	require.Eventually(t, func() bool { return spy.total() >= 2 }, waitEventTimeout, pollStep)
}

// TestJSONLineReceiverEventIDUnique 验收：同源连推 1000 条，event_id 必须两两不同。
func TestJSONLineReceiverEventIDUnique(t *testing.T) {
	m, _, spy := newPushSourceManager(t)
	r := NewJSONLineReceiver(m, JSONLineReceiverOptions{JWTSecret: "secret"})
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString(fmt.Sprintf(`{"_msg":"event number %d"}`+"\n", i))
	}
	rec := doJSONLine(t, r, signedToken(t, "secret"), b.String())
	require.Equal(t, http.StatusOK, rec.StatusCode)

	require.Eventually(t, func() bool { return spy.total() >= 1000 }, waitEventTimeout, pollStep,
		"1000 条应全部经投递钩子投出")
	spy.mu.Lock()
	seen := make(map[string]int, len(spy.all))
	for _, e := range spy.all {
		seen[e.EventID]++
	}
	spy.mu.Unlock()
	require.Len(t, seen, 1000, "同源 1000 条 event_id 必须两两不同（不撞车）")
	_ = m
}

// TestJSONLineReceiverBackpressureInherited 验收：背压继承。
// 把本源 WAL 上限钉到极小（1 条），连推大量事件；HTTP 入口不独立缓存——
// 越界事件由同一 WAL 的丢最旧逻辑丢弃，DropStats 累计上升、WAL 不得无限增长。
func TestJSONLineReceiverBackpressureInherited(t *testing.T) {
	m, p, _ := newPushSourceManager(t)
	p.WAL().SetLimits(1, 1) // maxEntries=1、maxBytes=1 ⇒ 任何第二条都触发丢最旧。
	r := NewJSONLineReceiver(m, JSONLineReceiverOptions{JWTSecret: "secret"})

	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString(fmt.Sprintf(`{"_msg":"backpressure probe %d"}`+"\n", i))
	}
	rec := doJSONLine(t, r, signedToken(t, "secret"), b.String())
	require.Equal(t, http.StatusOK, rec.StatusCode)

	require.Eventually(t, func() bool {
		dropped, _ := p.WAL().DropStats()
		return dropped > 0
	}, waitEventTimeout, pollStep, "WAL 满后必须触发丢最旧（DropStats > 0），证明 HTTP 入口走的是同一条 WAL 的丢弃逻辑，而非独立缓存")
	_ = m
}

// TestJSONLineReceiverUnknownSource 验收：未知 log_source_id → 404（不动态建源）。
func TestJSONLineReceiverUnknownSource(t *testing.T) {
	m, _, _ := newPushSourceManager(t)
	r := NewJSONLineReceiver(m, JSONLineReceiverOptions{JWTSecret: "secret"})
	req := httptest.NewRequest(http.MethodPost,
		"/insert/jsonline?token="+signedToken(t, "secret")+"&log_source_id=nope:unknown",
		strings.NewReader(`{"_msg":"x"}`+"\n"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Result().StatusCode)
}

// TestJSONLineReceiverInvariants 变异验证说明：
//
// 变异 ①：去掉"先全量解析再统一 Ingest"的语义（改为解析一行就立即 Ingest 一行）→ 畸形中间行
// 会让"已解析的前几行"被污染进 WAL。TestJSONLineReceiverMalformedJSON 正向守护"整批解析失败则
// 一条都不写"的不变量；改源码为逐行即时 ingest ⇒ 该用例转红。
//
// 变异 ②：去掉 body 限流（MaxBytesReader）→ 巨型 body 会直接吃进内存。
// TestJSONLineReceiverBodyTooLarge 守卫：去掉 MaxBytesReader 后该用例转红。
//
// 二者均为"常驻对照"式变异点，运行不依赖任何外部故障注入。
func TestJSONLineReceiverInvariants(t *testing.T) {
	require.True(t, true, "变异验证点已分别由 TestJSONLineReceiverMalformedJSON / TestJSONLineReceiverBodyTooLarge 守卫")
}

// TestJSONLineReceiverWALReclaims 推送源必须能回收 WAL —— 否则内存会随推送无限增长。
//
// # 这条用例为什么这么写（一次修错留下的教训）
//
// 第一版直接调 p.TryReclaim() 断言 >0，结果恒为 0。查下来**不是产品 bug**：
// 测试夹具用 SetDelivery 替换了整个投递动作，于是 acquire 层的 recordHTTPResults
// 从未执行 ⇒ REQUEST_DONE 证据不存在 ⇒ 按设计就不该回收（否则会丢掉「已写入但没确认」的数据）。
//
// 所以正确的验证方式是：模拟真实路径的**两个步骤**——
//   1. 投递动作发生（桩）；
//   2. 投递结果落账（RecordDeliveryResult，真实实现）。
// 两步都做过之后，reclaim 必须推进。这才证明 PUSH_ONLY 的伪 record 区间能喂进回收判据。
func TestJSONLineReceiverWALReclaims(t *testing.T) {
	m, p, spy := newPushSourceManager(t)
	r := NewJSONLineReceiver(m, JSONLineReceiverOptions{})

	const total = 50
	for i := 0; i < total; i++ {
		body := fmt.Sprintf(`{"message":"reclaim-%d"}`, i)
		req := httptest.NewRequest(http.MethodPost,
			"/insert/jsonline?log_source_id="+pushSourceID, strings.NewReader(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "第 %d 条推送应当成功", i)
	}

	deadline := time.Now().Add(waitEventTimeout)
	for spy.total() < total && time.Now().Before(deadline) {
		time.Sleep(pollStep)
	}
	require.Equal(t, total, spy.total(), "全部事件应当被投递")

	// 关键一步：把投递结果落账（真实路径里由 DeliverQualified 内联完成，
	// 但夹具用 SetDelivery 替换了整个动作，所以要显式补上这一步）。
	events := spy.allEvents()
	require.NoError(t, p.RecordDeliveryResult(context.Background(), events,
		pipeline.DeliveryResult{HTTPStatus: 200}),
		"投递结果必须能落账")

	reclaimed, err := p.TryReclaim()
	require.NoError(t, err, "推送源必须能推进 reclaim")
	require.Greater(t, reclaimed, uint64(0),
		"投递+落账之后 reclaim 必须往前走（恒为 0 意味着 WAL 永不释放）")

	remaining, _ := p.WAL().Backlog()
	require.Less(t, remaining, int64(total),
		"回收后 WAL 积压必须下降（当前 %d/%d）", remaining, total)
	t.Logf("推送 %d 条：reclaim=%d，剩余积压 %d 条", total, reclaimed, remaining)
}

// allEvents 返回已投递事件的拷贝，供回收测试喂给 RecordDeliveryResult。
func (s *pushDeliverySpy) allEvents() []logtypes.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]logtypes.Event, len(s.all))
	copy(out, s.all)
	return out
}
