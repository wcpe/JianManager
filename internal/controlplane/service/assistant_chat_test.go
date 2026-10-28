package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/ai"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// FR-515 Phase C 的运行时用例。
//
// 两个刻意的测试口径：
//   - 模型层全部用**假适配器**（ai.Streamer 的手写实现）注入，不打真实网络——
//     这些用例验证的是编排与持久化语义，不是协议解析；
//   - 只迁移助手运行时四张表：全量 AutoMigrate 会把无关表的建表开销与无关迁移的
//     失败都混进来，让「这一层的语义是否成立」变得不可判读。

// ── 脚手架 ──────────────────────────────────────────────────────────────

// newAssistantChatTestDB 建只迁移助手运行时四张表的内存库。
//
// 连接池刻意限制为 1：内存 SQLite 的 shared-cache 用表级锁，并发写会报
// SQLITE_LOCKED（busy_timeout 对它无效，见 router/testhelper_test.go 的踩坑记录），
// 而本层要验证的是「唯一索引 + 判序逻辑」而非并发吞吐。串行化连接池消除这类噪声，
// 不改变任何断言——8 路并发提交仍然会真实地竞争插入，只是不再撞 SQLite 的锁实现细节。
func newAssistantChatTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.AssistantConversation{},
		&model.AssistantMessage{},
		&model.AssistantRun{},
		&model.AssistantEvent{},
	))
	// 单会话单活跃 run / 幂等键的唯一性由**部分唯一索引**保证，GORM tag 表达不了谓词，
	// 所以这里必须手工建——真库由 database.migrateAssistantRunUniqueIndexes 在
	// AutoMigrate 之后建，但那处在另一个包且未导出，service 层够不到。
	//
	// 活跃态谓词从 model.AssistantRunActiveStatuses 现拼，不写第二份字面量：
	// 两处一旦漂移，索引会放过本该冲突的插入，而这层用例正是唯一会因此变红的哨兵。
	quoted := make([]string, 0, len(model.AssistantRunActiveStatuses()))
	for _, status := range model.AssistantRunActiveStatuses() {
		quoted = append(quoted, "'"+status+"'")
	}
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_assistant_runs_active
		ON assistant_runs (conversation_id) WHERE status IN (`+strings.Join(quoted, ", ")+`)`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_assistant_runs_idem
		ON assistant_runs (owner_user_id, conversation_id, idempotency_key)
		WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// streamScript 假适配器的脚本：收到请求后按脚本发事件。
type streamScript func(ctx context.Context, emit ai.Emit) error

// fakeStreamer ai.Streamer 的手写实现，记录收到的请求以便断言「请求里有什么」。
type fakeStreamer struct {
	script streamScript
	mu     sync.Mutex
	reqs   []ai.Request
}

// Stream 实现 ai.Streamer。
func (f *fakeStreamer) Stream(ctx context.Context, req ai.Request, emit ai.Emit) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.script == nil {
		return nil
	}
	return f.script(ctx, emit)
}

// requests 返回迄今收到的所有请求副本。
func (f *fakeStreamer) requests() []ai.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ai.Request(nil), f.reqs...)
}

// textScript 发若干文本增量 + 一条真实用量 + 正常结束。
func textScript(chunks ...string) streamScript {
	return func(_ context.Context, emit ai.Emit) error {
		for _, chunk := range chunks {
			if err := emit(ai.Event{Kind: ai.EventText, Text: chunk}); err != nil {
				return err
			}
		}
		in, out := 11, 22
		if err := emit(ai.Event{Kind: ai.EventUsage, Usage: &ai.Usage{InputTokens: &in, OutputTokens: &out}}); err != nil {
			return err
		}
		return emit(ai.Event{Kind: ai.EventEnd, FinishReason: "stop"})
	}
}

// hangingScript 一直阻塞到上下文结束（用于「必须有活跃 run」的用例）。
func hangingScript() streamScript {
	return func(ctx context.Context, _ ai.Emit) error {
		<-ctx.Done()
		return ctx.Err()
	}
}

// toolCallScript 每轮都发同一个工具调用：把轮次预算烧到上限。
func toolCallScript(callID string) streamScript {
	return func(_ context.Context, emit ai.Emit) error {
		if err := emit(ai.Event{Kind: ai.EventToolCallStart, CallID: callID, ToolName: "fake_tool"}); err != nil {
			return err
		}
		if err := emit(ai.Event{
			Kind:     ai.EventToolCallEnd,
			CallID:   callID,
			ToolName: "fake_tool",
			Args:     `{"target":"demo"}`,
		}); err != nil {
			return err
		}
		return emit(ai.Event{Kind: ai.EventEnd, FinishReason: "tool_use"})
	}
}

// fakeToolExecutor ToolExecutor 的手写实现（FR-516 在本 FR 里不存在）。
type fakeToolExecutor struct {
	mu     sync.Mutex
	calls  []ai.ToolCall
	result string
	err    error
}

// Available 始终提供一个工具（是否真的注册由被调用方决定）。
func (f *fakeToolExecutor) Available(_ context.Context, _ uint) []ai.ToolSpec {
	return []ai.ToolSpec{{
		Name:        "fake_tool",
		Description: "测试用工具，无任何副作用",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"}}}`),
	}}
}

// Execute 记录调用并返回预设结果。
func (f *fakeToolExecutor) Execute(_ context.Context, _ uint, call ai.ToolCall) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.err != nil {
		return "", f.err
	}
	return f.result, nil
}

// chatHarness 一套「内存库 + 假适配器」的服务与观察点。
type chatHarness struct {
	svc      *AssistantChatService
	db       *gorm.DB
	streamer *fakeStreamer
}

// harnessOpt 装配选项。
type harnessOpt struct {
	// tools 快照是否声明 tools 能力（决定要不要尝试注册工具）。
	tools bool
	// script 假适配器的脚本；留空表示发一句「你好」。
	script streamScript
}

