package router

// FR-515 Phase D 传输层测试：会话/消息/run/事件端点 + SSE。
//
// 这组用例守的是四件事，任何一条退化成"能跑但语义错"都会造成实质问题：
//  1. **越权一律 404**：会话是私有数据（§3 冻结项），403 与 404 的差别就是一个
//     「这个 id 存在吗」的探测接口。所以要逐个端点验，而不是只验一个就推而广之。
//  2. **幂等键**：前端重试按钮是常态。同键同内容必须回原 run，同键异内容必须 409，
//     两者都靠 run 行数断言——只看响应码无法区分"复用了原 run"与"又新建了一条"。
//  3. **活跃槽**：409 必须带出当前活跃 runId（§5.2 末段），否则前端只知道"忙"却无从接续观察。
//  4. **游标**：`before`/`after` 非法一律 400 INVALID_CURSOR（§6.2），尤其
//     after 超出 maxSeq 是 400 而**不是** 410——事件永不清理，410 会误导前端放弃补读。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// assistantChatFixture 装好路由、两个互不相干的成员用户，以及一个可用的模型配置。
//
// 为什么要真实建「供应商 + 模型 + 上游打桩」，而不是给运行时注入假适配器：
// 注入点（setModelPreparer）是 service 包内部的测试缝，router 包够不到；更重要的是
// 这里要验证的是**装配是否真的通了**（HTTP 层 → 运行时 → 配置面解析 → 出站适配器），
// 用假适配器会把「配置到运行时的接线错了」这类故障整段跳过。
type assistantChatFixture struct {
	router     *gin.Engine
	db         *gorm.DB
	adminToken string
	// tokenA / tokenB 两个互相独立的普通成员：越权用例要求它们谁也不是谁的管理员。
	tokenA string
	tokenB string
	// modelID 建好的模型（指向下面那个 SSE 打桩上游）。
	modelID uint
}

