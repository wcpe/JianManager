package service

/**
 * FR-491 审批执行器的守门用例。
 *
 * 这组断言守的是「**批准的**与**执行的**必须是同一件事」这条底线：
 *  1. 参数被篡改 → 不执行（且票据落 failed，不静默成功）；
 *  2. 目标漂移 → 不执行，保留批准事实与失败原因；
 *  3. 未登记 adapter → 失败关闭（不是静默成功）；
 *  4. 认领 CAS：同一张票只能被执行一次；
 *  5. 超时 → 回执 unverified，不自动重试。
 *
 * 断言一律包含「adapter 是否被调用」的次数，而不只看返回错误——
 * 返回 failed 但实际执行了，是最危险的形态。
 */

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newExecutorFixture 造一个已批准（executing）的票据 + 执行器。
func newExecutorFixture(t *testing.T, action string, payload map[string]any) (*gorm.DB, *ApprovalService, *ApprovalExecutor, *model.ApprovalTicket) {
	t.Helper()
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	ex := NewApprovalExecutor(db, svc, nil) // tasks=nil：同步执行，便于断言

	frozen, err := json.Marshal(payload)
	require.NoError(t, err)
	hash, err := hashApprovalPayload(payload)
	require.NoError(t, err)

	tk := &model.ApprovalTicket{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantAgent,
		Action: action, TargetType: "instance", TargetID: "7",
		PayloadHash: hash, PayloadFrozen: string(frozen),
		Evidence: `{"status":"RUNNING","version":3}`,
		Reason:   "测试", Status: model.ApprovalStatusPending,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	require.NoError(t, db.Create(tk).Error)

	// 走**真实的批准路径**而不是直接造 executing：否则 decided_at/decided_by 永远是空的，
	// 「保留批准事实」这条断言就成了空转（第一版 fixture 正是这么错的）。
	_, err = svc.Decide(context.Background(), DecideApprovalParams{
		TicketID: tk.ID, ActorUserID: 99, Approve: true,
	})
	require.NoError(t, err)
	require.NoError(t, db.First(tk, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusExecuting, tk.Status)
	return db, svc, ex, tk
}

// TestExecutor_RunsRegisteredAdapter 正常路径：adapter 被调用一次，票据 succeeded。
func TestExecutor_RunsRegisteredAdapter(t *testing.T) {
	payload := map[string]any{"instanceId": float64(7)}
	db, _, ex, tk := newExecutorFixture(t, "mcp.instance_stop", payload)

	calls := 0
	var gotPayload map[string]any
	ex.RegisterAdapter("mcp.instance_stop", func(ctx context.Context, tt *model.ApprovalTicket, p map[string]any) error {
		calls++
		gotPayload = p
		return nil
	})

	require.NoError(t, ex.Execute(context.Background(), tk.ID))
	require.Equal(t, 1, calls, "adapter 应被调用一次")
	// 执行的是**冻结的那一份**参数（不是请求里的新参数）。
	require.Equal(t, float64(7), gotPayload["instanceId"])

	var reloaded model.ApprovalTicket
	require.NoError(t, db.First(&reloaded, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusSucceeded, reloaded.Status)

	var rec model.ApprovalReceipt
	require.NoError(t, db.Where("ticket_id = ?", tk.ID).First(&rec).Error)
	require.Equal(t, model.ApprovalReceiptOK, rec.Outcome)
	require.NotNil(t, rec.FinishedAt)
}

// TestExecutor_TamperedPayloadDoesNotExecute 参数被篡改时不得执行。
func TestExecutor_TamperedPayloadDoesNotExecute(t *testing.T) {
	payload := map[string]any{"instanceId": float64(7)}
	db, _, ex, tk := newExecutorFixture(t, "mcp.instance_stop", payload)

	calls := 0
	ex.RegisterAdapter("mcp.instance_stop", func(context.Context, *model.ApprovalTicket, map[string]any) error {
		calls++
		return nil
	})

	// 篡改冻结参数但不改 hash（等价于有人直接改了库）。
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Where("id = ?", tk.ID).
		Update("payload_frozen", `{"instanceId":999}`).Error)

	err := ex.Execute(context.Background(), tk.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrApprovalPayloadTampered), "应报参数被篡改，得到 %v", err)
	require.Equal(t, 0, calls, "参数被篡改时**绝不能**执行")

	var reloaded model.ApprovalTicket
	require.NoError(t, db.First(&reloaded, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusFailed, reloaded.Status, "票据应落 failed 而不是 succeeded")
}

// TestExecutor_TargetDriftDoesNotExecute 目标漂移时不得执行。
func TestExecutor_TargetDriftDoesNotExecute(t *testing.T) {
	payload := map[string]any{"instanceId": float64(7)}
	db, _, ex, tk := newExecutorFixture(t, "mcp.instance_stop", payload)

	calls := 0
	ex.RegisterAdapter("mcp.instance_stop", func(context.Context, *model.ApprovalTicket, map[string]any) error {
		calls++
		return nil
	})
	// 校验器报告漂移（模拟：批准时实例是 RUNNING/version 3，执行时已变成 STOPPED/version 5）。
	ex.SetTargetVerifier(func(_ *model.ApprovalTicket, _ map[string]any) error {
		return fmt.Errorf("%w: 实例状态已变化", ErrApprovalTargetDrift)
	})

	err := ex.Execute(context.Background(), tk.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrApprovalTargetDrift))
	require.Equal(t, 0, calls, "目标漂移时**绝不能**执行")

	var reloaded model.ApprovalTicket
	require.NoError(t, db.First(&reloaded, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusFailed, reloaded.Status)
	require.NotEmpty(t, reloaded.FailureReason, "必须保留失败原因")
	// 批准事实必须保留（decided_by 不被清空）。
	require.NotNil(t, reloaded.DecidedAt)
}

// TestExecutor_UnregisteredActionFailsClosed 未登记 adapter → 失败关闭。
func TestExecutor_UnregisteredActionFailsClosed(t *testing.T) {
	payload := map[string]any{"instanceId": float64(7)}
	db, _, ex, tk := newExecutorFixture(t, "mcp.unregistered", payload)

	err := ex.Execute(context.Background(), tk.ID)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrApprovalNotRegistered),
		"未登记应失败关闭，得到 %v", err)

	var reloaded model.ApprovalTicket
	require.NoError(t, db.First(&reloaded, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusFailed, reloaded.Status,
		"未登记不得静默成功")
}

// TestExecutor_OnlyOncePerTicket 同一张票只能被执行一次（认领 CAS）。
func TestExecutor_OnlyOncePerTicket(t *testing.T) {
	payload := map[string]any{"instanceId": float64(7)}
	db, svc, ex, tk := newExecutorFixture(t, "mcp.instance_stop", payload)

	calls := 0
	ex.RegisterAdapter("mcp.instance_stop", func(context.Context, *model.ApprovalTicket, map[string]any) error {
		calls++
		return nil
	})

	require.NoError(t, ex.Execute(context.Background(), tk.ID))
	require.Equal(t, 1, calls)

	// 再执行一次：票据已 succeeded，应被拒且不再调用 adapter。
	err := ex.Execute(context.Background(), tk.ID)
	require.Error(t, err)
	require.Equal(t, 1, calls, "同一张票不得重复执行")

	// 直接打 ClaimForExecution 也必须被拒（CAS 生效，与状态名无关）。
	_, claimErr := svc.ClaimForExecution(context.Background(), tk.ID)
	require.Error(t, claimErr)
	_ = db
}

// TestExecutor_AdapterErrorMarksFailed adapter 报错 → failed 且保留原因。
func TestExecutor_AdapterErrorMarksFailed(t *testing.T) {
	payload := map[string]any{"instanceId": float64(7)}
	db, _, ex, tk := newExecutorFixture(t, "mcp.instance_stop", payload)

	ex.RegisterAdapter("mcp.instance_stop", func(context.Context, *model.ApprovalTicket, map[string]any) error {
		return errors.New("节点未连接")
	})

	err := ex.Execute(context.Background(), tk.ID)
	require.Error(t, err)

	var reloaded model.ApprovalTicket
	require.NoError(t, db.First(&reloaded, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusFailed, reloaded.Status)
	require.Contains(t, reloaded.FailureReason, "节点未连接", "失败原因必须如实保留")

	var rec model.ApprovalReceipt
	require.NoError(t, db.Where("ticket_id = ?", tk.ID).First(&rec).Error)
	require.Equal(t, model.ApprovalReceiptFailed, rec.Outcome)
}

// TestExecutor_TimeoutMarksUnverified 超时标 unverified（可能已产生副作用，不自动重试）。
func TestExecutor_TimeoutMarksUnverified(t *testing.T) {
	payload := map[string]any{"instanceId": float64(7)}
	db, _, ex, tk := newExecutorFixture(t, "mcp.instance_stop", payload)

	ex.RegisterAdapter("mcp.instance_stop", func(context.Context, *model.ApprovalTicket, map[string]any) error {
		return context.DeadlineExceeded
	})

	_ = ex.Execute(context.Background(), tk.ID)

	var rec model.ApprovalReceipt
	require.NoError(t, db.Where("ticket_id = ?", tk.ID).First(&rec).Error)
	require.Equal(t, model.ApprovalReceiptUnverified, rec.Outcome,
		"超时应标 unverified（可能已产生副作用），不是 failed")
	require.Equal(t, "TOOL_TIMEOUT", rec.ErrorCode)
}

// TestClaimForExecution_ConcurrentOnlyOneWins 并发认领只有一方成功（真正的 CAS 语义）。
//
// ## 为什么必须单独写这条
//
// 上面的 `TestExecutor_OnlyOncePerTicket` 是**串行**执行两次：第二次因票据状态已从
// `executing` 变为 `succeeded` 而失败。那只证明了「状态机不会被重复推进」，
// **完全没有测到认领用的那条 CAS 条件**——即使 CAS 完全失效，那条用例照样绿。
//
// 认领的真实条件里曾缺 `consumed_at IS NULL`：`Updates` 每次写入不同的 `now`，
// 数据库把这行算作「有变化」→ `RowsAffected` 恒为 1 → 多个执行器都能认领成功，
// 各写一条 in_flight 回执、各执行一次 adapter（副作用发生两次）。
// 本用例用并发认领把这个缺口钉住。
func TestClaimForExecution_ConcurrentOnlyOneWins(t *testing.T) {
	payload := map[string]any{"instanceId": float64(7)}
	_, svc, _, tk := newExecutorFixture(t, "mcp.instance_stop", payload)

	// 票据必须先进入 executing（认领的前置状态）。
	require.NoError(t, svc.db.Model(&model.ApprovalTicket{}).Where("id = ?", tk.ID).
		Update("status", model.ApprovalStatusExecuting).Error)

	const racers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winners []uint
	var losers int

	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 同时起跑，最大化碰撞概率
			receiptID, err := svc.ClaimForExecution(context.Background(), tk.ID)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				losers++
				return
			}
			winners = append(winners, receiptID)
		}()
	}
	close(start)
	wg.Wait()

	require.Len(t, winners, 1,
		"%d 个并发认领者中应只有 1 个成功（CAS 生效）；实际成功 %d 个——多于 1 说明"+
			"执行会重复发生、副作用无法保证一次", racers, len(winners))
	require.Equal(t, racers-1, losers)

	// 回执也必须只有一条：多写一条就等于「执行了两次」的痕迹。
	var receipts int64
	require.NoError(t, svc.db.Model(&model.ApprovalReceipt{}).
		Where("ticket_id = ?", tk.ID).Count(&receipts).Error)
	require.EqualValues(t, 1, receipts, "并发认领只应产生一条回执")
}

// TestClaimForExecution_RejectsAlreadyConsumed 已消费的票据不得再被认领。
//
// 这是上一条的单线程对照：不依赖并发调度，任何环境都能稳定复现。
func TestClaimForExecution_RejectsAlreadyConsumed(t *testing.T) {
	payload := map[string]any{"instanceId": float64(7)}
	_, svc, _, tk := newExecutorFixture(t, "mcp.instance_stop", payload)

	require.NoError(t, svc.db.Model(&model.ApprovalTicket{}).Where("id = ?", tk.ID).
		Update("status", model.ApprovalStatusExecuting).Error)

	_, err := svc.ClaimForExecution(context.Background(), tk.ID)
	require.NoError(t, err, "首次认领应成功")

	// 第二次：状态仍是 executing，只有 consumed_at 非空能挡住它。
	// 这正是缺 `consumed_at IS NULL` 时会漏掉的那条路径。
	_, err = svc.ClaimForExecution(context.Background(), tk.ID)
	require.ErrorIs(t, err, ErrApprovalNotPending,
		"同一张票在 executing 状态下被第二次认领必须失败——否则执行会发生两次")
}