// newChatHarness 组装服务并注入假适配器。
func newChatHarness(t *testing.T, opt harnessOpt) *chatHarness {
	t.Helper()
	db := newAssistantChatTestDB(t)
	// assistant 传 nil：模型准备函数已被替换，生产实现里的凭据解析与出站准入都不参与。
	svc := NewAssistantChatService(db, nil)
	t.Cleanup(svc.Stop)

	script := opt.script
	if script == nil {
		script = textScript("你好")
	}
	streamer := &fakeStreamer{script: script}
	snapshot := assistantModelSnapshot{
		PlatformModelID: 7,
		ProviderID:      9,
		Protocol:        model.AssistantProtocolOpenAIChat,
		UpstreamModelID: "fake-model",
		Streaming:       true,
		Tools:           opt.tools,
	}
	svc.setModelPreparer(func(*model.AssistantConversation) (assistantModelSnapshot, ai.Streamer, error) {
		return snapshot, streamer, nil
	})
	return &chatHarness{svc: svc, db: db, streamer: streamer}
}

// createConversation 建一个会话。
func (h *chatHarness) createConversation(t *testing.T, owner uint) *model.AssistantConversation {
	t.Helper()
	conv, err := h.svc.CreateConversation(owner, "测试会话", nil)
	require.NoError(t, err)
	return conv
}

// submit 提交一轮。
func (h *chatHarness) submit(t *testing.T, owner, convID uint, text, key string) *model.AssistantRun {
	t.Helper()
	run, err := h.svc.SubmitRun(context.Background(), owner, convID, SubmitRunBody{Text: text}, key)
	require.NoError(t, err)
	require.NotNil(t, run)
	return run
}

// reloadRun 读回 run 的当前状态。
func (h *chatHarness) reloadRun(t *testing.T, runID uint) *model.AssistantRun {
	t.Helper()
	var run model.AssistantRun
	require.NoError(t, h.db.First(&run, runID).Error)
	return &run
}

// events 按 seq 升序返回该 run 的事件。
func (h *chatHarness) events(t *testing.T, runID uint) []model.AssistantEvent {
	t.Helper()
	var evs []model.AssistantEvent
	require.NoError(t, h.db.Where("run_id = ?", runID).Order("seq asc").Find(&evs).Error)
	return evs
}

// countEventType 统计某类事件条数。
func (h *chatHarness) countEventType(t *testing.T, runID uint, typ string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.db.Model(&model.AssistantEvent{}).
		Where("run_id = ? AND type = ?", runID, typ).Count(&n).Error)
	return n
}

// countRuns 统计某会话的 run 条数。
func (h *chatHarness) countRuns(t *testing.T, convID uint) int64 {
	t.Helper()
	var n int64
	require.NoError(t, h.db.Model(&model.AssistantRun{}).Where("conversation_id = ?", convID).Count(&n).Error)
	return n
}

// waitTerminal 等 run 进入终态**且** run_end 事件已落库。
//
// 两者不是原子写入（先 CAS 状态、再写终态事件），只等状态会让用例偶发读到
// 「已经是 completed 但还没有 run_end」的半截快照。
func (h *chatHarness) waitTerminal(t *testing.T, runID uint) *model.AssistantRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var run model.AssistantRun
		require.NoError(t, h.db.First(&run, runID).Error)
		if isTerminalRunStatus(run.Status) && h.countEventType(t, runID, model.AssistantEventTypeRunEnd) > 0 {
			return &run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %d 未在超时内进入终态", runID)
	return nil
}

// ── 1. 幂等 ─────────────────────────────────────────────────────────────

// TestAssistantChat_SubmitRunIdempotent 同键同摘要重复提交只产生一个 run；异摘要 409。
func TestAssistantChat_SubmitRunIdempotent(t *testing.T) {
	h := newChatHarness(t, harnessOpt{})
	const owner = uint(1001)
	conv := h.createConversation(t, owner)

	first := h.submit(t, owner, conv.ID, "帮我看看节点状态", "key-1")
	h.waitTerminal(t, first.ID)

	// 同键同摘要：返回**原来那一条**，不新建。
	second, err := h.svc.SubmitRun(context.Background(), owner, conv.ID,
		SubmitRunBody{Text: "帮我看看节点状态"}, "key-1")
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID, "同键同摘要必须复用原 run")
	require.Equal(t, int64(1), h.countRuns(t, conv.ID), "库内只能有一条 run")

	// 同键异摘要：同一个键被用来提交了不同内容 → 冲突，且不新建。
	_, err = h.svc.SubmitRun(context.Background(), owner, conv.ID,
		SubmitRunBody{Text: "换一个问题"}, "key-1")
	require.ErrorIs(t, err, ErrAssistantIdempotencyConflict)
	require.Equal(t, int64(1), h.countRuns(t, conv.ID), "冲突不得留下第二条 run")

	// 换一个键则允许再开一轮（幂等键只约束同一个键的重复提交）。
	third := h.submit(t, owner, conv.ID, "换一个问题", "key-2")
	require.NotEqual(t, first.ID, third.ID)
	h.waitTerminal(t, third.ID)
}

// ── 2. 并发幂等 ─────────────────────────────────────────────────────────

