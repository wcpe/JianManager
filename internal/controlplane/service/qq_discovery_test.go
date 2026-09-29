package service

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newQQDiscoveryTestDB 建 QQ 发现测试库（含通道表 + 两张新表）。
func newQQDiscoveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newAlertTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.QQDiscoveredGroup{}, &model.QQGatewayConnection{}))
	return db
}

// qqDiscoveryTestChannel 插一条启用 qq 通道（appSecret 明文，发送前按明文兼容解析）。
func qqDiscoveryTestChannel(t *testing.T, db *gorm.DB, appID, secret, baseURL string) {
	t.Helper()
	raw, _ := json.Marshal(ChannelConfig{
		AppID: appID, AppSecret: secret,
		TargetType: qqTargetGroup, TargetID: "openid-group", BaseURL: baseURL,
	})
	require.NoError(t, db.Create(&model.AlertChannel{
		Name: "qq-" + appID, Type: model.ChannelTypeQQ, Enabled: true, Config: string(raw),
	}).Error)
}

// ── 事件解析与 upsert 去重 ──

// TestQQDiscovery_入群事件解析缺字段报错 事件体缺 group_openid 必须报错。
func TestQQDiscovery_入群事件解析缺字段报错(t *testing.T) {
	// 正常事件解析成功。
	ev, err := parseGroupAddRobot(json.RawMessage(`{"group_openid":"g1","op_member_openid":"u1","timestamp":1700000000}`))
	require.NoError(t, err)
	assert.Equal(t, "g1", ev.GroupOpenID)
	assert.Equal(t, "u1", ev.OpMemberOpenID)
	assert.Equal(t, int64(1700000000), ev.Timestamp)

	// 缺 group_openid 报错。
	_, err = parseGroupAddRobot(json.RawMessage(`{"op_member_openid":"u1"}`))
	require.Error(t, err)

	// 非法 JSON 报错。
	_, err = parseGroupAddRobot(json.RawMessage(`{bad`))
	require.Error(t, err)
}

// TestQQDiscovery_重复进群不产生重复条目 upsert 去重：同一 group_openid 只留一条。
func TestQQDiscovery_重复进群不产生重复条目(t *testing.T) {
	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())

	first := time.Unix(1700000000, 0)
	require.NoError(t, svc.RecordGroupEvent("app1", &qqGroupAddRobot{
		GroupOpenID: "g1", OpMemberOpenID: "u1",
	}, first))

	// 同一群重复进群：不新增，只刷新 last_seen_at。
	second := time.Unix(1700003600, 0)
	require.NoError(t, svc.RecordGroupEvent("app1", &qqGroupAddRobot{
		GroupOpenID: "g1", OpMemberOpenID: "u2",
	}, second))

	var count int64
	require.NoError(t, db.Model(&model.QQDiscoveredGroup{}).Count(&count).Error)
	assert.Equal(t, int64(1), count, "重复进群不应产生重复条目")

	var g model.QQDiscoveredGroup
	require.NoError(t, db.Where("group_open_id = ?", "g1").First(&g).Error)
	assert.True(t, g.FirstSeenAt.Equal(first), "首次发现时间不应被覆盖")
	assert.True(t, g.LastSeenAt.Equal(second), "最近发现时间应刷新")
	assert.Equal(t, "u2", g.OpMemberOpenID)

	// 不同群新增第二条。
	require.NoError(t, svc.RecordGroupEvent("app1", &qqGroupAddRobot{GroupOpenID: "g2"}, time.Now()))
	require.NoError(t, db.Model(&model.QQDiscoveredGroup{}).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}

