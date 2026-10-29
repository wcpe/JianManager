package service

/* *
 *审批票据内核的守门用例。
 *
 * 这组断言守的是**几条「看起来对但会静默破防」的规则**：
 *  1. Agent 身份永久不可批准——判据必须是身份，不是权限节点；
 *  2. 幂等靠数据库约束（先查再插在并发下双双插入）；
 *  3. 只有 pending 可决策，且 CAS 只让一个人赢；
 *  4. 过期要**回写状态**，否则列表「看着待审但点不动」；
 *  5. 越权与不存在共用 not found（不给 id 探测接口）。
 */

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// newApprovalTestDB 建审批两表 + 幂等部分唯一索引。
//
// 索引手工建：真库由 database 包在 AutoMigrate 之后建，但那是另一个包且未导出。
// 若不建，「幂等是否真的由数据库保证」就无从断言——这正是本组用例的价值。
func newApprovalTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newAssistantChatTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.ApprovalTicket{}, &model.ApprovalReceipt{}))
	require.NoError(t, db.Exec(
		"CREATE UNIQUE INDEX IF NOT EXISTS ux_approval_tickets_pending ON approval_tickets "+
			"(applicant_user_id, action, target_type, target_id, payload_hash) WHERE status = 'pending'").Error)
	return db
}

// submitOne 提交一张样例票据。
func submitOne(t *testing.T, svc *ApprovalService, owner uint, action string, payload map[string]any) *model.ApprovalTicket {
	t.Helper()
	tk, created, err := svc.SubmitApproval(SubmitApprovalParams{
		ApplicantUserID: owner,
		ApplicantKind:   model.ApprovalApplicantAgent,
		TokenID:         "tok-1",
		Action:          action,
		TargetType:      "instance",
		TargetID:        "7",
		Payload:         payload,
		Evidence:        map[string]any{"status": "RUNNING", "version": 3},
		Reason:          "需要重启以应用配置",
	})
	require.NoError(t, err)
	require.True(t, created)
	return tk
}

// TestApproval_AgentCanNeverDecide Agent 身份无论权限如何都不可批准。
//
// 这是本 FR 最重要的一条：如果把「谁能批」实现成「有 approval.decide 节点就能批」，
// 那么一把拥有该节点的 Agent Token 就能自己提、自己批，审批环节彻底失效。
func TestApproval_AgentCanNeverDecide(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	tk := submitOne(t, svc, 1, "mcp.instance_stop", map[string]any{"instanceId": float64(7)})

	_, err := svc.Decide(context.Background(), DecideApprovalParams{
		TicketID: tk.ID, ActorUserID: 1, ActorIsAgent: true, Approve: true,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrApprovalAgentCannotDecide),
		"Agent 决策必须返回 ErrApprovalAgentCannotDecide，得到 %v", err)

	// 反向校验：票据仍是 pending（没有被偷偷改成 executing）。
	var reloaded model.ApprovalTicket
	require.NoError(t, db.First(&reloaded, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusPending, reloaded.Status)

	// 人类可以批准同一张票。
	ok, err := svc.Decide(context.Background(), DecideApprovalParams{
		TicketID: tk.ID, ActorUserID: 99, Approve: true,
	})
	require.NoError(t, err)
	require.Equal(t, model.ApprovalStatusExecuting, ok.Status)
}

// TestApproval_PendingIsIdempotent 同条件 pending 期间重复提交返回同一张票。
func TestApproval_PendingIsIdempotent(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	tk := submitOne(t, svc, 1, "mcp.instance_stop", map[string]any{"instanceId": float64(7)})

	again, created, err := svc.SubmitApproval(SubmitApprovalParams{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantAgent, TokenID: "tok-1",
		Action: "mcp.instance_stop", TargetType: "instance", TargetID: "7",
		Payload:  map[string]any{"instanceId": float64(7)},
		Evidence: map[string]any{"status": "RUNNING"},
		Reason:   "重试同一件事",
	})
	require.NoError(t, err)
	require.False(t, created, "重复提交不应新建票据")
	require.Equal(t, tk.ID, again.ID, "应返回原票据")

	var count int64
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Count(&count).Error)
	require.Equal(t, int64(1), count, "库内只应有一张票")

	// 反向校验：参数不同则必须新建（幂等键含 payload_hash）。
	other, created2, err := svc.SubmitApproval(SubmitApprovalParams{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantAgent,
		Action: "mcp.instance_stop", TargetType: "instance", TargetID: "7",
		Payload: map[string]any{"instanceId": float64(7), "force": true},
		Reason:  "换了参数",
	})
	require.NoError(t, err)
	require.True(t, created2, "参数不同应新建票据")
	require.NotEqual(t, tk.ID, other.ID)
}