// TestAssistantChat_SubmitRunConcurrentSameKey 8 路同键同摘要并发提交，库内只有一条 run。
//
// 竞争窗口在「查键未命中 → 插入」之间：没有「插入冲突即回读」这一条，8 路里会有 7 路
// 直接失败或各插一条。栅栏（close(start)）保证它们真的同时冲进去。
func TestAssistantChat_SubmitRunConcurrentSameKey(t *testing.T) {
	h := newChatHarness(t, harnessOpt{})
	const owner = uint(1002)
	conv := h.createConversation(t, owner)

	const n = 8
	runs := make([]*model.AssistantRun, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			runs[i], errs[i] = h.svc.SubmitRun(context.Background(), owner, conv.ID,
				SubmitRunBody{Text: "并发同一轮"}, "same-key")
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range errs {
		require.NoError(t, errs[i], "第 %d 路同键提交不得失败", i)
		require.NotNil(t, runs[i])
		require.Equal(t, runs[0].ID, runs[i].ID, "第 %d 路必须拿到同一个 runId", i)
	}
	require.Equal(t, int64(1), h.countRuns(t, conv.ID), "并发同键只能落一条 run")
	h.waitTerminal(t, runs[0].ID)
}

// ── 3. 单会话单活跃 run ─────────────────────────────────────────────────

// TestAssistantChat_ConversationBusy 已有活跃 run 时再提交 → CONVERSATION_BUSY 并带出 runId。
func TestAssistantChat_ConversationBusy(t *testing.T) {
	h := newChatHarness(t, harnessOpt{script: hangingScript()})
	const owner = uint(1003)
	conv := h.createConversation(t, owner)

	first := h.submit(t, owner, conv.ID, "第一轮", "")

	_, err := h.svc.SubmitRun(context.Background(), owner, conv.ID, SubmitRunBody{Text: "第二轮"}, "")
	require.ErrorIs(t, err, ErrAssistantConversationBusy)
	// 错误必须带出活跃 runId，否则前端无从「接续观察」（§5.2 末段）。
	var busy *AssistantConversationBusyError
	require.ErrorAs(t, err, &busy)
	require.Equal(t, first.ID, busy.RunID)
	require.Contains(t, err.Error(), "活跃 run")

	require.NoError(t, h.svc.Cancel(owner, first.ID))
	h.waitTerminal(t, first.ID)

	// 终态释放槽位后可以再提交。
	second := h.submit(t, owner, conv.ID, "第三轮", "")
	require.NotEqual(t, first.ID, second.ID)
	require.NoError(t, h.svc.Cancel(owner, second.ID))
	h.waitTerminal(t, second.ID)
}

// ── 4. 跨会话并发上限 ───────────────────────────────────────────────────

// TestAssistantChat_TooManyRuns 同用户已有 2 个活跃 run 时第 3 个被拒。
func TestAssistantChat_TooManyRuns(t *testing.T) {
	h := newChatHarness(t, harnessOpt{script: hangingScript()})
	const owner = uint(1004)
	conv1 := h.createConversation(t, owner)
	conv2 := h.createConversation(t, owner)
	conv3 := h.createConversation(t, owner)

	first := h.submit(t, owner, conv1.ID, "一", "")
	second := h.submit(t, owner, conv2.ID, "二", "")

	_, err := h.svc.SubmitRun(context.Background(), owner, conv3.ID, SubmitRunBody{Text: "三"}, "")
	require.ErrorIs(t, err, ErrAssistantTooManyRuns)
	require.Equal(t, int64(0), h.countRuns(t, conv3.ID), "被拒的提交不得留下 run")

	// 另一个用户不受影响（上限按 owner 计，不是全局）。
	other := h.createConversation(t, owner+1)
	otherRun := h.submit(t, owner+1, other.ID, "别人的一轮", "")
	require.NotZero(t, otherRun.ID)

	for _, run := range []*model.AssistantRun{first, second, otherRun} {
		require.NoError(t, h.svc.Cancel(run.OwnerUserID, run.ID))
		h.waitTerminal(t, run.ID)
	}
}

// ── 5. 越权 ─────────────────────────────────────────────────────────────

// TestAssistantChat_CrossOwnerInvisible 他人会话的每条读写路径都返回 NotFound（不是 403）。
//
// 统一 404 而不区分「不存在」与「无权」，是为了不把会话 id 变成一个存在性探测接口（§6）。
func TestAssistantChat_CrossOwnerInvisible(t *testing.T) {
	h := newChatHarness(t, harnessOpt{})
	const ownerA = uint(1005)
	const ownerB = uint(1006)
	conv := h.createConversation(t, ownerA)
	title := "改名"
	modelID := uint(3)
	var modelIDPtr *uint = &modelID

	_, err := h.svc.GetConversation(ownerB, conv.ID)
	require.ErrorIs(t, err, ErrAssistantChatNotFound)

	_, err = h.svc.UpdateConversation(ownerB, conv.ID, &title, nil)
	require.ErrorIs(t, err, ErrAssistantChatNotFound)

	_, err = h.svc.UpdateConversation(ownerB, conv.ID, nil, &modelIDPtr)
	require.ErrorIs(t, err, ErrAssistantChatNotFound)

	require.ErrorIs(t, h.svc.DeleteConversation(ownerB, conv.ID), ErrAssistantChatNotFound)

	_, err = h.svc.ListMessages(ownerB, conv.ID, nil, 10)
	require.ErrorIs(t, err, ErrAssistantChatNotFound)

	_, err = h.svc.SubmitRun(context.Background(), ownerB, conv.ID, SubmitRunBody{Text: "越权提交"}, "k")
	require.ErrorIs(t, err, ErrAssistantChatNotFound)

	// 列表里也看不到。
	list, _, err := h.svc.ListConversations(ownerB, "", 10)
	require.NoError(t, err)
	require.Empty(t, list)

	// 会话本身没被上面的越权尝试改坏。
	got, err := h.svc.GetConversation(ownerA, conv.ID)
	require.NoError(t, err)
	require.Equal(t, conv.Title, got.Title)
	require.Nil(t, got.ModelID)
	require.Equal(t, int64(0), h.countRuns(t, conv.ID))
}

// ── 6. 事件 seq 连续 ────────────────────────────────────────────────────

// TestAssistantChat_EventSeqContiguousFromOne 跑完一轮后 seq 从 1 到 N 无空洞，run_end 恰好一条。
func TestAssistantChat_EventSeqContiguousFromOne(t *testing.T) {
	h := newChatHarness(t, harnessOpt{script: textScript("你", "好", "呀")})
	const owner = uint(1007)
	conv := h.createConversation(t, owner)

	run := h.submit(t, owner, conv.ID, "打个招呼", "seq-key")
	done := h.waitTerminal(t, run.ID)

	require.Equal(t, model.AssistantRunStatusCompleted, done.Status)
	require.NotNil(t, done.TerminationReason)
	require.Equal(t, model.AssistantTerminationCompleted, *done.TerminationReason)
	// 供应商用量只记真报的值（本用例的假适配器报了 11/22）。
	require.NotNil(t, done.UsageInput)
	require.Equal(t, 11, *done.UsageInput)

	evs := h.events(t, run.ID)
	require.NotEmpty(t, evs)
	for i, ev := range evs {
		require.Equal(t, i+1, ev.Seq, "seq 必须从 1 连续递增，不允许空洞")
	}
	require.Equal(t, int64(1), h.countEventType(t, run.ID, model.AssistantEventTypeRunEnd), "run_end 每 run 恰好一条")
	require.Equal(t, model.AssistantEventTypeRunEnd, evs[len(evs)-1].Type, "run_end 必须是最后一条")
	require.Equal(t, int64(3), h.countEventType(t, run.ID, model.AssistantEventTypeTextDelta))

	// 增量合并成一条 assistant 消息，而不是每个 delta 一条。
	var msgs []model.AssistantMessage
	require.NoError(t, h.db.Where("conversation_id = ?", conv.ID).Order("id asc").Find(&msgs).Error)
	assistantCount := 0
	for _, msg := range msgs {
		if msg.Role == model.AssistantMessageRoleAssistant {
			assistantCount++
			require.Equal(t, "你好呀", msg.Content)
			require.Equal(t, model.AssistantMessageStateComplete, msg.State)
		}
	}
	require.Equal(t, 1, assistantCount, "文本增量必须合并成一条 assistant 消息")

	// 会话活跃指针已清理。
	got, err := h.svc.GetConversation(owner, conv.ID)
	require.NoError(t, err)
	require.Nil(t, got.ActiveRunID)
}

// ── 7. 取消 ─────────────────────────────────────────────────────────────

// TestAssistantChat_Cancel 取消后 run 落 cancelled 且补 run_end；重复取消不报错。
func TestAssistantChat_Cancel(t *testing.T) {
	h := newChatHarness(t, harnessOpt{script: hangingScript()})
	const owner = uint(1008)
	conv := h.createConversation(t, owner)

	run := h.submit(t, owner, conv.ID, "跑到一半我改主意了", "cancel-key")
	require.NoError(t, h.svc.Cancel(owner, run.ID))
	done := h.waitTerminal(t, run.ID)

	require.Equal(t, model.AssistantRunStatusCancelled, done.Status)
	require.NotNil(t, done.TerminationReason)
	require.Equal(t, model.AssistantTerminationCancelled, *done.TerminationReason)
	require.Equal(t, int64(1), h.countEventType(t, run.ID, model.AssistantEventTypeRunEnd))

	// 幂等：已终态再取消不报错。
	require.NoError(t, h.svc.Cancel(owner, run.ID))

	// 越权取消：他人看不到这条 run。
	require.ErrorIs(t, h.svc.Cancel(owner+1, run.ID), ErrAssistantChatNotFound)
}

// ── 8. 重启回收 ─────────────────────────────────────────────────────────

// TestAssistantChat_ReapOnStartup 非终态 run 变 interrupted 并各补一条 run_end；终态 run 不动。
func TestAssistantChat_ReapOnStartup(t *testing.T) {
	h := newChatHarness(t, harnessOpt{})
	const owner = uint(1009)
	conv1 := h.createConversation(t, owner)
	conv2 := h.createConversation(t, owner)
	conv3 := h.createConversation(t, owner)

	running := h.insertRun(t, conv1.ID, owner, model.AssistantRunStatusRunning, nil)
	waiting := h.insertRun(t, conv2.ID, owner, model.AssistantRunStatusWaitingConfirmation, nil)
	completedReason := model.AssistantTerminationCompleted
	completed := h.insertRun(t, conv3.ID, owner, model.AssistantRunStatusCompleted, &completedReason)
	// 会话的活跃指针指向那条 running run，回收后必须被清掉。
	require.NoError(t, h.db.Model(&model.AssistantConversation{}).Where("id = ?", conv1.ID).
		Updates(map[string]any{"active_run_id": running.ID}).Error)

	reaped, err := h.svc.ReapOnStartup()
	require.NoError(t, err)
	require.Equal(t, 2, reaped)

	for _, tc := range []struct {
		id   uint
		conv uint
	}{{running.ID, conv1.ID}, {waiting.ID, conv2.ID}} {
		got := h.reloadRun(t, tc.id)
		require.Equal(t, model.AssistantRunStatusInterrupted, got.Status)
		require.NotNil(t, got.TerminationReason)
		require.Equal(t, model.AssistantTerminationInterrupted, *got.TerminationReason)
		require.Equal(t, int64(1), h.countEventType(t, tc.id, model.AssistantEventTypeRunEnd),
			"被回收的 run 必须补一条 run_end")
	}

	// completed 的 run 一律不动（§8：已经落定的终态不得改写成「未知」）。
	untouched := h.reloadRun(t, completed.ID)
	require.Equal(t, model.AssistantRunStatusCompleted, untouched.Status)
	require.NotNil(t, untouched.TerminationReason)
	require.Equal(t, model.AssistantTerminationCompleted, *untouched.TerminationReason)
	require.Equal(t, int64(0), h.countEventType(t, completed.ID, model.AssistantEventTypeRunEnd),
		"终态 run 不得被追加 run_end")

	// 活跃指针被清空。
	got, err := h.svc.GetConversation(owner, conv1.ID)
	require.NoError(t, err)
	require.Nil(t, got.ActiveRunID)

	// 幂等：再跑一次不应再回收任何 run，也不产出第二条 run_end。
	again, err := h.svc.ReapOnStartup()
	require.NoError(t, err)
	require.Equal(t, 0, again)
	require.Equal(t, int64(1), h.countEventType(t, running.ID, model.AssistantEventTypeRunEnd))
}

// insertRun 直接往库里塞一条 run（模拟上一个进程遗留的残留行）。
func (h *chatHarness) insertRun(t *testing.T, convID, owner uint, status string, reason *string) *model.AssistantRun {
	t.Helper()
	run := &model.AssistantRun{
		ConversationID:    convID,
		OwnerUserID:       owner,
		ModelSnapshot:     `{"protocol":"openai-chat","upstreamModelId":"fake-model"}`,
		ToolPolicyVersion: 1,
		Status:            status,
		TerminationReason: reason,
		Version:           1,
	}
	require.NoError(t, h.db.Create(run).Error)
	return run
}

// ── 9. 预算 ─────────────────────────────────────────────────────────────

// TestAssistantChat_BudgetExhausted 工具轮次超限 → completed + budget_exhausted，不无限循环。
//
// 轮次上限在这里被调到 1：默认的 8 轮要靠真跑 8 次往返才触达，用例既慢又难读。
// 可调本身就是要求——预算是服务端硬约束，但它的**数值**不该被测试反向固化。
func TestAssistantChat_BudgetExhausted(t *testing.T) {
	h := newChatHarness(t, harnessOpt{tools: true, script: toolCallScript("call-budget")})
	const owner = uint(1010)
	conv := h.createConversation(t, owner)

	tools := &fakeToolExecutor{result: "工具结果"}
	h.svc.SetToolExecutor(tools)
	limits := defaultAssistantChatLimits()
	limits.MaxToolRounds = 1
	h.svc.setLimits(limits)

	run := h.submit(t, owner, conv.ID, "一直调工具", "budget-key")
	done := h.waitTerminal(t, run.ID)

	require.Equal(t, model.AssistantRunStatusCompleted, done.Status)
	require.NotNil(t, done.TerminationReason)
	require.Equal(t, model.AssistantTerminationBudgetExhausted, *done.TerminationReason)

	// 只执行了首轮的那一次调用，第二轮被预算拦下。
	require.Len(t, tools.calls, 1)
	require.Equal(t, "call-budget", tools.calls[0].CallID)
	require.LessOrEqual(t, len(h.streamer.requests()), 2, "达到轮次上限后必须停止请求模型")

	require.Equal(t, int64(1), h.countEventType(t, run.ID, model.AssistantEventTypeRunEnd))
	// 收尾说明写进 run_end 的 note（§9：终态事件要说明「工具轮次已达上限」）。
	evs := h.events(t, run.ID)
	var runEnd model.AssistantEvent
	for _, ev := range evs {
		if ev.Type == model.AssistantEventTypeRunEnd {
			runEnd = ev
		}
	}
	require.Equal(t, model.AssistantEventTypeRunEnd, runEnd.Type)
	require.Contains(t, runEnd.Data, "工具轮次已达上限")
	require.Contains(t, runEnd.Data, model.AssistantTerminationBudgetExhausted)

	for i, ev := range evs {
		require.Equal(t, i+1, ev.Seq)
	}
}

// ── 10. 工具缝 ──────────────────────────────────────────────────────────

// TestAssistantChat_NoToolExecutor 未注入 ToolExecutor 时请求不含工具、不产生工具事件。
//
// 假适配器仍然「返回」了一个工具调用：这模拟模型自作主张地发起调用。此时既不能执行
// （没人能执行），也不该把调用泄漏成事件——那会让前端显示一个永远不会有结果的调用。
func TestAssistantChat_NoToolExecutor(t *testing.T) {
	h := newChatHarness(t, harnessOpt{tools: true, script: toolCallScript("call-ghost")})
	const owner = uint(1011)
	conv := h.createConversation(t, owner)

	run := h.submit(t, owner, conv.ID, "没有工具的执行器", "nostool-key")
	done := h.waitTerminal(t, run.ID)

	reqs := h.streamer.requests()
	require.Len(t, reqs, 1)
	require.Empty(t, reqs[0].Tools, "未注入执行器时请求里不得注册任何工具")
	require.Equal(t, "fake-model", reqs[0].Model, "模型标识来自 run 的冻结快照")
	require.Equal(t, "没有工具的执行器", reqs[0].Messages[len(reqs[0].Messages)-1].Content)

	require.Equal(t, model.AssistantRunStatusCompleted, done.Status)
	require.Equal(t, int64(0), h.countEventType(t, run.ID, model.AssistantEventTypeToolCall))
	require.Equal(t, int64(0), h.countEventType(t, run.ID, model.AssistantEventTypeToolResult))

	var toolMsgs int64
	require.NoError(t, h.db.Model(&model.AssistantMessage{}).
		Where("conversation_id = ? AND role = ?", conv.ID, model.AssistantMessageRoleTool).Count(&toolMsgs).Error)
	require.Zero(t, toolMsgs, "没有执行器就不得留下工具消息")
}

// ── 11. 历史装配保留结构化工具调用（§7.1）────────────────────────────────

// TestAssistantChat_HistoryKeepsStructuredToolCalls 第二轮请求必须把上一轮的工具调用
// 作为结构化 ToolCalls 外发，且对应的 tool 消息带 ToolCallID——不能降级成正文文本。
func TestAssistantChat_HistoryKeepsStructuredToolCalls(t *testing.T) {
	h := newChatHarness(t, harnessOpt{tools: true})
	const owner = uint(1012)
	conv := h.createConversation(t, owner)

	tools := &fakeToolExecutor{result: "节点全部在线"}
	h.svc.SetToolExecutor(tools)

	// 第一轮：模型调用一次工具，然后给出结论。
	round := 0
	var mu sync.Mutex
	h.streamer.script = func(_ context.Context, emit ai.Emit) error {
		mu.Lock()
		round++
		current := round
		mu.Unlock()
		if current == 1 {
			if err := emit(ai.Event{Kind: ai.EventToolCallEnd, CallID: "call-hist", ToolName: "fake_tool", Args: `{"target":"nodes"}`}); err != nil {
				return err
			}
			return emit(ai.Event{Kind: ai.EventEnd, FinishReason: "tool_use"})
		}
		if err := emit(ai.Event{Kind: ai.EventText, Text: "节点全部在线"}); err != nil {
			return err
		}
		return emit(ai.Event{Kind: ai.EventEnd, FinishReason: "stop"})
	}

	first := h.submit(t, owner, conv.ID, "节点都好吗", "hist-1")
	h.waitTerminal(t, first.ID)

	// 第二轮：新 run 重新装配历史。
	second := h.submit(t, owner, conv.ID, "再确认一次", "hist-2")
	h.waitTerminal(t, second.ID)

	reqs := h.streamer.requests()
	require.GreaterOrEqual(t, len(reqs), 3)
	last := reqs[len(reqs)-1]

	var assistantWithCalls *ai.Message
	var toolMsg *ai.Message
	for i := range last.Messages {
		msg := last.Messages[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			assistantWithCalls = &last.Messages[i]
		}
		if msg.Role == "tool" {
			toolMsg = &last.Messages[i]
		}
	}
	require.NotNil(t, assistantWithCalls, "历史里的工具调用必须作为结构化 ToolCalls 外发")
	require.Len(t, assistantWithCalls.ToolCalls, 1)
	require.Equal(t, "call-hist", assistantWithCalls.ToolCalls[0].CallID)
	require.Equal(t, "fake_tool", assistantWithCalls.ToolCalls[0].ToolName)
	require.JSONEq(t, `{"target":"nodes"}`, assistantWithCalls.ToolCalls[0].Args)
	// 工具调用不得被塞进正文。
	require.NotContains(t, assistantWithCalls.Content, "call-hist")

	require.NotNil(t, toolMsg, "配对的结果必须以 role=tool 出现")
	require.Equal(t, "call-hist", toolMsg.ToolCallID)
	require.Equal(t, "fake_tool", toolMsg.ToolName)
	require.Equal(t, "节点全部在线", toolMsg.Content)
}

// ── 12. 会话列表游标 ────────────────────────────────────────────────────
// TestAssistantChat_ListConversationsCursor 游标分页不重不漏，非法游标明确报错。
func TestAssistantChat_ListConversationsCursor(t *testing.T) {
	h := newChatHarness(t, harnessOpt{})
	const owner = uint(1013)
	for i := 0; i < 5; i++ {
		h.createConversation(t, owner)
	}

	seen := make(map[uint]struct{}, 5)
	cursor := ""
	for page := 0; page < 5; page++ {
		rows, next, err := h.svc.ListConversations(owner, cursor, 2)
		require.NoError(t, err)
		for _, row := range rows {
			_, dup := seen[row.ID]
			require.False(t, dup, "游标翻页不得重复返回同一行")
			seen[row.ID] = struct{}{}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	require.Len(t, seen, 5, "游标翻页必须覆盖全部会话")

	_, _, err := h.svc.ListConversations(owner, "不是游标", 2)
	require.ErrorIs(t, err, ErrAssistantInvalidCursor)
}

// ── 13. run 总预算（墙钟）──────────────────────────────────────────────

// TestAssistantChat_RunWallClockTimedOut 超出 run 总时长 → timed_out，而不是挂死或 failed。
//
// 这与「单次模型请求超时」是两件事（§7）：前者判 timed_out，后者判 failed。
// 用 50ms 的墙钟预算代替默认 300s，否则这条分支永远跑不到。
func TestAssistantChat_RunWallClockTimedOut(t *testing.T) {
	h := newChatHarness(t, harnessOpt{script: hangingScript()})
	const owner = uint(1014)
	conv := h.createConversation(t, owner)

	limits := defaultAssistantChatLimits()
	limits.RunWallClock = 50 * time.Millisecond
	h.svc.setLimits(limits)

	run := h.submit(t, owner, conv.ID, "等太久就算了", "wallclock-key")
	done := h.waitTerminal(t, run.ID)

	require.Equal(t, model.AssistantRunStatusTimedOut, done.Status)
	require.NotNil(t, done.TerminationReason)
	require.Equal(t, model.AssistantTerminationTimedOut, *done.TerminationReason)
	require.Equal(t, int64(1), h.countEventType(t, run.ID, model.AssistantEventTypeRunEnd))
}

// ── 14. 事件载荷上限 ────────────────────────────────────────────────────

// TestAssistantChat_EventPayloadTruncated 单条事件载荷超限被截断且仍可解析（§7 EVENT_PAYLOAD_MAX）。
//
// 断言三件事：不超过上限、仍是合法 JSON、带 truncated 标记。第二点是关键——
// 直接切 JSON 文本虽然也「不超过上限」，但前端解析失败就什么也显示不出来。
func TestAssistantChat_EventPayloadTruncated(t *testing.T) {
	long := strings.Repeat("零", 40000) // 远超 32KiB（每个汉字 3 字节）
	const limit = assistantChatEventPayloadMax

	payload, err := marshalBoundedEventPayload(map[string]any{"text": long}, limit)
	require.NoError(t, err)
	require.LessOrEqual(t, len(payload), limit, "载荷不得超过 EVENT_PAYLOAD_MAX")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload), &decoded), "截断后的载荷必须仍是合法 JSON")
	require.Equal(t, true, decoded["truncated"], "截断必须显式标记")
	require.Contains(t, decoded, "text", "结构化字段不得被整段丢弃")

	// 未超限时原样返回且不带 truncated 标记。
	small, err := marshalBoundedEventPayload(map[string]any{"text": "短"}, limit)
	require.NoError(t, err)
	require.JSONEq(t, `{"text":"短"}`, small)
}

