package router

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/middleware"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 本文件覆盖 FR-522 的 SSE 端点契约与 FR-536 的连接治理（429 / 心跳 / 慢消费者 / 补读）。
// 用真实 httptest.Server 而不是 ResponseRecorder：SSE 是长连接，需要真正的流式写出与增量读取。

// sseStream 是一个已建立的事件流连接。
type sseStream struct {
	resp   *http.Response
	cancel context.CancelFunc
	reader *bufio.Reader
}

// Close 断开连接并释放资源。
func (s *sseStream) Close() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.resp != nil && s.resp.Body != nil {
		_ = s.resp.Body.Close()
	}
}

// sseFrame 是解析后的一帧：id/event/data 三者。
type sseFrame struct {
	ID    string
	Event string
	Data  string
}

// readFrame 读取下一帧（跳过注释行与空行）。返回 error 表示连接已关闭或超时。
func (s *sseStream) readFrame(t *testing.T, timeout time.Duration) (sseFrame, error) {
	t.Helper()
	type result struct {
		frame sseFrame
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		var f sseFrame
		for {
			line, err := s.reader.ReadString('\n')
			if err != nil {
				ch <- result{sseFrame{}, err}
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				if f.Event != "" || f.Data != "" {
					ch <- result{f, nil}
					return
				}
				continue
			}
			if strings.HasPrefix(line, ":") {
				continue // 注释行（占位/保活）
			}
			switch {
			case strings.HasPrefix(line, "id: "):
				f.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.Data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	select {
	case r := <-ch:
		return r.frame, r.err
	case <-time.After(timeout):
		return sseFrame{}, fmt.Errorf("等待帧超时（%s）", timeout)
	}
}

// withServer 起一个 httptest.Server 并返回其 base URL（测试结束自动关闭）。
func withServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

// buildStreamEngine 组装一个只挂业务事件流端点的最小引擎。
//
// 为什么不复用 setupTestRouter：本组用例需要控制缓冲容量与心跳间隔（默认 20 万条/20 秒），
// 用共享实例就得真的灌 20 万条或等 20 秒。鉴权行为由 TestBusinessStream_RouteRegistered 在
// **真实路由**上单独覆盖，这里用注入主体的桩中间件聚焦端点/治理语义。
func buildStreamEngine(t *testing.T, stream *service.BusinessStreamService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/api/v1")
	group.Use(func(c *gin.Context) {
		c.Set(middleware.CtxUserID, uint(1))
		c.Set(middleware.CtxUsername, "tester")
		c.Next()
	})
	NewBusinessStreamHandler(stream).RegisterRoutes(group)
	return engine
}

// openStream 建立一条到指定引擎的 SSE 连接。
func openStream(t *testing.T, baseURL, token string, query url.Values, lastEventID string) *sseStream {
	t.Helper()
	raw := baseURL + "/api/v1/business/stream"
	if len(query) > 0 {
		raw += "?" + query.Encode()
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return &sseStream{resp: resp, cancel: cancel, reader: bufio.NewReader(resp.Body)}
}

// emitBusinessEvent 注入一条业务事件（绕过 Worker 事件流，聚焦端点行为）。
func emitBusinessEvent(svc *service.BusinessStreamService, domain, topic string) {
	emitBusinessEventSized(svc, domain, topic, 0)
}

// emitBusinessEventSized 注入一条带指定载荷体积（字节）的业务事件。
//
// 体积可调是慢消费者用例的需要：客户端不读时，内核 socket 缓冲会先吸收若干百 KB，
// 只有把总量推过这个水位，「写阻塞 → 队列满 → 判为慢消费者」这条链路才会真实发生。
func emitBusinessEventSized(svc *service.BusinessStreamService, domain, topic string, payloadBytes int) {
	data := map[string]any{"player": "steve"}
	if payloadBytes > 0 {
		data["blob"] = strings.Repeat("x", payloadBytes)
	}
	raw, _ := json.Marshal(map[string]any{
		"type":   "event",
		"domain": domain,
		"topic":  topic,
		"data":   data,
	})
	svc.Ingest("node-main", &workerpb.PluginEvent{
		InstanceUuid: "inst-uuid",
		Type:         "activity_progress",
		Timestamp:    1730000000,
		Domain:       domain,
		DedupKey:     "e-" + topic,
		RawJson:      string(raw),
	})
}

// TestBusinessStream_RouteRegistered 在**真实路由**上验证端点存在 + 鉴权（契约：Agent Token / JWT）。
func TestBusinessStream_RouteRegistered(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	base := withServer(t, r)

	// 无凭据必须 401（不能因为未注册而落到 SPA 回退的 200/404）。
	resp, err := http.Get(base + "/api/v1/business/stream")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// 带 JWT 能建立流并拿到契约要求的响应头。
	stream := openStream(t, base, token, nil, "")
	defer stream.Close()
	require.Equal(t, http.StatusOK, stream.resp.StatusCode)
	require.Equal(t, "text/event-stream", stream.resp.Header.Get("Content-Type"))
	require.Equal(t, "no-cache", stream.resp.Header.Get("Cache-Control"))
	require.Equal(t, "keep-alive", stream.resp.Header.Get("Connection"))
	require.Equal(t, "no", stream.resp.Header.Get("X-Accel-Buffering"),
		"必须禁用代理缓冲，否则事件会成批延迟到达")
}

// TestBusinessStream_FrameFormat 帧格式与 id==seq（§3.2 / §8 验收 6）。
func TestBusinessStream_FrameFormat(t *testing.T) {
	svc := service.NewBusinessStreamService(nil, service.NewBusinessStreamBuffer(100, time.Minute))
	base := withServer(t, buildStreamEngine(t, svc))

	stream := openStream(t, base, "t", nil, "")
	defer stream.Close()

	emitBusinessEvent(svc, "activity", "activity.spring-2026")
	frame, err := stream.readFrame(t, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, "business.event", frame.Event)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(frame.Data), &payload))
	require.Equal(t, fmt.Sprintf("%v", payload["seq"]), frame.ID, "SSE 的 id 必须等于 seq")
	require.Equal(t, "activity", payload["domain"])
	require.Equal(t, "activity.spring-2026", payload["topic"])
	require.Equal(t, "node-main", payload["node"])
	require.Equal(t, "e-activity.spring-2026", payload["dedupKey"])
	require.NotNil(t, payload["payload"], "payload 必须原样透传（不解析、不丢弃）")
}

// TestBusinessStream_QueryFilter 订阅过滤：未匹配事件不下发（§8 验收 1/2）。
func TestBusinessStream_QueryFilter(t *testing.T) {
	svc := service.NewBusinessStreamService(nil, service.NewBusinessStreamBuffer(100, time.Minute))
	base := withServer(t, buildStreamEngine(t, svc))

	stream := openStream(t, base, "t", url.Values{"domains": {"activity"}}, "")
	defer stream.Close()

	emitBusinessEvent(svc, "economy", "economy.balance")
	emitBusinessEvent(svc, "activity", "activity.spring-2026")

	frame, err := stream.readFrame(t, 5*time.Second)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(frame.Data), &payload))
	require.Equal(t, "activity", payload["domain"], "economy 事件应被过滤")
}