// newAssistantChatFixture 构造夹具。
func newAssistantChatFixture(t *testing.T) *assistantChatFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := setupTestDB(t)
	// 用默认（宽松出站）路由：打桩上游跑在 127.0.0.1 的 http 上，
	// 严格出站会把它挡在准入之外，测到的就只是"准入生效"，不是被测逻辑。
	r := setupTestRouter(db)

	// 先建管理员（/setup），再建两个成员。
	adminToken := getAdminToken(t, r)
	tokenA := getMemberToken(t, r, "chat_member_a", "password123")
	tokenB := getMemberToken(t, r, "chat_member_b", "password123")

	// 上游打桩：一次性返回两帧 SSE（一段文本 + [DONE]），让一轮 run 能自然收尾。
	// 断言侧因此可以等 run 到终态，而不是靠 sleep 猜时序。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"收到\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)

	// 供应商 + 模型走管理端接口创建：与生产同一装配路径，避免"测试里手搓 DB 行
	// 写得进、生产装配读不出"的两套形态。
	code, body := doAssistantReq(t, r, http.MethodPost, "/api/v1/assistant/admin/providers", adminToken,
		fmt.Sprintf(`{"name":"测试网关","protocol":"openai-chat","baseUrl":%q,"credentialMode":"none","enabled":true}`,
			upstream.URL))
	require.Equal(t, http.StatusCreated, code, "建供应商失败: %s", body)
	var provider struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &provider))

	code, body = doAssistantReq(t, r, http.MethodPost, "/api/v1/assistant/admin/models", adminToken,
		fmt.Sprintf(`{"providerId":%d,"modelId":"test-model","enabled":true,"streaming":true,"tools":false}`,
			provider.ID))
	require.Equal(t, http.StatusCreated, code, "建模型失败: %s", body)
	var mdl struct {
		ID uint `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &mdl))

	return &assistantChatFixture{
		router:     r,
		db:         db,
		adminToken: adminToken,
		tokenA:     tokenA,
		tokenB:     tokenB,
		modelID:    mdl.ID,
	}
}

// createConversation 用指定 token 建一个会话，返回会话 id。
func (f *assistantChatFixture) createConversation(t *testing.T, token string) uint {
	t.Helper()
	w := makeRequest(f.router, http.MethodPost, "/api/v1/assistant/conversations",
		map[string]any{"title": "测试会话", "modelId": f.modelID}, token)
	require.Equal(t, http.StatusCreated, w.Code, "建会话失败: %s", w.Body.String())
	resp := parseJSON(t, w)
	id, _ := resp["id"].(float64)
	require.NotZero(t, id, "建会话响应缺少 id: %s", w.Body.String())
	return uint(id)
}

// conversationOwnerID 取会话归属用户 id（直接插活跃 run 时需要它）。
func (f *assistantChatFixture) conversationOwnerID(t *testing.T, convID uint) uint {
	t.Helper()
	var conv model.AssistantConversation
	require.NoError(t, f.db.First(&conv, convID).Error)
	return conv.OwnerUserID
}

// countRuns 统计某会话下的 run 行数（幂等用例的核心断言）。
func (f *assistantChatFixture) countRuns(t *testing.T, convID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&model.AssistantRun{}).Where("conversation_id = ?", convID).Count(&n).Error)
	return n
}

// waitRunTerminal 等 run 进入终态（执行体在后台推进，测试必须等它收敛再断言）。
//
// 为什么不用 sleep 固定时长：固定等待会让慢机器上偶发失败、快机器上浪费 5 秒；
// 轮询到终态既确定又快。
func (f *assistantChatFixture) waitRunTerminal(t *testing.T, runID uint) *model.AssistantRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var run model.AssistantRun
		require.NoError(t, f.db.First(&run, runID).Error)
		switch run.Status {
		case model.AssistantRunStatusCompleted, model.AssistantRunStatusFailed,
			model.AssistantRunStatusCancelled, model.AssistantRunStatusTimedOut,
			model.AssistantRunStatusInterrupted:
			return &run
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %d 在 10 秒内未进入终态，当前状态=%s", runID, run.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// doAssistantChatReq 发一个可带自定义头的请求。
//
// 为什么不用 makeRequest：它不写自定义头，而 Idempotency-Key 正是要验的东西。
// 其余行为（JSON 编码、Bearer 头）与 makeRequest 保持一致。
func doAssistantChatReq(t *testing.T, r *gin.Engine, method, path string,
	body interface{}, token string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		buf = bytes.NewBuffer(raw)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// 越权：对他人会话的全部读写一律 404，不是 403、不是 500。
//
// 逐个端点验而不是抽查一个：每个 handler 都自己解析 id 并独立调用 service，
// 抽查会让「新加的端点忘了带 owner 条件」这类回归正好落在没测到的那个端点上。
func TestAssistantChatCrossUserAccessIs404(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"会话详情", http.MethodGet, fmt.Sprintf("/api/v1/assistant/conversations/%d", convID), nil},
		{"改会话", http.MethodPatch, fmt.Sprintf("/api/v1/assistant/conversations/%d", convID), map[string]any{"title": "改名"}},
		{"删会话", http.MethodDelete, fmt.Sprintf("/api/v1/assistant/conversations/%d", convID), nil},
		{"消息列表", http.MethodGet, fmt.Sprintf("/api/v1/assistant/conversations/%d/messages", convID), nil},
		{"提交 run", http.MethodPost, fmt.Sprintf("/api/v1/assistant/conversations/%d/runs", convID),
			map[string]any{"text": "越权提交"}},
	}
	for _, tc := range cases {
		w := makeRequest(f.router, tc.method, tc.path, tc.body, f.tokenB)
		require.Equalf(t, http.StatusNotFound, w.Code,
			"%s 越权必须是 404（不泄露存在性），实际 %d 响应=%s", tc.name, w.Code, w.Body.String())
		resp := parseJSON(t, w)
		require.Equalf(t, "CONVERSATION_NOT_FOUND", resp["error"], "%s 错误码不对: %s", tc.name, w.Body.String())
	}

	// 越权不得留下任何副作用：B 的越权删除不得真的删掉 A 的会话。
	w := makeRequest(f.router, http.MethodGet, fmt.Sprintf("/api/v1/assistant/conversations/%d", convID), nil, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code, "会话应仍然存在: %s", w.Body.String())
}

// 越权读他人的 run 事件与 SSE 同样 404（run 没有 conversation 那条路径可以代理归属）。
func TestAssistantChatCrossUserRunAccessIs404(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)
	w := doAssistantChatReq(t, f.router, http.MethodPost,
		fmt.Sprintf("/api/v1/assistant/conversations/%d/runs", convID),
		map[string]any{"text": "第一轮"}, f.tokenA, map[string]string{"Idempotency-Key": "run-1"})
	require.Equal(t, http.StatusAccepted, w.Code, "提交 run 失败: %s", w.Body.String())
	runID := uint(parseJSON(t, w)["runId"].(float64))

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"补读事件", http.MethodGet, fmt.Sprintf("/api/v1/assistant/runs/%d/events", runID)},
		{"SSE 流", http.MethodGet, fmt.Sprintf("/api/v1/assistant/runs/%d/stream", runID)},
		{"取消", http.MethodPost, fmt.Sprintf("/api/v1/assistant/runs/%d/cancel", runID)},
	} {
		w := makeRequest(f.router, tc.method, tc.path, nil, f.tokenB)
		require.Equalf(t, http.StatusNotFound, w.Code, "%s 越权必须是 404，实际 %d 响应=%s",
			tc.name, w.Code, w.Body.String())
		require.Equalf(t, "RUN_NOT_FOUND", parseJSON(t, w)["error"], "%s 错误码不对", tc.name)
	}
}

// 未登录一律 401（鉴权由 protected 组的中间件统一承担，本组端点不自造鉴权）。
func TestAssistantChatRequiresLogin(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/assistant/conversations"},
		{http.MethodPost, "/api/v1/assistant/conversations"},
		{http.MethodGet, fmt.Sprintf("/api/v1/assistant/conversations/%d", convID)},
		{http.MethodGet, fmt.Sprintf("/api/v1/assistant/conversations/%d/messages", convID)},
		{http.MethodPost, fmt.Sprintf("/api/v1/assistant/conversations/%d/runs", convID)},
		{http.MethodGet, "/api/v1/assistant/runs/1/events"},
		{http.MethodGet, "/api/v1/assistant/runs/1/stream"},
		{http.MethodPost, "/api/v1/assistant/runs/1/cancel"},
	} {
		w := makeRequest(f.router, tc.method, tc.path, map[string]any{"text": "x"}, "")
		require.Equalf(t, http.StatusUnauthorized, w.Code, "%s %s 未登录应 401，实际 %d",
			tc.method, tc.path, w.Code)
	}
}

// 会话列表只含自己的：私有会话的隔离不能只靠"前端不显示"。
func TestAssistantChatConversationListIsPerUser(t *testing.T) {
	f := newAssistantChatFixture(t)
	convA := f.createConversation(t, f.tokenA)
	convB := f.createConversation(t, f.tokenB)

	w := makeRequest(f.router, http.MethodGet, "/api/v1/assistant/conversations", nil, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code, "列表失败: %s", w.Body.String())
	items, ok := parseJSON(t, w)["items"].([]interface{})
	require.True(t, ok, "响应缺少 items 数组: %s", w.Body.String())
	require.Len(t, items, 1, "A 只该看到自己的 1 个会话: %s", w.Body.String())
	first, _ := items[0].(map[string]interface{})
	require.EqualValues(t, convA, first["id"])
	require.NotEqualValues(t, convB, first["id"], "不得看到他人会话")
}

// 会话详情带最近消息；不存在的会话 404。
func TestAssistantChatConversationDetailAndNotFound(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)

	w := makeRequest(f.router, http.MethodGet, fmt.Sprintf("/api/v1/assistant/conversations/%d", convID), nil, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code, "详情失败: %s", w.Body.String())
	resp := parseJSON(t, w)
	require.Contains(t, resp, "conversation")
	require.Contains(t, resp, "messages", "详情应同时带最近消息（§6）")
	conv, _ := resp["conversation"].(map[string]interface{})
	require.EqualValues(t, f.modelID, conv["modelId"])

	w = makeRequest(f.router, http.MethodGet, "/api/v1/assistant/conversations/999999", nil, f.tokenA)
	require.Equal(t, http.StatusNotFound, w.Code, "不存在的会话应 404: %s", w.Body.String())
	require.Equal(t, "CONVERSATION_NOT_FOUND", parseJSON(t, w)["error"])
}

// 改标题 / 换模型 / 删会话的正常路径。
//
// 重点是 modelId 的**三态**：缺省=不改、null=清空（回跟随默认模型）、数字=改用该模型。
// 三态在本层靠 json.RawMessage 区分，若误用 *uint，`null` 会与「缺省」压成同一个 nil，
// 「清空模型」这个操作就再也表达不出来——而它不会报错，只会静默地"改了但没生效"。
func TestAssistantChatUpdateAndDeleteConversation(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)
	path := fmt.Sprintf("/api/v1/assistant/conversations/%d", convID)

	// 只改标题：modelId 缺省，必须保持原值。
	w := makeRequest(f.router, http.MethodPatch, path, map[string]any{"title": "改名后"}, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code, "改标题失败: %s", w.Body.String())
	resp := parseJSON(t, w)
	require.Equal(t, "改名后", resp["title"])
	require.EqualValues(t, f.modelID, resp["modelId"], "未传 modelId 时不得改动模型")

	// modelId: null → 显式清空（回跟随默认模型）。
	w = doAssistantChatReq(t, f.router, http.MethodPatch, path,
		json.RawMessage(`{"modelId":null}`), f.tokenA, nil)
	require.Equal(t, http.StatusOK, w.Code, "清空模型失败: %s", w.Body.String())
	require.Nil(t, parseJSON(t, w)["modelId"], "modelId 应被清空")

	// modelId: 数字 → 改用该模型。
	w = makeRequest(f.router, http.MethodPatch, path, map[string]any{"modelId": f.modelID}, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code, "换模型失败: %s", w.Body.String())
	require.EqualValues(t, f.modelID, parseJSON(t, w)["modelId"])

	// 删除后即不可读（软删除对读路径表现为不存在）。
	w = makeRequest(f.router, http.MethodDelete, path, nil, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code, "删除失败: %s", w.Body.String())
	require.Equal(t, true, parseJSON(t, w)["deleted"])
	w = makeRequest(f.router, http.MethodGet, path, nil, f.tokenA)
	require.Equal(t, http.StatusNotFound, w.Code, "删除后应 404: %s", w.Body.String())
}

// 消息分页的 before 非法 → 400 INVALID_CURSOR（不是 500，也不是"忽略参数回全量"）。
func TestAssistantChatMessagesInvalidBeforeCursor(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)

	for _, bad := range []string{"abc", "-1", "1.5"} {
		w := makeRequest(f.router, http.MethodGet,
			fmt.Sprintf("/api/v1/assistant/conversations/%d/messages?before=%s", convID, bad), nil, f.tokenA)
		require.Equalf(t, http.StatusBadRequest, w.Code, "before=%s 应 400，实际 %d", bad, w.Code)
		require.Equalf(t, "INVALID_CURSOR", parseJSON(t, w)["error"], "before=%s 错误码不对", bad)
	}

	// 合法 before 正常返回（游标合法时不该被误伤）。
	w := makeRequest(f.router, http.MethodGet,
		fmt.Sprintf("/api/v1/assistant/conversations/%d/messages?before=1000", convID), nil, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code, "合法 before 应 200: %s", w.Body.String())
}

// 事件补读：after 超出该 run 的 maxSeq → 400 INVALID_CURSOR，**不是 410**（§6.2）。
func TestAssistantChatEventsAfterBeyondMaxIsInvalidCursor(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)
	runID := f.submitRun(t, f.tokenA, convID, "补读用例", "")
	f.waitRunTerminal(t, runID)

	// 正常补读：seq 从 1 开始且连续，最后一条必是 run_end（§5.3）。
	w := makeRequest(f.router, http.MethodGet,
		fmt.Sprintf("/api/v1/assistant/runs/%d/events?after=0", runID), nil, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code, "补读失败: %s", w.Body.String())
	events := parseJSONArray(t, w)
	require.NotEmpty(t, events, "一轮已完成的 run 至少应有 text_delta 与 run_end: %s", w.Body.String())
	first, _ := events[0].(map[string]interface{})
	require.EqualValues(t, 1, first["seq"], "seq 必须从 1 开始")
	last, _ := events[len(events)-1].(map[string]interface{})
	require.Equal(t, model.AssistantEventTypeRunEnd, last["type"], "最后一条必须是 run_end")

	// after 超过 maxSeq：400 而非 410。事件永不清理，游标失效只可能是伪造或跨 run 误用。
	w = makeRequest(f.router, http.MethodGet,
		fmt.Sprintf("/api/v1/assistant/runs/%d/events?after=999999", runID), nil, f.tokenA)
	require.Equal(t, http.StatusBadRequest, w.Code, "超界 after 应 400，实际 %d 响应=%s", w.Code, w.Body.String())
	require.Equal(t, "INVALID_CURSOR", parseJSON(t, w)["error"])

	// after 非整数同样 400（不是把参数当 0 处理）。
	w = makeRequest(f.router, http.MethodGet,
		fmt.Sprintf("/api/v1/assistant/runs/%d/events?after=x", runID), nil, f.tokenA)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Equal(t, "INVALID_CURSOR", parseJSON(t, w)["error"])
}

// 幂等：同键同内容两次 → 同一个 runId，且**不新建** run。
//
// run 行数断言是这条用例的关键：只比 runId 相等无法排除"新建了一条但复用了 id"这类
// 不可能但一旦发生就无声的错误；行数则直接回答"库里多没多一条"。
func TestAssistantChatSubmitRunIdempotentReplay(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)
	headers := map[string]string{"Idempotency-Key": "idem-key-1"}
	body := map[string]any{"text": "同一个请求"}

	first := doAssistantChatReq(t, f.router, http.MethodPost,
		fmt.Sprintf("/api/v1/assistant/conversations/%d/runs", convID), body, f.tokenA, headers)
	require.Equal(t, http.StatusAccepted, first.Code, "首次提交应 202: %s", first.Body.String())
	runID1 := uint(parseJSON(t, first)["runId"].(float64))
	require.NotZero(t, runID1)

	second := doAssistantChatReq(t, f.router, http.MethodPost,
		fmt.Sprintf("/api/v1/assistant/conversations/%d/runs", convID), body, f.tokenA, headers)
	require.Equal(t, http.StatusAccepted, second.Code, "重放应 202: %s", second.Body.String())
	runID2 := uint(parseJSON(t, second)["runId"].(float64))
	require.Equal(t, runID1, runID2, "同键同内容必须回原 run")
	require.EqualValues(t, 1, f.countRuns(t, convID), "同键重放不得新建 run")
	// 等执行体收尾再结束用例：否则 t.Cleanup 会在后台写库前关掉数据库，
	// 日志里会出现一串 "database is closed" 的假故障，掩盖真正的缺陷。
	f.waitRunTerminal(t, runID1)
}

// 幂等：同键异内容 → 409 IDEMPOTENCY_CONFLICT，且同样**不新建** run。
func TestAssistantChatSubmitRunIdempotencyConflict(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)
	headers := map[string]string{"Idempotency-Key": "idem-key-2"}

	first := doAssistantChatReq(t, f.router, http.MethodPost,
		fmt.Sprintf("/api/v1/assistant/conversations/%d/runs", convID),
		map[string]any{"text": "第一份内容"}, f.tokenA, headers)
	require.Equal(t, http.StatusAccepted, first.Code, "首次提交应 202: %s", first.Body.String())
	firstRunID := uint(parseJSON(t, first)["runId"].(float64))

	second := doAssistantChatReq(t, f.router, http.MethodPost,
		fmt.Sprintf("/api/v1/assistant/conversations/%d/runs", convID),
		map[string]any{"text": "第二份内容"}, f.tokenA, headers)
	require.Equal(t, http.StatusConflict, second.Code, "同键异内容应 409: %s", second.Body.String())
	require.Equal(t, "IDEMPOTENCY_CONFLICT", parseJSON(t, second)["error"])
	require.EqualValues(t, 1, f.countRuns(t, convID), "冲突请求不得新建 run")
	// 同上：等后台执行体收尾，避免用例结束时 DB 已关闭却仍有写入。
	f.waitRunTerminal(t, firstRunID)
}

// 活跃槽：会话已有活跃 run 时再提交 → 409 CONVERSATION_BUSY，且响应体带出**当前活跃 runId**。
//
// 直接往库里插一条活跃 run，而不是靠"让真实 run 跑慢一点"：
// 后者依赖时序（上游响应多快、调度何时切走），在慢机器上会偶发变成 completed 而使用例失真。
// 插入的行本身就是 service 的判定输入（findActiveRun 只查库），语义等价且确定。
func TestAssistantChatSubmitRunBusyCarriesActiveRunID(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)

	active := &model.AssistantRun{
		ConversationID:    convID,
		OwnerUserID:       f.conversationOwnerID(t, convID),
		ModelSnapshot:     "{}",
		ToolPolicyVersion: 1,
		Status:            model.AssistantRunStatusRunning,
		Version:           1,
	}
	require.NoError(t, f.db.Create(active).Error)

	w := doAssistantChatReq(t, f.router, http.MethodPost,
		fmt.Sprintf("/api/v1/assistant/conversations/%d/runs", convID),
		map[string]any{"text": "撞上活跃槽"}, f.tokenA, nil)
	require.Equal(t, http.StatusConflict, w.Code, "应 409 CONVERSATION_BUSY: %s", w.Body.String())
	resp := parseJSON(t, w)
	require.Equal(t, "CONVERSATION_BUSY", resp["error"])
	require.EqualValues(t, active.ID, resp["runId"], "409 必须带出当前活跃 runId（前端靠它接续观察）")
}

// SSE：内容类型正确，并以 `event: <type>` + `data: <json>` 的形态吐出事件，最后以 run_end 结束。
//
// 为什么读完需要的帧就结束、不等长连接：这条流的自然终点就是 run_end（§5.3 每 run 恰好一条），
// handler 读到它即返回；若此处仍挂着，说明终点判定坏了——那正是要暴露的缺陷，而不是要绕过的噪声。
func TestAssistantChatStreamSSEFrames(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)
	runID := f.submitRun(t, f.tokenA, convID, "流式用例", "")
	run := f.waitRunTerminal(t, runID)
	require.Equal(t, model.AssistantRunStatusCompleted, run.Status, "打桩上游应让本轮正常收尾")

	w := makeRequest(f.router, http.MethodGet,
		fmt.Sprintf("/api/v1/assistant/runs/%d/stream?after=0", runID), nil, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code, "SSE 应 200: %s", w.Body.String())
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	require.Equal(t, "no-cache", w.Header().Get("Cache-Control"))
	require.Equal(t, "no", w.Header().Get("X-Accel-Buffering"), "必须关掉反向代理缓冲，否则流式变批量")

	raw := w.Body.String()
	require.Contains(t, raw, "event: "+model.AssistantEventTypeTextDelta, "应补发历史文本增量: %s", raw)
	require.Contains(t, raw, "event: "+model.AssistantEventTypeRunEnd, "应以 run_end 结束: %s", raw)
	require.Contains(t, raw, "data: {", "每条事件都必须带 JSON 载荷: %s", raw)
	// seq 必须出现在帧里：前端断线重连靠它算 after。
	require.Contains(t, raw, `"seq":1`, "帧内必须带 seq: %s", raw)
}

// submitRun 提交一轮并返回 runId（helper 供多个用例复用）。
func (f *assistantChatFixture) submitRun(t *testing.T, token string, convID uint, text, idemKey string) uint {
	t.Helper()
	headers := map[string]string{}
	if idemKey != "" {
		headers["Idempotency-Key"] = idemKey
	}
	w := doAssistantChatReq(t, f.router, http.MethodPost,
		fmt.Sprintf("/api/v1/assistant/conversations/%d/runs", convID),
		map[string]any{"text": text}, token, headers)
	require.Equal(t, http.StatusAccepted, w.Code, "提交 run 应 202: %s", w.Body.String())
	resp := parseJSON(t, w)
	runID, ok := resp["runId"].(float64)
	require.True(t, ok && runID > 0, "响应缺少 runId: %s", w.Body.String())
	return uint(runID)
}

// ── FR-515 §9：角色与数据保留的边界 ──────────────────────────────────────

// loginAs 用给定账密登录，返回 accessToken（角色变更后需重新签发时用）。
func loginAs(t *testing.T, r *gin.Engine, username, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "登录失败: %s", w.Body.String())
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	token, _ := resp["accessToken"].(string)
	require.NotEmpty(t, token, "登录响应缺少 accessToken: %s", w.Body.String())
	return token
}

// 组管理员（role=GroupAdmin）同样不能读写他人私有会话。
//
// 会话归属只认 owner_id，与角色无关：组管理员能管组内资源，但助手会话是**个人私有数据**，
// 不在组的管辖范围内。这条容易在「给组管理员补权限」时被顺手改坏——若把 owner 检查
// 换成「同组即可读」，用户与 AI 的私人对话就会对组管理员透明。
func TestAssistantChat_GroupAdminCannotAccessOthersConversation(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)

	// 提升 member_b 为组管理员，再重新登录（角色变更后原 token 的角色信息已过期）
	require.NoError(t, f.db.Model(&model.User{}).
		Where("username = ?", "chat_member_b").
		Update("role", model.RoleGroupAdmin).Error)
	tokenB := loginAs(t, f.router, "chat_member_b", "password123")

	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{"会话详情", http.MethodGet, fmt.Sprintf("/api/v1/assistant/conversations/%d", convID)},
		{"消息列表", http.MethodGet, fmt.Sprintf("/api/v1/assistant/conversations/%d/messages", convID)},
	} {
		w := makeRequest(f.router, tc.method, tc.path, nil, tokenB)
		require.Equalf(t, http.StatusNotFound, w.Code,
			"组管理员%s越权必须 404（不泄露存在性），实际 %d 响应=%s", tc.name, w.Code, w.Body.String())
		require.Equalf(t, "CONVERSATION_NOT_FOUND", parseJSON(t, w)["error"], "%s 错误码不对", tc.name)
	}
}

// 所有登录角色都能建立自己的会话，且互不可见。
//
// 会话创建不校验角色（只写 owner_id），因此普通成员、组管理员、平台管理员
// 走的应当是同一条路径。这条防的是「某天给建会话加角色白名单」——
// 那会让低权限用户彻底无法使用助手，而不是只少几个功能。
func TestAssistantChat_AllRolesCanCreateOwnConversation(t *testing.T) {
	f := newAssistantChatFixture(t)

	// 把 member_b 提成组管理员
	require.NoError(t, f.db.Model(&model.User{}).
		Where("username = ?", "chat_member_b").
		Update("role", model.RoleGroupAdmin).Error)
	tokenB := loginAs(t, f.router, "chat_member_b", "password123")

	convMember := f.createConversation(t, f.tokenA)  // 普通成员
	convGroup := f.createConversation(t, tokenB)     // 组管理员
	convAdmin := f.createConversation(t, f.adminToken) // 平台管理员
	require.NotEqual(t, convMember, convGroup, "不同用户应拿到不同会话")
	require.NotEqual(t, convGroup, convAdmin)

	// 互不可见：各自列表只含自己的
	countOf := func(token string) int {
		w := makeRequest(f.router, http.MethodGet, "/api/v1/assistant/conversations", nil, token)
		require.Equal(t, http.StatusOK, w.Code, "列会话失败: %s", w.Body.String())
		var resp struct {
			Items []map[string]any `json:"items"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		return len(resp.Items)
	}
	require.Equal(t, 1, countOf(f.tokenA), "普通成员只该看到自己那 1 个会话")
	require.Equal(t, 1, countOf(tokenB), "组管理员只该看到自己那 1 个会话")
}