// ── 15. 会话更新三态与消息分页 ──────────────────────────────────────────

// TestAssistantChat_UpdateConversationThreeStates 标题与模型的「不改 / 设值 / 显式清空」三态。
//
// modelID 用 **uint 而不是 *uint：内层 nil 表示「清空，回跟随默认模型」，
// 外层 nil 表示「这次不动它」。用零值表示「不改」会让「清空」无法表达。
func TestAssistantChat_UpdateConversationThreeStates(t *testing.T) {
	h := newChatHarness(t, harnessOpt{})
	const owner = uint(1015)
	conv := h.createConversation(t, owner)

	// 只改标题：模型保持 nil。
	title := "手改的标题"
	got, err := h.svc.UpdateConversation(owner, conv.ID, &title, nil)
	require.NoError(t, err)
	require.Equal(t, title, got.Title)
	require.Nil(t, got.ModelID)

	// 设值：只动模型，标题不变。
	modelID := uint(42)
	setModel := &modelID
	got, err = h.svc.UpdateConversation(owner, conv.ID, nil, &setModel)
	require.NoError(t, err)
	require.Equal(t, title, got.Title, "未传标题时不得被清零")
	require.NotNil(t, got.ModelID)
	require.Equal(t, modelID, *got.ModelID)

	// 显式清空：内层 nil 必须写成 SQL NULL，而不是留旧值或写 0。
	clearModel := (*uint)(nil)
	got, err = h.svc.UpdateConversation(owner, conv.ID, nil, &clearModel)
	require.NoError(t, err)
	require.Nil(t, got.ModelID, "内层 nil 必须清空模型")

	// 空标题被拒（而不是静默接受一个空壳会话）。
	empty := "   "
	_, err = h.svc.UpdateConversation(owner, conv.ID, &empty, nil)
	require.ErrorIs(t, err, ErrAssistantInvalid)
}