// TestBusinessStream_ReplayWithinWindow Last-Event-ID 在窗口内可补齐（§8 验收 3）。
func TestBusinessStream_ReplayWithinWindow(t *testing.T) {
	svc := service.NewBusinessStreamService(nil, service.NewBusinessStreamBuffer(100, time.Minute))
	base := withServer(t, buildStreamEngine(t, svc))

	for i := 0; i < 3; i++ {
		emitBusinessEvent(svc, "activity", "activity.a")
	}

	stream := openStream(t, base, "t", nil, "1")
	defer stream.Close()

	for _, wantID := range []string{"2", "3"} {
		frame, err := stream.readFrame(t, 5*time.Second)
		require.NoError(t, err)
		require.Equal(t, "business.event", frame.Event)
		require.Equal(t, wantID, frame.ID, "应从 Last-Event-ID 之后补发")
	}
}

// TestBusinessStream_ReplayGapViaSince 窗口外重连返回 replay_gap 且含 oldestAvailableSeq（§8 验收 4）。
func TestBusinessStream_ReplayGapViaSince(t *testing.T) {
	buf := service.NewBusinessStreamBuffer(3, time.Minute)
	svc := service.NewBusinessStreamService(nil, buf)
	base := withServer(t, buildStreamEngine(t, svc))

	for i := 0; i < 6; i++ {
		emitBusinessEvent(svc, "activity", "activity.a")
	}
	require.Equal(t, int64(4), buf.OldestSeq(), "容量 3 写入 6 条后窗口应为 [4,6]")

	stream := openStream(t, base, "t", url.Values{"since": {"1"}}, "")
	defer stream.Close()

	frame, err := stream.readFrame(t, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, "replay_gap", frame.Event)

	var gap map[string]any
	require.NoError(t, json.Unmarshal([]byte(frame.Data), &gap))
	require.EqualValues(t, 1, gap["requestedSeq"])
	require.EqualValues(t, 4, gap["oldestAvailableSeq"])
	require.Equal(t, "BUFFER_EXPIRED", gap["reason"])

	// gap 之后必须从窗口起点补发（而不是静默丢弃）。
	next, err := stream.readFrame(t, 5*time.Second)
	require.NoError(t, err)
	require.Equal(t, "business.event", next.Event)
	require.Equal(t, "4", next.ID)
}