// TestApproval_OnlyPendingCanBeDecided 非 pending 一律拒绝决策。
func TestApproval_OnlyPendingCanBeDecided(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	tk := submitOne(t, svc, 1, "mcp.instance_stop", map[string]any{"instanceId": float64(7)})

	// 申请人撤回
	_, err := svc.Withdraw(tk.ID, 1)
	require.NoError(t, err)

	// 再撤回一次 → 拒绝
	_, err = svc.Withdraw(tk.ID, 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrApprovalNotPending))

	// 批准已撤回的 → 拒绝
	_, err = svc.Decide(context.Background(), DecideApprovalParams{
		TicketID: tk.ID, ActorUserID: 99, Approve: true,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrApprovalNotPending))
}

// TestApproval_WithdrawRequiresOwnership 只有申请人能撤回。
func TestApproval_WithdrawRequiresOwnership(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	tk := submitOne(t, svc, 1, "mcp.instance_stop", map[string]any{"instanceId": float64(7)})

	_, err := svc.Withdraw(tk.ID, 2) // 不是申请人
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrApprovalNotFound),
		"越权应返回 not found（不给 id 探测接口），得到 %v", err)
}

// TestApproval_ExpiredIsWrittenBack 过期必须回写状态，不能只显示。
func TestApproval_ExpiredIsWrittenBack(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	// 把时钟拨到票据过期之后
	svc.now = func() time.Time { return time.Now().Add(25 * time.Hour) }

	tk := submitOne(t, svc, 1, "mcp.instance_stop", map[string]any{"instanceId": float64(7)})
	// 注意：SubmitApproval 用 now() 算 expiresAt，故这张票一出生就"已过期"。
	// 为让断言针对「读取时回写」，这里把 expires_at 显式改到过去。
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Where("id = ?", tk.ID).
		Update("expires_at", time.Now().Add(-time.Hour)).Error)

	list, err := svc.ListApprovals(ApprovalListFilter{ApplicantUserID: ptrUint(1)})
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, model.ApprovalStatusExpired, list[0].Status, "读取时状态应为 expired")

	// 关键：库里也必须已回写（否则会出现「列表显示过期但点得动」）。
	var reloaded model.ApprovalTicket
	require.NoError(t, db.First(&reloaded, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusExpired, reloaded.Status, "过期状态必须回写库")
}

// TestApproval_ExpiredCannotBeApproved 过期票不可再批准。
func TestApproval_ExpiredCannotBeApproved(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	tk := submitOne(t, svc, 1, "mcp.instance_stop", map[string]any{"instanceId": float64(7)})
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Where("id = ?", tk.ID).
		Update("expires_at", time.Now().Add(-time.Minute)).Error)

	_, err := svc.Decide(context.Background(), DecideApprovalParams{
		TicketID: tk.ID, ActorUserID: 99, Approve: true,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrApprovalExpired), "应报过期，得到 %v", err)
}