// TestAssistantChat_DeleteConversationRefusesWhileRunning 有活跃 run 时拒绝删除。
//
// 允许删除会让执行体继续往一个用户看不见也删不掉的会话里写事件与消息，
// 留一个既不可观测也不可清理的幽灵 run。
func TestAssistantChat_DeleteConversationRefusesWhileRunning(t *testing.T) {
	h := newChatHarness(t, harnessOpt{script: hangingScript()})
	const owner = uint(1016)
	conv := h.createConversation(t, owner)
	run := h.submit(t, owner, conv.ID, "跑着的时候删我", "")

	err := h.svc.DeleteConversation(owner, conv.ID)
	require.ErrorIs(t, err, ErrAssistantConversationBusy)

	require.NoError(t, h.svc.Cancel(owner, run.ID))
	h.waitTerminal(t, run.ID)

	require.NoError(t, h.svc.DeleteConversation(owner, conv.ID), "终态后应允许删除")
	_, err = h.svc.GetConversation(owner, conv.ID)
	require.ErrorIs(t, err, ErrAssistantChatNotFound, "删除后必须不可读")
}

// TestAssistantChat_ListMessages 倒序取、正序返回，before 游标向前翻不动重复。
func TestAssistantChat_ListMessages(t *testing.T) {
	h := newChatHarness(t, harnessOpt{})
	const owner = uint(1017)
	conv := h.createConversation(t, owner)
	for i := 1; i <= 5; i++ {
		require.NoError(t, h.db.Create(&model.AssistantMessage{
			ConversationID: conv.ID,
			Role:           model.AssistantMessageRoleUser,
			Content:        "第" + strconv.Itoa(i) + "条",
			Turn:           i,
			State:          model.AssistantMessageStateComplete,
		}).Error)
	}

	latest, err := h.svc.ListMessages(owner, conv.ID, nil, 2)
	require.NoError(t, err)
	require.Len(t, latest, 2)
	// 正序返回：拿到的是最近的 2 条，且按时间升序排列。
	require.Equal(t, "第4条", latest[0].Content)
	require.Equal(t, "第5条", latest[1].Content)

	before := latest[0].ID
	older, err := h.svc.ListMessages(owner, conv.ID, &before, 2)
	require.NoError(t, err)
	require.Len(t, older, 2)
	require.Equal(t, "第2条", older[0].Content)
	require.Equal(t, "第3条", older[1].Content)
}