// TestQQDiscovery_非入群事件收下即丢 其余事件类型不落库。
func TestQQDiscovery_非入群事件收下即丢(t *testing.T) {
	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())

	svc.handleDispatch("GROUP_AT_MESSAGE_CREATE", json.RawMessage(`{"group_openid":"g9"}`))
	svc.handleDispatch("FRIEND_ADD", json.RawMessage(`{}`))

	var count int64
	require.NoError(t, db.Model(&model.QQDiscoveredGroup{}).Count(&count).Error)
	assert.Equal(t, int64(0), count, "非入群事件不应落库")

	// 入群事件正常落库。
	svc.handleDispatch("GROUP_ADD_ROBOT", json.RawMessage(`{"group_openid":"g1","op_member_openid":"u1","timestamp":1700000000}`))
	require.NoError(t, db.Model(&model.QQDiscoveredGroup{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// ── 凭证解析（方案 A） ──

// TestQQDiscovery_默认机器人取首个启用通道 方案 A：首个启用 qq 通道的 appId 即默认机器人。
func TestQQDiscovery_默认机器人取首个启用通道(t *testing.T) {
	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())

	// 无通道时报错。
	_, err := svc.ResolveCredentials()
	require.Error(t, err)

	qqDiscoveryTestChannel(t, db, "app-first", "s1", "")
	qqDiscoveryTestChannel(t, db, "app-second", "s2", "")

	cred, err := svc.ResolveCredentials()
	require.NoError(t, err)
	assert.Equal(t, "app-first", cred.AppID, "默认机器人应为首个启用通道")
	assert.Equal(t, "s1", cred.AppSecret)
	assert.Equal(t, qqDefaultAPIBase, cred.BaseURL)

	// 指定 appId 可选中第二个。
	cred2, err := svc.ResolveCredentialsFor("app-second")
	require.NoError(t, err)
	assert.Equal(t, "app-second", cred2.AppID)

	// 不存在的 appId 报错。
	_, err = svc.ResolveCredentialsFor("app-missing")
	require.Error(t, err)
}

// TestQQDiscovery_密钥经ENV引用解析 appSecret 的 ${ENV} 引用在服务端解析，绝不下发前端。
func TestQQDiscovery_密钥经ENV引用解析(t *testing.T) {
	t.Setenv("JM_TEST_QQ_DISCOVERY_SECRET", "secret-from-env")
	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())

	raw, _ := json.Marshal(ChannelConfig{
		AppID: "app-env", AppSecret: "${JM_TEST_QQ_DISCOVERY_SECRET}",
		TargetType: qqTargetGroup, TargetID: "g1",
	})
	require.NoError(t, db.Create(&model.AlertChannel{
		Name: "qq-env", Type: model.ChannelTypeQQ, Enabled: true, Config: string(raw),
	}).Error)

	cred, err := svc.ResolveCredentials()
	require.NoError(t, err)
	assert.Equal(t, "secret-from-env", cred.AppSecret, "服务端应解析出明文密钥")
}

// ── 分享链接 ──

// qqShareLinkMock 分享/取 token 假服务端：记录分享请求的形状。
type qqShareLinkMock struct {
	srv *httptest.Server
	mu  sync.Mutex

	gotPath   string
	gotAuth   string
	gotBody   map[string]interface{}
	shareResp string
	shareCode int
}

func newQQShareLinkMock(t *testing.T) *qqShareLinkMock {
	t.Helper()
	m := &qqShareLinkMock{shareResp: `{"data":{"url":"https://qun.qq.com/qunpro/robot/qunshare?x=1"}}`, shareCode: http.StatusOK}
	m.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app/getAppAccessToken" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token":"mock-token","expires_in":7200}`))
			return
		}
		m.mu.Lock()
		m.gotPath = r.URL.Path
		m.gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&m.gotBody)
		resp, code := m.shareResp, m.shareCode
		m.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(m.srv.Close)
	return m
}

// TestQQDiscovery_分享链接请求形状 路径/鉴权头/callback_data 格式断言。
func TestQQDiscovery_分享链接请求形状(t *testing.T) {
	m := newQQShareLinkMock(t)
	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())
	qqDiscoveryTestChannel(t, db, "1020001", "plain", m.srv.URL)

	url, err := svc.GenerateShareLink("")
	require.NoError(t, err)
	assert.Contains(t, url, "qun.qq.com")

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Equal(t, "/v2/generate_url_link", m.gotPath, "分享链接应走官方路径")
	assert.Equal(t, "QQBot mock-token", m.gotAuth, "鉴权头应为 QQBot {token}")
	assert.Equal(t, "jm-1020001", m.gotBody["callback_data"], "callback_data 应为 jm-<连接ID> 格式（冒号会被官方拒绝）")
}

// TestQQDiscovery_分享链接失败判定 HTTP 状态 + code 都要判（checkQQResponse 同款语义）。
func TestQQDiscovery_分享链接失败判定(t *testing.T) {
	cases := []struct {
		name  string
		code  int
		resp  string
		check func(t *testing.T, err error)
	}{
		{"HTTP非2xx失败", http.StatusBadGateway, `{"message":"bad"}`, func(t *testing.T, err error) {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "502")
		}},
		{"业务code非0失败", http.StatusOK, `{"code":40034100,"message":"频率限制"}`, func(t *testing.T, err error) {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "40034100")
		}},
		{"缺url失败", http.StatusOK, `{"data":{}}`, func(t *testing.T, err error) {
			require.Error(t, err)
			assert.Contains(t, err.Error(), "url")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newQQShareLinkMock(t)
			m.shareCode = tc.code
			m.shareResp = tc.resp
			db := newQQDiscoveryTestDB(t)
			svc := NewQQDiscoveryService(db, NewChannelNotifier())
			qqDiscoveryTestChannel(t, db, "1020001", "plain", m.srv.URL)
			_, err := svc.GenerateShareLink("")
			tc.check(t, err)
		})
	}
}

// TestQQDiscovery_callback超长被拒 callback_data 超 32 字符必须报错。
func TestQQDiscovery_callback超长被拒(t *testing.T) {
	_, err := buildCallbackData(strings.Repeat("x", 30))
	require.Error(t, err, "jm- + 30 字符 = 33 > 32 应被拒")

	cb, err := buildCallbackData("1020001")
	require.NoError(t, err)
	assert.Equal(t, "jm-1020001", cb)
}

// ── 网关状态机（假 WSS 服务端） ──

// qqFakeGateway gorilla 假网关：发 Hello → 收 Identify/心跳 → 按剧本回包。
type qqFakeGateway struct {
	srv      *httptest.Server
	upgrader websocket.Upgrader

	mu sync.Mutex
	// 观测点。
	gotIdentify *qqIdentifyData
	hbCount     atomic.Int32
	gotResume   *qqResumeData
	// 计数（多次连接场景断言用）。
	connCount     atomic.Int32
	identifyCount atomic.Int32
	resumeCount   atomic.Int32
	servedCount   atomic.Int32
	// 剧本。
	readySession string
	closeCode    int // 建会成功后主动关闭的码（0 = 不关闭）
	sendResumed  bool
	// readySeq 非 0 时：READY 之后补一帧带 s 的 Dispatch（用于断言 Resume 携带的 seq）。
	readySeq int64
	// closeAfterServed > 0 时：只关闭前 N 次成功建会（READY/RESUMED），之后保持连接；
	// 0 表示每次建会都按 closeCode 关闭（既有的「断线重连」剧本）。
	closeAfterServed int32
	// invalidSessionOnResume 为 true 时：收到 Op6 回 Op9(d=false)，且保持连接不主动断开
	// （验证客户端自己断开并重连，而不是静默挂着收不到事件）。
	invalidSessionOnResume bool
	// closeOnResumeCode 非 0 时：收到 Op6 先回 RESUMED（会话曾续上），再用该码关闭
	// （4006/4007 = 会话失效，必须重新 Identify 的关闭码剧本）。
	closeOnResumeCode int
	// noHeartbeatAck 为 true 时：心跳不回 Op11（ACK 超时剧本）。
	noHeartbeatAck bool
}

func newQQFakeGateway(t *testing.T, readySession string) *qqFakeGateway {
	t.Helper()
	g := &qqFakeGateway{readySession: readySession}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := g.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		g.connCount.Add(1)
		defer c.Close()
		// 发 Hello。
		hello, _ := json.Marshal(qqGatewayPayload{Op: qqOpHello, D: json.RawMessage(`{"heartbeat_interval":50}`)})
		if err := c.WriteMessage(websocket.TextMessage, hello); err != nil {
			return
		}
		for {
			_, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			var p qqGatewayPayload
			if err := json.Unmarshal(msg, &p); err != nil {
				continue
			}
			switch p.Op {
			case qqOpIdentify:
				var id qqIdentifyData
				_ = json.Unmarshal(p.D, &id)
				g.mu.Lock()
				g.gotIdentify = &id
				g.mu.Unlock()
				g.identifyCount.Add(1)
				ready, _ := json.Marshal(qqGatewayPayload{
					Op: qqOpDispatch, T: "READY",
					D: json.RawMessage(fmt.Sprintf(`{"session_id":%q}`, g.readySession)),
				})
				_ = c.WriteMessage(websocket.TextMessage, ready)
				// 补一帧带 seq 的 Dispatch：客户端应把它记进 Resume 的 seq。
				if g.readySeq != 0 {
					seq := g.readySeq
					seqFrame, _ := json.Marshal(qqGatewayPayload{
						Op: qqOpDispatch, T: "MESSAGE_CREATE", S: &seq, D: json.RawMessage(`{}`),
					})
					_ = c.WriteMessage(websocket.TextMessage, seqFrame)
				}
				if g.shouldCloseAfterServed() {
					_ = c.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(g.closeCode, "fake close"))
					return
				}
			case qqOpResume:
				var rs qqResumeData
				_ = json.Unmarshal(p.D, &rs)
				g.mu.Lock()
				g.gotResume = &rs
				g.mu.Unlock()
				g.resumeCount.Add(1)
				if g.invalidSessionOnResume {
					// Op9 d=false：会话作废，客户端必须改为重新 Identify。
					// 这里刻意不主动关连接：只有客户端自己断开重连，才不会「静默挂着收不到事件」。
					invalidSession, _ := json.Marshal(qqGatewayPayload{Op: qqOpInvalidSession, D: json.RawMessage(`false`)})
					_ = c.WriteMessage(websocket.TextMessage, invalidSession)
				} else if g.sendResumed {
					resumed, _ := json.Marshal(qqGatewayPayload{Op: qqOpDispatch, T: "RESUMED", D: json.RawMessage(`{}`)})
					_ = c.WriteMessage(websocket.TextMessage, resumed)
					if g.closeOnResumeCode != 0 {
						// 会话续上之后又被作废（4006/4007）：客户端必须复位会话改走 Identify。
						_ = c.WriteMessage(websocket.CloseMessage,
							websocket.FormatCloseMessage(g.closeOnResumeCode, "session invalidated"))
						return
					}
					if g.shouldCloseAfterServed() {
						_ = c.WriteMessage(websocket.CloseMessage,
							websocket.FormatCloseMessage(g.closeCode, "fake close"))
						return
					}
				}
			case qqOpHeartbeat:
				g.hbCount.Add(1)
				if g.noHeartbeatAck {
					continue
				}
				ack, _ := json.Marshal(qqGatewayPayload{Op: qqOpHeartbeatAck, D: json.RawMessage(`{}`)})
				_ = c.WriteMessage(websocket.TextMessage, ack)
			}
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// shouldCloseAfterServed 判断本次成功建会（READY/RESUMED）之后是否主动关闭连接。
func (g *qqFakeGateway) shouldCloseAfterServed() bool {
	if g.closeCode == 0 {
		return false
	}
	if g.closeAfterServed <= 0 {
		return true
	}
	return g.servedCount.Add(1) <= g.closeAfterServed
}

// wsURL 把 httptest 的 http 地址换成 ws。
func (g *qqFakeGateway) wsURL() string {
	return "ws" + strings.TrimPrefix(g.srv.URL, "http")
}

// waitFor 条件轮询（测试时钟）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", msg)
}

// newQQGatewayMux 假凭证/网关地址服务端：/app/getAppAccessToken + /gateway → 假 WSS 地址。
func newQQGatewayMux(t *testing.T, gw *qqFakeGateway) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_, _ = w.Write([]byte(`{"access_token":"mock-token","expires_in":7200}`))
		case "/gateway":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"url":%q}`, gw.wsURL())))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newQQGatewayTestService 建一个已配好通道的网关服务（未 Start，供直连 connectAndServe 的测试用）。