// TestBusinessStream_TooManyConnections 单 Token 超限返回 429 + Retry-After + 契约体（§8 验收 4）。
func TestBusinessStream_TooManyConnections(t *testing.T) {
	svc := service.NewBusinessStreamService(nil, service.NewBusinessStreamBuffer(10, time.Minute))
	base := withServer(t, buildStreamEngine(t, svc))

	var opened []*sseStream
	defer func() {
		for _, s := range opened {
			s.Close()
		}
	}()
	for i := 0; i < service.BusinessStreamMaxPerToken; i++ {
		s := openStream(t, base, "t", nil, "")
		require.Equal(t, http.StatusOK, s.resp.StatusCode, "第 %d 个连接应成功", i+1)
		opened = append(opened, s)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/business/stream", nil)
	over, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer over.Body.Close()

	require.Equal(t, http.StatusTooManyRequests, over.StatusCode)
	require.Equal(t, "5", over.Header.Get("Retry-After"))
	body, _ := io.ReadAll(over.Body)
	var detail map[string]any
	require.NoError(t, json.Unmarshal(body, &detail))
	require.Equal(t, "TOO_MANY_CONNECTIONS", detail["code"])
	require.EqualValues(t, service.BusinessStreamMaxPerToken, detail["limit"])
}

// TestBusinessStream_Heartbeat 心跳按配置间隔发送（§8 验收 5）。
func TestBusinessStream_Heartbeat(t *testing.T) {
	svc := service.NewBusinessStreamService(nil, service.NewBusinessStreamBuffer(10, time.Minute))
	svc.SetHeartbeatInterval(120 * time.Millisecond)
	base := withServer(t, buildStreamEngine(t, svc))

	stream := openStream(t, base, "t", nil, "")
	defer stream.Close()

	frame, err := stream.readFrame(t, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, "ping", frame.Event)
	var ping map[string]any
	require.NoError(t, json.Unmarshal([]byte(frame.Data), &ping))
	require.NotNil(t, ping["t"], "心跳必须带服务端时间戳")
	require.GreaterOrEqual(t, svc.Metrics().HeartbeatSent, uint64(1))
}

// TestBusinessStream_SlowConsumerGetErrorFrame 慢消费者终止帧的内容契约（§3.3 / §8 验收 2）。
//
// 这一层用**受控 writer 直接调用收尾逻辑**验证「被判慢消费者时确实会补发 SLOW_CONSUMER 错误帧」，
// 而不是靠真实 TCP 背压去赌这一帧能否送达：真实 socket 缓冲满时，任何终止帧都必须等对端把内核
// 缓冲读走一部分才写得出去——那是 TCP 的性质，不是实现的缺陷（实测灌满后连接被正常断开，
// 而 error 帧当时确实写不出去，这正是规格「最后努力」的语义）。
//
// 真实连接下的断开行为与「不影响其他订阅者」由 TestBusinessStream_SlowConsumerDisconnectedUnderLoad 覆盖。
func TestBusinessStream_SlowConsumerGetErrorFrame(t *testing.T) {
	svc := service.NewBusinessStreamService(nil, service.NewBusinessStreamBuffer(2000, time.Minute))
	sub, err := svc.Subscribe("user:1", service.BusinessStreamFilter{})
	require.NoError(t, err)

	// 灌满订阅者队列 → 走服务真实路径「队列满 → 标记 slow + halt」。
	for i := 0; i < service.BusinessSubscriberQueueSize+1; i++ {
		emitBusinessEventSized(svc, "activity", "activity.a", 64)
	}
	require.True(t, sub.SlowConsumer(), "队列打满后应被标记为慢消费者")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	NewBusinessStreamHandler(svc).finish(c, http.NewResponseController(rec), sub)

	body := rec.Body.String()
	require.Contains(t, body, "event: error")
	require.Contains(t, body, service.SSEErrorSlowConsumer)
	require.Contains(t, body, "Last-Event-ID", "错误帧应提示重连带位点")
}

// TestBusinessStream_FinishSilentForNormalConnection 正常断开不发错误帧（避免噪音）。
func TestBusinessStream_FinishSilentForNormalConnection(t *testing.T) {
	svc := service.NewBusinessStreamService(nil, service.NewBusinessStreamBuffer(10, time.Minute))
	sub, err := svc.Subscribe("user:1", service.BusinessStreamFilter{})
	require.NoError(t, err)
	svc.Unsubscribe(sub)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	NewBusinessStreamHandler(svc).finish(c, http.NewResponseController(rec), sub)
	require.Empty(t, rec.Body.String(), "非慢消费的断开不应写终止帧")
}

// TestBusinessStream_SlowConsumerDisconnectedUnderLoad 真实 socket 背压下慢消费者被断开，且不影响其他订阅者与主链路。
//
// 只断言**可稳定观测**的三件事（不断言终止帧送达，理由见上一个用例）：
//  1. 慢连接被判为 SLOW_CONSUMER 并被移除；
//  2. 健康连接仍能持续收到事件（主链路与其他订阅者不被阻塞）；
//  3. 服务端不挂起：所有 Ingest 立即返回。
func TestBusinessStream_SlowConsumerDisconnectedUnderLoad(t *testing.T) {
	svc := service.NewBusinessStreamService(nil, service.NewBusinessStreamBuffer(200_000, time.Minute))
	srv := httptest.NewServer(buildStreamEngine(t, svc))
	defer srv.Close()

	// 慢连接：裸 TCP 且完全不读，接收窗口很快被填满。
	slowConn := openRawSSEConn(t, srv.URL)
	defer slowConn.Close()
	emitBusinessEvent(svc, "activity", "activity.warmup")
	require.NoError(t, readRawFrameHeader(slowConn, 5*time.Second), "订阅应已建立")

	// 健康连接：正常读取。
	healthy := openStream(t, srv.URL, "t", nil, "")
	defer healthy.Close()
	require.Eventually(t, func() bool { return svc.Metrics().Connections >= 2 }, 2*time.Second, 10*time.Millisecond)

	// 灌入直到慢连接被断开；期间健康连接必须能跟上（主链路不阻塞的证据）。
	healthyReceived := make(chan int, 1)
	go func() {
		count := 0
		for {
			if _, err := healthy.readFrame(t, 30*time.Second); err != nil {
				healthyReceived <- count
				return
			}
			count++
			if count > 200 {
				healthyReceived <- count
				return
			}
		}
	}()

	for i := 0; i < 20_000; i++ {
		emitBusinessEventSized(svc, "activity", "activity.a", 16384)
		if svc.Metrics().SlowConsumerDisconnects >= 1 {
			break
		}
	}
	require.Eventually(t, func() bool {
		return svc.Metrics().SlowConsumerDisconnects >= 1
	}, 30*time.Second, 20*time.Millisecond, "慢消费者应被断开")

	// 健康订阅者必须真的收到了事件（不是被动挂住）。
	select {
	case n := <-healthyReceived:
		require.Greater(t, n, 0, "健康订阅者应收到事件")
	case <-time.After(30 * time.Second):
		t.Fatal("健康订阅者未在预期时间内收到任何事件")
	}
}

// openRawSSEConn 用裸 TCP 建立一条「只发不收」的 SSE 连接，返回已写完请求的连接。
func openRawSSEConn(t *testing.T, baseURL string) *net.TCPConn {
	t.Helper()
	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	conn, err := net.Dial("tcp", u.Host)
	require.NoError(t, err)
	tcpConn, ok := conn.(*net.TCPConn)
	require.True(t, ok, "期望 TCP 连接")
	// 收紧接收窗口：让服务端尽快遇到写阻塞，用例不必灌上百 MB。
	require.NoError(t, tcpConn.SetReadBuffer(4096))
	request := "GET /api/v1/business/stream HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Accept: text/event-stream\r\n" +
		"\r\n"
	_, err = tcpConn.Write([]byte(request))
	require.NoError(t, err)
	return tcpConn
}

// readRawFrameHeader 读到首帧出现即返回（确认订阅已建立），之后不再消费。
func readRawFrameHeader(conn net.Conn, timeout time.Duration) error {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("未读到任何数据")
	}
	return nil
}