// ── 18. 上下文预算裁剪（CONTEXT_TOKEN_BUDGET） ──────────────────────────

// TestTrimToContextBudget_UnknownBudgetKeepsAll 未知预算（模型未声明窗口）时**不裁**。
//
// 这条守的是「未知就不装懂」：把 0 当成「预算为零」会裁光全部历史，
// 是比不裁剪严重得多的错误。
func TestTrimToContextBudget_UnknownBudgetKeepsAll(t *testing.T) {
	in := []ai.Message{
		{Role: "user", Content: strings.Repeat("旧", 5000)},
		{Role: "assistant", Content: strings.Repeat("新", 5000)},
	}
	got := trimToContextBudget(in, assistantChatLimits{ContextTokenBudget: 0}, nil, nil)
	require.Len(t, got, 2, "预算未知时必须原样返回")
}

// TestTrimToContextBudget_TrimsOldestFirst 预算不足时从**最旧**端裁，且保留最新消息。
func TestTrimToContextBudget_TrimsOldestFirst(t *testing.T) {
	// 每条约 4000 字节（1000 个汉字 × 3 字节）。预算 100 token × 4 = 400 字节下限，
	// 只装得下最后一条。
	in := []ai.Message{
		{Role: "user", Content: strings.Repeat("旧", 1000)},
		{Role: "assistant", Content: strings.Repeat("中", 1000)},
		{Role: "user", Content: strings.Repeat("新", 1000)},
	}
	got := trimToContextBudget(in, assistantChatLimits{ContextTokenBudget: 100}, nil, nil)
	require.NotEmpty(t, got, "至少必须保留最新一条，不能裁空")
	require.Equal(t, "新", string([]rune(got[len(got)-1].Content)[0]), "最新一条必须保留")
	for _, m := range got {
		require.NotEqual(t, "旧", string([]rune(m.Content)[0]), "最旧的一条应被裁掉")
	}
}

