package service

/*
 * 审批票的申请人归属（P0 回归）。
 *
 * 修复前：助手派生的 principal 没有 Token（ADR-105 不为人类用户签发 jmat_），
 * `tokenOwnerUserID(deps, 0)` 直接返回 0，于是所有经助手触发的写操作落下的审批票
 * applicant_user_id 恒为 0。这不只是“显示不好看”，而是两个实质错误：
 *   ① 幂等部分唯一索引含 applicant_user_id → **不同用户**的同 action+同目标+同参数
 *      请求会互相命中同一张票并被回读；
 *   ② own.* 接口按 applicant 过滤 → 真申请人看不到自己的申请，也无法 withdraw。
 */

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/database"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestAssistantPrincipal_CarriesActAsUserID 助手身份必须带“代表哪个用户”。
func TestAssistantPrincipal_CarriesActAsUserID(t *testing.T) {
	// 用全量 schema 的库：LoadUserAccess 要查 user_role_bindings 等 RBAC 表，
	// 而 newApprovalTestDB / newConfigTestDB 都只建了几张业务表。
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/principal.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(db))
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	authz := NewAuthzService(db)

	// 造一个真用户，否则 LoadUserAccess 拿不到任何权限。
	u := &model.User{Username: "alice", Role: model.RoleGroupAdmin}
	require.NoError(t, db.Create(u).Error)

	p, err := AssistantPrincipalForOwner(authz, u.ID)
	require.NoError(t, err)
	if p == nil {
		// 该用户没有可映射权限时返回 nil 是合法结果，但那时也无从建票。
		t.Skip("该用户未派生任何能力")
	}
	require.Equal(t, u.ID, p.ActAsUserID, "principal 必须带上真实用户 id")
	require.Zero(t, p.TokenID, "助手不得伪造 Token（ADR-105）")
}

// TestSubmitApproval_RejectsZeroApplicant 服务层 fail-fast：没有申请人就不得建票。
//
// 纵深防御：即使上游（MCP 拦截层）漏填，也不应该让一张“无人拥有的票”进库——
// 它会连同幂等索引一起静默影响其他用户。
func TestSubmitApproval_RejectsZeroApplicant(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)

	_, _, err := svc.SubmitApproval(SubmitApprovalParams{
		ApplicantUserID:  0, // 关键：无人
		ApplicantKind:    model.ApprovalApplicantAgent,
		RequireApplicant: true, // 助手委托链：必须有真实用户
		Action:           "instance_stop",
		TargetType:       "instance",
		TargetID:         "1",
		Payload:          map[string]any{"id": 1},
		Reason:           "test",
	})
	require.Error(t, err, "applicant=0 必须被拒绝")
	require.Contains(t, err.Error(), "申请人")

	var n int64
	require.NoError(t, db.Model(&model.ApprovalTicket{}).Count(&n).Error)
	require.Equal(t, int64(0), n, "不得留下无主票据")
}

// TestSubmitApproval_DistinctApplicantsDoNotShareTicket 不同申请人不得共用同一张票。
//
// 这条直接盯着“幂等索引里含 applicant_user_id”这个前提：
// 若申请人恒为 0，两次不同用户的同参数请求会被当成重复提交，后者拿到前者的票。
func TestSubmitApproval_DistinctApplicantsDoNotShareTicket(t *testing.T) {
	db := newApprovalTestDB(t)
	svc := NewApprovalService(db)

	params := SubmitApprovalParams{
		ApplicantKind: model.ApprovalApplicantAgent,
		Action:        "instance_stop",
		TargetType:    "instance",
		TargetID:      "1",
		Payload:       map[string]any{"id": 1},
		Reason:        "test",
	}

	p1 := params
	p1.ApplicantUserID = 11
	t1, _, err := svc.SubmitApproval(p1)
	require.NoError(t, err)

	p2 := params
	p2.ApplicantUserID = 22
	t2, _, err := svc.SubmitApproval(p2)
	require.NoError(t, err)

	require.NotEqual(t, t1.ID, t2.ID, "不同申请人必须是两张各自独立的票")

	// 同一申请人重复提交仍然幂等（回读同一张）。
	t1b, _, err := svc.SubmitApproval(p1)
	require.NoError(t, err)
	require.Equal(t, t1.ID, t1b.ID, "同一申请人的重复提交应回读原票")
}