//
// newAlertTestDB 的库名取自 t.Name()，-count>1 时同一测例会复用内存库：上一轮写入的通道
// （baseURL 指向已关停的假网关）会被 ResolveCredentials 优先选中，故先清掉遗留通道。
func newQQGatewayTestService(t *testing.T, muxURL string) *QQDiscoveryService {
	t.Helper()
	db := newQQDiscoveryTestDB(t)
	require.NoError(t, db.Where("type = ?", model.ChannelTypeQQ).Delete(&model.AlertChannel{}).Error)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())
	qqDiscoveryTestChannel(t, db, "1020001", "plain", muxURL)
	return svc
}

// startQQGatewayTestService 建一个已配好通道的服务并 Start（测试结束自动 Stop）。
func startQQGatewayTestService(t *testing.T, muxURL string) *QQDiscoveryService {
	t.Helper()
	svc := newQQGatewayTestService(t, muxURL)
	svc.Start()
	t.Cleanup(svc.Stop)
	return svc
}

// TestQQDiscovery_网关Identify与心跳 Identify 字段形状 + 心跳发出 + 状态 connected。
func TestQQDiscovery_网关Identify与心跳(t *testing.T) {
	gw := newQQFakeGateway(t, "sess-1")

	muxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_, _ = w.Write([]byte(`{"access_token":"mock-token","expires_in":7200}`))
		case "/gateway":
			// 鉴权头断言：同样 QQBot 鉴权。
			assert.Equal(t, "QQBot mock-token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(fmt.Sprintf(`{"url":%q}`, gw.wsURL())))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer muxSrv.Close()
	// 通道 baseURL 指向 mux。
	db2 := newQQDiscoveryTestDB(t)
	svc2 := NewQQDiscoveryService(db2, NewChannelNotifier())
	qqDiscoveryTestChannel(t, db2, "1020001", "plain", muxSrv.URL)

	stop := make(chan struct{})
	defer close(stop)
	cred, err := svc2.ResolveCredentials()
	require.NoError(t, err)
	done := make(chan string, 1)
	go func() {
		// serve 一次建连：connectAndServe 在不断线时会阻塞；用 stop 关闭来结束。
		done <- svc2.connectAndServe(stop, cred)
	}()

	// Identify 应被假服务端收到。
	waitFor(t, 5*time.Second, func() bool {
		gw.mu.Lock()
		defer gw.mu.Unlock()
		return gw.gotIdentify != nil
	}, "假网关应收到 Identify")
	gw.mu.Lock()
	id := *gw.gotIdentify
	gw.mu.Unlock()
	assert.Equal(t, "QQBot mock-token", id.Token, "Identify token 应带 QQBot 前缀")
	assert.Equal(t, 33554432, id.Intents, "intents 应为 1<<25")
	assert.Equal(t, []int{0, 1}, id.Shard)

	// READY 后状态应为 connected。
	waitFor(t, 5*time.Second, func() bool {
		return svc2.GatewayStatus().Status == model.QQGatewayConnected
	}, "READY 后状态应为 connected")

	// 心跳应发出（假服务端计数）。
	waitFor(t, 5*time.Second, func() bool {
		return gw.hbCount.Load() >= 1
	}, "应发出心跳 Op1")
}

// TestQQDiscovery_断线触发重连退避 建连后服务端关闭 → BackoffTries 增长。
func TestQQDiscovery_断线触发重连退避(t *testing.T) {
	// 假网关：收到 Identify 后立即正常关闭（1000，非致命）→ 客户端应退避重连。
	gw := newQQFakeGateway(t, "sess-x")
	gw.closeCode = websocket.CloseNormalClosure

	muxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_, _ = w.Write([]byte(`{"access_token":"mock-token","expires_in":7200}`))
		case "/gateway":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"url":%q}`, gw.wsURL())))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer muxSrv.Close()

	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())
	qqDiscoveryTestChannel(t, db, "1020001", "plain", muxSrv.URL)

	svc.Start()
	defer svc.Stop()
	waitFor(t, 10*time.Second, func() bool {
		return svc.BackoffTries() >= 1
	}, "断线后应触发重连退避")
}

// TestQQDiscovery_4014停止重连并置error 4014 关闭 → 状态 error 且不再重连。
func TestQQDiscovery_4014停止重连并置error(t *testing.T) {
	gw := newQQFakeGateway(t, "sess-4014")
	gw.closeCode = qqCloseIntentNoPrivilege // 4014：intent 无权限

	muxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			_, _ = w.Write([]byte(`{"access_token":"mock-token","expires_in":7200}`))
		case "/gateway":
			_, _ = w.Write([]byte(fmt.Sprintf(`{"url":%q}`, gw.wsURL())))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer muxSrv.Close()

	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())
	qqDiscoveryTestChannel(t, db, "1020001", "plain", muxSrv.URL)

	svc.Start()
	defer svc.Stop()
	waitFor(t, 10*time.Second, func() bool {
		return svc.GatewayStatus().Status == model.QQGatewayError
	}, "4014 后状态应为 error")
	triesAfterError := svc.BackoffTries()
	assert.Contains(t, svc.GatewayStatus().LastError, "1<<25", "4014 错误应明确指向 intent 权限")

	// error 态停止重连：额外等待后退避计数不应增长。
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, triesAfterError, svc.BackoffTries(), "error 态不应继续重连")
}

// TestQQDiscovery_致命关闭码翻译 4914/4915 同样停止重连；code 映射断言。
func TestQQDiscovery_致命关闭码翻译(t *testing.T) {
	cases := []struct {
		name string
		code int
		want string
	}{
		{"4014 intent 无权限", qqCloseIntentNoPrivilege, "1<<25"},
		{"4914 下架", qqCloseOffline, "4914"},
		{"4915 封禁", qqCloseBanned, "4915"},
		{"普通关闭非致命", websocket.CloseNormalClosure, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fatalCloseMessage(tc.code)
			if tc.want == "" {
				assert.Empty(t, got)
			} else {
				assert.Contains(t, got, tc.want)
			}
			assert.Equal(t, tc.want != "", isFatalClose(&websocket.CloseError{Code: tc.code}))
		})
	}
}

// TestQQDiscovery_退避指数增长加抖动上限 backoffFor 单调有界。
func TestQQDiscovery_退避指数增长加抖动上限(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 12; i++ {
		d := backoffFor(i, rnd)
		assert.GreaterOrEqual(t, d, qqGatewayBackoffInitial/2)
		assert.LessOrEqual(t, d, qqGatewayBackoffMax)
	}
	// 大次数收敛到上限附近。
	d := backoffFor(100, rnd)
	assert.LessOrEqual(t, d, qqGatewayBackoffMax)
	assert.GreaterOrEqual(t, d, qqGatewayBackoffMax/2)
}

// ── 会话生命周期：Resume / Op9 / ACK 超时 / 退避复位 / goroutine 不泄漏 ──

// TestQQDiscovery_断线后Resume并复位状态 第二次连接发 Op6（session_id/seq 正确），收 RESUMED 后状态回 connected。
func TestQQDiscovery_断线后Resume并复位状态(t *testing.T) {
	gw := newQQFakeGateway(t, "sess-resume")
	gw.closeCode = websocket.CloseNormalClosure
	gw.closeAfterServed = 1 // 仅首次建会（READY）后关闭：之后保持连接，便于断言 RESUMED 后的稳态。
	gw.readySeq = 42        // READY 后补一帧 s=42：Resume 应带上它。
	gw.sendResumed = true

	svc := startQQGatewayTestService(t, newQQGatewayMux(t, gw).URL)

	waitFor(t, 10*time.Second, func() bool { return gw.identifyCount.Load() >= 1 }, "首次连接应发 Identify")
	waitFor(t, 15*time.Second, func() bool { return gw.resumeCount.Load() >= 1 }, "第二次连接应发 Resume（Op6）")

	gw.mu.Lock()
	rs := gw.gotResume
	gw.mu.Unlock()
	require.NotNil(t, rs)
	assert.Equal(t, "QQBot mock-token", rs.Token, "Resume token 应带 QQBot 前缀")
	assert.Equal(t, "sess-resume", rs.SessionID, "Resume 应带 READY 下发的 session_id")
	assert.Equal(t, int64(42), rs.Seq, "Resume 应带最近一次收到的 seq")

	// RESUMED 后状态回到 connected。
	waitFor(t, 10*time.Second, func() bool {
		return svc.GatewayStatus().Status == model.QQGatewayConnected
	}, "RESUMED 后状态应为 connected")
	assert.Equal(t, int32(1), gw.identifyCount.Load(), "会话有效时不应重复 Identify")
}

// TestQQDiscovery_会话失效后改走Identify 服务端作废会话（Op9 d=false / 4006 关闭码）→ 下一次连接必须 Identify。
func TestQQDiscovery_会话失效后改走Identify(t *testing.T) {
	cases := []struct {
		name  string
		setup func(gw *qqFakeGateway)
	}{
		// Op9 后服务端保持连接 ：客户端必须自己断开重连才会出现第三次 Identify。
		{"Op9下发d=false", func(gw *qqFakeGateway) { gw.invalidSessionOnResume = true }},
		// 会话曾续上（RESUMED）之后被 4006 关闭：必须由会话失效关闭码复位。
		{"会话失效关闭码4006", func(gw *qqFakeGateway) {
			gw.sendResumed = true
			gw.closeOnResumeCode = qqCloseInvalidSession
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gw := newQQFakeGateway(t, "sess-op9")
			gw.closeCode = websocket.CloseNormalClosure
			gw.closeAfterServed = 1 // 首次建会后关闭：第二次连接走 Resume
			tc.setup(gw)

			svc := startQQGatewayTestService(t, newQQGatewayMux(t, gw).URL)

			waitFor(t, 15*time.Second, func() bool { return gw.resumeCount.Load() >= 1 }, "第二次连接应先尝试 Resume")
			waitFor(t, 15*time.Second, func() bool { return gw.identifyCount.Load() >= 2 }, "会话失效后必须重新 Identify")

			// 会话已复位：稳定后不应再出现 Resume（旧实现会拿陈旧 session_id 无限重试 Resume）。
			time.Sleep(300 * time.Millisecond)
			assert.Equal(t, int32(1), gw.resumeCount.Load(), "会话复位后不应再用陈旧 session_id 重试 Resume")
			assert.Equal(t, int32(2), gw.identifyCount.Load(), "只应多做一次 Identify")
			waitFor(t, 10*time.Second, func() bool {
				return svc.GatewayStatus().Status == model.QQGatewayConnected
			}, "重新 Identify 收到 READY 后状态应 connected")
		})
	}
}

// TestQQDiscovery_心跳ACK超时立即断开重连 服务端不回 ACK：立刻断开并重连，而不是等读超时（假绿灯）。
func TestQQDiscovery_心跳ACK超时立即断开重连(t *testing.T) {
	gw := newQQFakeGateway(t, "sess-ack")
	gw.noHeartbeatAck = true // 心跳不回 ACK → 客户端应在 interval 内判超时
	gw.sendResumed = true    // 重连（Resume）能拿到 RESUMED，避免退化成「陈旧会话反复 Resume」

	startQQGatewayTestService(t, newQQGatewayMux(t, gw).URL)

	waitFor(t, 10*time.Second, func() bool { return gw.hbCount.Load() >= 1 }, "应发出心跳 Op1")
	// 若 ACK 超时只 return 不关连接，读循环要等 heartbeatInterval+30s（≈30s）才醒，本断言必然超时。
	waitFor(t, 10*time.Second, func() bool { return gw.connCount.Load() >= 2 }, "ACK 超时应立刻断开并重连")
	waitFor(t, 10*time.Second, func() bool { return gw.hbCount.Load() >= 2 }, "重连后应继续发心跳")
}

// TestQQDiscovery_退避档位连接成功后复位 档位推进与复位语义（首次退避以初值 1s 为基数）。
func TestQQDiscovery_退避档位连接成功后复位(t *testing.T) {
	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())
	rnd := rand.New(rand.NewSource(1))

	first := svc.nextBackoff(rnd)
	assert.Equal(t, 1, svc.BackoffLevel(), "首次退避档位应为 1")
	assert.LessOrEqual(t, first, qqGatewayBackoffInitial, "首次退避必须以初值 1s 为基数，不应翻倍成 2s")
	assert.GreaterOrEqual(t, first, qqGatewayBackoffInitial/2, "抖动下限为基数的一半")
	assert.Equal(t, first, svc.LastBackoffWait(), "应记录本次退避等待时长")

	second := svc.nextBackoff(rnd)
	assert.Equal(t, 2, svc.BackoffLevel(), "未连上时档位应继续推进")
	assert.GreaterOrEqual(t, second, first, "档位越大等待越久")
	assert.Equal(t, 2, svc.BackoffTries(), "累计退避次数随档位推进")

	// 连接成功（READY/RESUMED → markConnected）→ 档位复位；累计次数保留（4014 断言依赖它只增不减）。
	svc.markConnected()
	assert.Equal(t, 0, svc.BackoffLevel(), "连接成功后档位应复位为 0")
	assert.Equal(t, 2, svc.BackoffTries(), "累计退避次数不应被复位")
}

// TestQQDiscovery_反复重连不累积退避档位 每轮连上又断线时档位每轮从初值重来（旧实现会一路涨到 5min）。
func TestQQDiscovery_反复重连不累积退避档位(t *testing.T) {
	gw := newQQFakeGateway(t, "sess-reset")
	gw.closeCode = websocket.CloseNormalClosure // 每轮建会后立即关闭 → 反复重连
	gw.sendResumed = true                       // 第二轮起是 Resume：回 RESUMED，避免等 30s 读超时

	svc := startQQGatewayTestService(t, newQQGatewayMux(t, gw).URL)

	waitFor(t, 30*time.Second, func() bool { return svc.BackoffTries() >= 3 }, "应至少完成 3 次退避重连")
	// 每轮都收到 READY → 档位复位，因此任意时刻观察到的档位都不会超过 1；
	// 旧实现只增不减，3 轮后档位已是 3（首等 4s 起，稳定运行数小时后会顶到 5min）。
	assert.LessOrEqual(t, svc.BackoffLevel(), 1, "连接成功后档位必须复位，不应跨轮累积")
	assert.LessOrEqual(t, svc.LastBackoffWait(), qqGatewayBackoffInitial,
		"每轮退避都应从初值 1s 重来")
	assert.GreaterOrEqual(t, svc.BackoffTries(), 3, "累计退避次数只增不减")
}

// TestQQDiscovery_重连不泄漏心跳goroutine 反复「连上又断」后 goroutine 数不随重连次数增长。
func TestQQDiscovery_重连不泄漏心跳goroutine(t *testing.T) {
	gw := newQQFakeGateway(t, "sess-leak")
	gw.closeCode = websocket.CloseNormalClosure // 每次建会后立刻断 → 反复走 serve 退出路径
	gw.sendResumed = true                       // 第二轮起走 Resume，同样能正常建会再断开

	mux := newQQGatewayMux(t, gw)
	svc := newQQGatewayTestService(t, mux.URL)
	cred, err := svc.ResolveCredentials()
	require.NoError(t, err)

	stop := make(chan struct{})
	defer close(stop)

	// 预热一轮，避免首次连接的惰性初始化（连接池等）被算进基线。
	require.Empty(t, svc.connectAndServe(stop, cred))
	waitFor(t, 5*time.Second, func() bool { return gw.connCount.Load() >= 1 }, "预热连接应建立")
	time.Sleep(200 * time.Millisecond) // 等假网关侧连接 goroutine 收尾
	baseline := runtime.NumGoroutine()

	const rounds = 8
	for i := 0; i < rounds; i++ {
		require.Empty(t, svc.connectAndServe(stop, cred), "第 %d 轮连接不应返回致命错误", i+1)
	}
	waitFor(t, 5*time.Second, func() bool {
		return gw.connCount.Load() == int32(rounds+1)
	}, fmt.Sprintf("应完成 %d 次建会", rounds+1))

	// 旧实现每次 serve 退出都泄漏一个永久阻塞的心跳 goroutine（8 轮 → 基线 +8）。
	waitFor(t, 5*time.Second, func() bool {
		return runtime.NumGoroutine() <= baseline+2
	}, "重连后不应残留心跳 goroutine")
}

// TestQQDiscovery_Stop状态语义 Stop 不得抹掉人工介入态（error）的诊断信息，其余态照常翻回 disconnected。
func TestQQDiscovery_Stop状态语义(t *testing.T) {
	t.Run("人工介入态保留诊断", func(t *testing.T) {
		gw := newQQFakeGateway(t, "sess-stop")
		gw.closeCode = qqCloseIntentNoPrivilege // 4014 → 人工介入态

		svc := startQQGatewayTestService(t, newQQGatewayMux(t, gw).URL)
		waitFor(t, 10*time.Second, func() bool {
			return svc.GatewayStatus().Status == model.QQGatewayError
		}, "4014 后状态应为 error")

		svc.Stop()

		view := svc.GatewayStatus()
		assert.Equal(t, model.QQGatewayError, view.Status, "Stop 不应把人工介入态抹成 disconnected")
		assert.Contains(t, view.LastError, "1<<25", "Stop 不应抹掉 last_error 诊断信息")
	})

	t.Run("非人工介入态翻回disconnected", func(t *testing.T) {
		gw := newQQFakeGateway(t, "sess-stop-2")
		gw.closeCode = websocket.CloseNormalClosure // 反复断线 → 长期处于 connecting

		svc := startQQGatewayTestService(t, newQQGatewayMux(t, gw).URL)
		waitFor(t, 10*time.Second, func() bool {
			return svc.GatewayStatus().Status == model.QQGatewayConnecting
		}, "断线退避中状态应为 connecting")

		svc.Stop()
		assert.Equal(t, model.QQGatewayDisconnected, svc.GatewayStatus().Status,
			"非人工介入态 Stop 后应翻回 disconnected（既有语义不变）")
	})
}

// ── 查询：分页与密钥安全 ──

// TestQQDiscovery_群列表分页 分页参数与总数断言。
func TestQQDiscovery_群列表分页(t *testing.T) {
	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())
	base := time.Unix(1700000000, 0)
	for i := 0; i < 5; i++ {
		require.NoError(t, svc.RecordGroupEvent("app1", &qqGroupAddRobot{
			GroupOpenID: fmt.Sprintf("g%d", i),
		}, base.Add(time.Duration(i)*time.Minute)))
	}

	items, total, err := svc.ListGroups(1, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(5), total)
	assert.Len(t, items, 2)

	items2, total2, err := svc.ListGroups(3, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(5), total2)
	assert.Len(t, items2, 1, "第 3 页只剩 1 条")

	// 越界 pageSize 回退默认。
	_, _, err = svc.ListGroups(1, 1000)
	require.NoError(t, err)

	// 响应序列化不含 appSecret。
	raw, _ := json.Marshal(items)
	assert.NotContains(t, string(raw), "appSecret")
	assert.NotContains(t, string(raw), "secret")
}

// TestQQDiscovery_状态响应不含密钥 状态视图序列化断言。
func TestQQDiscovery_状态响应不含密钥(t *testing.T) {
	db := newQQDiscoveryTestDB(t)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())
	raw, _ := json.Marshal(svc.GatewayStatus())
	assert.NotContains(t, string(raw), "appSecret")
	assert.NotContains(t, string(raw), "secret")
}

// TestQQDiscovery_建连失败原因写入状态 建连失败必须把原因写进对外状态，而不是只落日志。
//
// 背景：原先这些失败只 slog，而 loop 在退避前会 setStatus(connecting, "") 清空 last_error，
// 两者叠加导致「机器人不存在 / 密钥错误 / 网关不可达」这类最常见的配置错误，通过 API 与
// 前端只能看到永远「连接中」且 last_error 为空，运维只能去翻服务器日志。
// 该盲区由真机冒烟验证（错误凭证起真实 CP）实测复现，本用例锁定修复后行为。
func TestQQDiscovery_建连失败原因写入状态(t *testing.T) {
	// 指向一个已关闭的地址：建连阶段必然失败，且原因可读。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	db := newQQDiscoveryTestDB(t)
	require.NoError(t, db.Where("type = ?", model.ChannelTypeQQ).Delete(&model.AlertChannel{}).Error)
	svc := NewQQDiscoveryService(db, NewChannelNotifier())
	qqDiscoveryTestChannel(t, db, "1020001", "plain", deadURL)

	svc.Start()
	t.Cleanup(svc.Stop)

	require.Eventually(t, func() bool {
		return svc.GatewayStatus().LastError != ""
	}, 10*time.Second, 50*time.Millisecond,
		"建连失败后 last_error 必须非空，否则 API/前端无法展示失败原因")

	view := svc.GatewayStatus()
	assert.Equal(t, model.QQGatewayConnecting, view.Status, "可重试的建连失败应保持 connecting 而非 error")
	assert.Contains(t, view.LastError, "失败", "last_error 应为人类可读的失败描述，实际: %s", view.LastError)
}