// TestTrimToContextBudget_NeverSplitsToolRound 裁剪不得把 assistant 的 tool_calls
// 与它的 tool 结果切开——切一半会让上游直接判请求非法（§7.1）。
func TestTrimToContextBudget_NeverSplitsToolRound(t *testing.T) {
	in := []ai.Message{
		{Role: "user", Content: strings.Repeat("旧", 2000)},
		{Role: "assistant", Content: "", ToolCalls: []ai.ToolCall{{CallID: "c1", ToolName: "t", Args: "{}"}}},
		{Role: "tool", Content: strings.Repeat("果", 2000), ToolCallID: "c1", ToolName: "t"},
		{Role: "user", Content: "新问题"},
	}
	got := trimToContextBudget(in, assistantChatLimits{ContextTokenBudget: 200}, nil, nil)

	// 结果里不允许出现「无宿主调用的 tool 消息」。
	for i, m := range got {
		if m.Role != "tool" {
			continue
		}
		require.Greater(t, i, 0, "tool 消息不得位于切片首位（会失去宿主调用）")
		require.Equal(t, "assistant", got[i-1].Role, "tool 消息前面必须是带 tool_calls 的 assistant")
	}
	require.Equal(t, "新问题", got[len(got)-1].Content, "最新消息必须保留")
}