// 事件永不因保留策略被删：塞满 500 条后 `after=1` 仍能拉回全部。
//
// 这条是纯回归护栏——当前实现**不存在任何事件清理路径**（全仓无对 assistant_run_events
// 的 Delete，也无保留期 job）。但一旦将来有人加「只保留最近 N 条」的清理，
// 前端补读会静默丢事件，表现是「回复缺了一段」，从日志里几乎无法归因。
func TestAssistantChat_EventsNeverPruned(t *testing.T) {
	f := newAssistantChatFixture(t)
	convID := f.createConversation(t, f.tokenA)

	// 建一条 run 行（直接插库：本用例只关心事件表读取语义，不必真跑模型）
	ownerID := f.conversationOwnerID(t, convID)
	run := &model.AssistantRun{
		ConversationID: convID,
		OwnerUserID:    ownerID,
		ModelSnapshot:  `{}`,
		Status:         "completed",
	}
	require.NoError(t, f.db.Create(run).Error, "建 run 行失败")
	runID := run.ID
	var maxSeq int
	require.NoError(t, f.db.Model(&model.AssistantEvent{}).
		Where("run_id = ?", runID).
		Select("COALESCE(MAX(seq),0)").Scan(&maxSeq).Error)

	// 插 501 条而不是 500：`after=1` 的语义是「seq > 1」，会排除 seq=1 那条。
	// 若只插 500 条（seq 1..500），after=1 只能拿到 499 条——那是游标语义正确，
	// 不是事件被裁剪。插 501 条后 after=1 恰好应拿到 500 条，与验收原文口径一致。
	const want = 500
	evs := make([]model.AssistantEvent, want+1)
	for i := range evs {
		evs[i] = model.AssistantEvent{
			ConversationID: convID,
			RunID:          runID,
			Seq:            maxSeq + i + 1,
			Type:           "text_delta",
			Data:           `{"text":"x"}`,
		}
	}
	require.NoError(t, f.db.CreateInBatches(evs, 100).Error, "批量插事件失败")

	w := makeRequest(f.router, http.MethodGet,
		fmt.Sprintf("/api/v1/assistant/runs/%d/events?after=1", runID), nil, f.tokenA)
	require.Equal(t, http.StatusOK, w.Code,
		"事件补读不该返回错误（尤其不是 410——事件永不清理）: %s", w.Body.String())

	var frames []map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &frames))
	require.GreaterOrEqual(t, len(frames), want,
		"after=1 应能拉回全部 %d 条，实际 %d 条——说明事件被裁剪了", want, len(frames))
}