// TestApproval_ConcurrentApproveOnlyOneWins 并发批准只有一个人成功（CAS）。
//
// 断言必须包含「状态只变一次」，不能只看返回码：
// 两道闸（前置 status 检查 + CAS）里只放宽一道时返回码可能不变，
// 但副作用次数会暴露问题（与重复 approve 的教训一致）。
func TestApproval_ConcurrentApproveOnlyOneWins(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	tk := submitOne(t, svc, 1, "mcp.instance_stop", map[string]any{"instanceId": float64(7)})

	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := svc.Decide(context.Background(), DecideApprovalParams{
				TicketID: tk.ID, ActorUserID: 99, Approve: true,
			})
			errs <- err
		}()
	}
	success := 0
	for i := 0; i < n; i++ {
		if err := <-errs; err == nil {
			success++
		}
	}
	require.Equal(t, 1, success, "并发批准只应有一个成功，实际 %d", success)

	var reloaded model.ApprovalTicket
	require.NoError(t, db.First(&reloaded, tk.ID).Error)
	require.Equal(t, model.ApprovalStatusExecuting, reloaded.Status)
	// 只应有一条 decided 记录的时间戳（不被覆盖成多次）。
	require.NotNil(t, reloaded.DecidedAt)
}

// TestApproval_ReasonRequired 理由必填（否则批准人无从判断）。
func TestApproval_ReasonRequired(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	_, _, err := svc.SubmitApproval(SubmitApprovalParams{
		ApplicantUserID: 1, Action: "mcp.instance_stop",
		TargetType: "instance", TargetID: "7",
		Payload: map[string]any{}, Reason: "   ",
	})
	require.Error(t, err, "空理由应被拒收")
}

// TestApproval_PayloadTamperDetected 冻结参数被改后哈希不符。
func TestApproval_PayloadTamperDetected(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	tk := submitOne(t, svc, 1, "mcp.instance_stop", map[string]any{"instanceId": float64(7)})

	// 模拟库内参数被篡改：改 frozen 不改 hash。
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Where("id = ?", tk.ID).
		Update("payload_frozen", `{"instanceId":999}`).Error)

	var reloaded model.ApprovalTicket
	require.NoError(t, db.First(&reloaded, tk.ID).Error)
	var frozen map[string]any
	require.NoError(t, jsonUnmarshalForTest(reloaded.PayloadFrozen, &frozen))
	h, err := hashApprovalPayload(frozen)
	require.NoError(t, err)
	require.NotEqual(t, reloaded.PayloadHash, h, "篡改后哈希必须不符（执行器据此拒绝）")
}

// TestApproval_HashKeyOrderIndependent 键序不影响哈希（同内容必须同哈希）。
func TestApproval_HashKeyOrderIndependent(t *testing.T) {
	a, err := hashApprovalPayload(map[string]any{"b": float64(2), "a": float64(1)})
	require.NoError(t, err)
	b, err := hashApprovalPayload(map[string]any{"a": float64(1), "b": float64(2)})
	require.NoError(t, err)
	require.Equal(t, a, b, "键序不同不该改变哈希，否则幂等与防篡改都会失效")
}

// TestApproval_ExpireStaleSweeps 巡检兜底把超期 pending 置 expired。
func TestApproval_ExpireStaleSweeps(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)
	tk := submitOne(t, svc, 1, "mcp.instance_stop", map[string]any{"instanceId": float64(7)})
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Where("id = ?", tk.ID).
		Update("expires_at", time.Now().Add(-time.Minute)).Error)

	n, err := svc.ExpireStale()
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	// 幂等：再跑一次不再有可扫的。
	n2, err := svc.ExpireStale()
	require.NoError(t, err)
	require.Equal(t, int64(0), n2)
}

func ptrUint(v uint) *uint { return &v }

// jsonUnmarshalForTest 小包装，避免在用例里到处 import encoding/json。
func jsonUnmarshalForTest(raw string, out any) error {
	return json.Unmarshal([]byte(raw), out)
}