// ── 19. 运行期事件广播（Phase D 补充） ───────────────────────────────────

// TestAssistantChatHub_PublishAfterPersist 广播必须发生在**落库成功之后**。
//
// 判据不能是「收到广播后查库能查到」——同一 goroutine 里 Create 一定先于测试查库，
// 那种写法即使把广播挪到落库之前也会通过（已实测：变异后仍绿，属无效测试）。
//
// 所以这里**让落库失败**（同 run 同 seq 冲突），再断言订阅者一条都收不到：
// 只有「先写库、写成功才广播」的实现才能通过。先发后写的实现会在这里漏出广播。
func TestAssistantChatHub_PublishAfterPersist(t *testing.T) {
	h := newChatHarness(t, harnessOpt{})
	const owner = uint(1016)
	conv := h.createConversation(t, owner)

	run := &model.AssistantRun{ConversationID: conv.ID, OwnerUserID: owner, Status: model.AssistantRunStatusRunning}
	require.NoError(t, h.db.Create(run).Error)

	// 预先占住 seq=1，使后续插入必然冲突。
	require.NoError(t, h.db.Create(&model.AssistantEvent{
		ConversationID: conv.ID, RunID: run.ID, Seq: 1,
		Type: model.AssistantEventTypeTextDelta, Data: `{"text":"占位"}`,
	}).Error)

	live, cancel := h.svc.SubscribeRunEvents(run.ID)
	defer cancel()

	sink := newAssistantEventSink(h.db, run, defaultAssistantChatLimits())
	sink.hub = h.svc.hub
	sink.nextSeq = 1 // 强制撞上已占用的 seq

	require.Error(t, sink.append(model.AssistantEventTypeTextDelta, map[string]any{"text": "不应广播"}, nil, nil),
		"同 seq 重复插入必须失败")

	select {
	case ev := <-live:
		t.Fatalf("落库失败仍广播了 seq=%d：先发后写会让订阅者卡在库里不存在的空洞处", ev.Seq)
	case <-time.After(300 * time.Millisecond):
		// 正确：没有广播。
	}
}

// TestAssistantChatHub_SlowSubscriberDoesNotBlockWriter 订阅者不消费时，写入方不得被阻塞。
//
// 若这里阻塞，一个卡住的浏览器就能把整个 run 的执行体拖死——事件已落库，
// 丢掉广播由订阅者自己按 seq 补读即可自愈。
func TestAssistantChatHub_SlowSubscriberDoesNotBlockWriter(t *testing.T) {
	h := newChatHarness(t, harnessOpt{})
	const owner = uint(1017)
	conv := h.createConversation(t, owner)

	run := &model.AssistantRun{ConversationID: conv.ID, OwnerUserID: owner, Status: model.AssistantRunStatusRunning}
	require.NoError(t, h.db.Create(run).Error)

	// 订阅但**永不消费**：写满缓冲后再写，仍然必须立即返回。
	_, cancel := h.svc.SubscribeRunEvents(run.ID)
	defer cancel()

	sink := newAssistantEventSink(h.db, run, defaultAssistantChatLimits())
	sink.hub = h.svc.hub

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < assistantEventHubBuffer+20; i++ {
			require.NoError(t, sink.append(model.AssistantEventTypeTextDelta,
				map[string]any{"text": "x"}, nil, nil))
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("订阅者不消费时写入方被阻塞：一个慢客户端会拖死整个 run")
	}

	// 丢弃不影响落库完整性：所有事件都必须在库里。
	var count int64
	require.NoError(t, h.db.Model(&model.AssistantEvent{}).Where("run_id = ?", run.ID).Count(&count).Error)
	require.Equal(t, int64(assistantEventHubBuffer+20), count, "广播可丢，落库不可丢")
}
