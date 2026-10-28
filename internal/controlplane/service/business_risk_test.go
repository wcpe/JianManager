package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// buildScopeFixture 构造三层分组树：cluster → region → zone，并在各层挂实例。
//
// 返回 (集群根, 大区A, 大区B, 小区A1, 小区A2, 小区B1)。
func buildScopeFixture(t *testing.T) (*BusinessTargetResolver, uint, uint, uint, uint, uint, uint) {
	t.Helper()
	db := newBatchTestDB(t)
	cluster := mkGroup(t, db, "cluster-main", nil)
	regionA := mkGroup(t, db, "region-a", &cluster.ID)
	regionB := mkGroup(t, db, "region-b", &cluster.ID)
	zoneA1 := mkGroup(t, db, "zone-a1", &regionA.ID)
	zoneA2 := mkGroup(t, db, "zone-a2", &regionA.ID)
	zoneB1 := mkGroup(t, db, "zone-b1", &regionB.ID)
	return NewBusinessTargetResolver(db), cluster.ID, regionA.ID, regionB.ID, zoneA1.ID, zoneA2.ID, zoneB1.ID
}

// TestClassifyScope_CrossRegion 跨两个大区 → RegionCount=2。
func TestClassifyScope_CrossRegion(t *testing.T) {
	r, _, _, _, zoneA1, _, zoneB1 := buildScopeFixture(t)
	db := r.db

	i1 := mkBatchInstance(t, db, "a1", 1, model.InstanceStatusRunning)
	i2 := mkBatchInstance(t, db, "b1", 1, model.InstanceStatusRunning)
	linkGroup(t, db, zoneA1, i1.ID)
	linkGroup(t, db, zoneB1, i2.ID)

	target := &BusinessResolvedTarget{
		InstanceIDs: []uint{i1.ID, i2.ID},
		Instances:   []model.Instance{*i1, *i2},
	}
	stats, err := r.ClassifyScope(target, DefaultBusinessScopeLevels)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.ClusterCount, "同一集群")
	assert.Equal(t, 2, stats.RegionCount, "跨 2 个大区")
	assert.Equal(t, 2, stats.ZoneCount)
	assert.Equal(t, 3, stats.MaxDepth, "三层树深应为 3")
}

// TestClassifyScope_SameRegionCrossZone 同大区跨小区 → RegionCount=1, ZoneCount=2。
func TestClassifyScope_SameRegionCrossZone(t *testing.T) {
	r, _, _, _, zoneA1, zoneA2, _ := buildScopeFixture(t)
	db := r.db
	i1 := mkBatchInstance(t, db, "a1", 1, model.InstanceStatusRunning)
	i2 := mkBatchInstance(t, db, "a2", 1, model.InstanceStatusRunning)
	linkGroup(t, db, zoneA1, i1.ID)
	linkGroup(t, db, zoneA2, i2.ID)

	target := &BusinessResolvedTarget{InstanceIDs: []uint{i1.ID, i2.ID}}
	stats, err := r.ClassifyScope(target, DefaultBusinessScopeLevels)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.RegionCount)
	assert.Equal(t, 2, stats.ZoneCount)
}

// TestClassifyScope_ShallowTreeDegrades 两层树时「大区」塌陷到最深可用祖先，不会误报跨大区。
func TestClassifyScope_ShallowTreeDegrades(t *testing.T) {
	db := newBatchTestDB(t)
	// 只有两层：root（集群） → middle（同时充当大区和小区）
	root := mkGroup(t, db, "root", nil)
	m1 := mkGroup(t, db, "m1", &root.ID)
	m2 := mkGroup(t, db, "m2", &root.ID)
	i1 := mkBatchInstance(t, db, "i1", 1, model.InstanceStatusRunning)
	i2 := mkBatchInstance(t, db, "i2", 1, model.InstanceStatusRunning)
	linkGroup(t, db, m1.ID, i1.ID)
	linkGroup(t, db, m2.ID, i2.ID)

	r := NewBusinessTargetResolver(db)
	stats, err := r.ClassifyScope(&BusinessResolvedTarget{InstanceIDs: []uint{i1.ID, i2.ID}}, DefaultBusinessScopeLevels)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.ClusterCount)
	assert.Equal(t, 2, stats.RegionCount, "第 2 层就是最深可用层，两个 m 分属两个「大区」")
	assert.Equal(t, 2, stats.MaxDepth)
}

// TestClassifyScope_Unassigned 未分组实例只计入 Unassigned，不参与跨区判定。
func TestClassifyScope_Unassigned(t *testing.T) {
	db := newBatchTestDB(t)
	i1 := mkBatchInstance(t, db, "orphan", 1, model.InstanceStatusRunning)
	r := NewBusinessTargetResolver(db)
	stats, err := r.ClassifyScope(&BusinessResolvedTarget{InstanceIDs: []uint{i1.ID}}, DefaultBusinessScopeLevels)
	require.NoError(t, err)
	assert.Equal(t, 1, stats.Unassigned)
	assert.Equal(t, 0, stats.RegionCount)
}

// TestAncestorAt_DegradesToDeepest 祖先链不足目标层级时降级取链尾。
func TestAncestorAt_DegradesToDeepest(t *testing.T) {
	chain := []uint{10, 20}
	assert.Equal(t, uint(10), ancestorAt(chain, 1))
	assert.Equal(t, uint(20), ancestorAt(chain, 2))
	assert.Equal(t, uint(20), ancestorAt(chain, 3), "层级不足应降级取链尾")
	assert.Equal(t, uint(0), ancestorAt(nil, 1))
	assert.Equal(t, uint(10), ancestorAt(chain, 0), "非法层级按第 1 层处理")
}

// TestBusinessScopeLevels_Normalize 非法层级回退默认值。
func TestBusinessScopeLevels_Normalize(t *testing.T) {
	got := BusinessScopeLevels{Cluster: 0, Region: -1, Zone: 0}.Normalize()
	assert.Equal(t, DefaultBusinessScopeLevels, got)
}

// TestAssessRisk_SingleInstance 单实例 → low，无需确认。
func TestAssessRisk_SingleInstance(t *testing.T) {
	target := &BusinessResolvedTarget{
		InstanceIDs: []uint{1},
		Instances:   []model.Instance{{ID: 1, Status: model.InstanceStatusRunning}},
	}
	risk := AssessBusinessRisk(BusinessRiskInput{
		Target:     target,
		Scope:      BusinessScopeStats{ClusterCount: 1, RegionCount: 1, ZoneCount: 1},
		ActionRisk: BusinessActionRiskWrite,
	})
	assert.Equal(t, BusinessRiskLow, risk.Level)
	assert.False(t, risk.RequiresConfirmation)
	assert.False(t, risk.RequiresReason)
	assert.Equal(t, 1, risk.Online)
	assert.Equal(t, 0, risk.Offline)
	assert.Contains(t, risk.Reasons, "影响 1 个实例")
}

// TestAssessRisk_SameZoneThreshold 同小区 ≤10 → low；>10 → medium。
func TestAssessRisk_SameZoneThreshold(t *testing.T) {
	sameZone := BusinessScopeStats{ClusterCount: 1, RegionCount: 1, ZoneCount: 1}
	mk := func(n int) *BusinessResolvedTarget {
		ids := make([]uint, n)
		insts := make([]model.Instance, n)
		for i := 0; i < n; i++ {
			ids[i] = uint(i + 1)
			insts[i] = model.Instance{ID: uint(i + 1), Status: model.InstanceStatusRunning}
		}
		return &BusinessResolvedTarget{InstanceIDs: ids, Instances: insts}
	}
	risk10 := AssessBusinessRisk(BusinessRiskInput{Target: mk(10), Scope: sameZone, ActionRisk: BusinessActionRiskWrite})
	assert.Equal(t, BusinessRiskLow, risk10.Level, "同小区 10 台仍是 low")

	risk11 := AssessBusinessRisk(BusinessRiskInput{Target: mk(11), Scope: sameZone, ActionRisk: BusinessActionRiskWrite})
	assert.Equal(t, BusinessRiskMedium, risk11.Level, "同小区 11 台升 medium")
	assert.False(t, risk11.RequiresConfirmation)
}

// TestAssessRisk_CrossRegionHigh 跨大区 → high，强制确认。
func TestAssessRisk_CrossRegionHigh(t *testing.T) {
	target := &BusinessResolvedTarget{
		InstanceIDs: []uint{1, 2},
		Instances: []model.Instance{
			{ID: 1, Status: model.InstanceStatusRunning},
			{ID: 2, Status: model.InstanceStatusRunning},
		},
	}
	risk := AssessBusinessRisk(BusinessRiskInput{
		Target:     target,
		Scope:      BusinessScopeStats{ClusterCount: 1, RegionCount: 2, ZoneCount: 2},
		ActionRisk: BusinessActionRiskWrite,
	})
	assert.Equal(t, BusinessRiskHigh, risk.Level)
	assert.True(t, risk.RequiresConfirmation)
	assert.False(t, risk.RequiresReason, "high 不要求 reason，只有 critical 要求")
	assert.Contains(t, risk.Reasons, "跨 2 个大区")
}

// TestAssessRisk_CrossClusterHigh 跨集群 → high，且描述用「集群」而非「大区」。
func TestAssessRisk_CrossClusterHigh(t *testing.T) {
	target := &BusinessResolvedTarget{InstanceIDs: []uint{1}, Instances: []model.Instance{{ID: 1, Status: model.InstanceStatusRunning}}}
	risk := AssessBusinessRisk(BusinessRiskInput{
		Target:     target,
		Scope:      BusinessScopeStats{ClusterCount: 2, RegionCount: 3, ZoneCount: 4},
		ActionRisk: BusinessActionRiskWrite,
	})
	assert.Equal(t, BusinessRiskHigh, risk.Level)
	assert.Contains(t, risk.Reasons, "跨 2 个集群")
	for _, r := range risk.Reasons {
		assert.NotContains(t, r, "跨 3 个大区", "已按更粗的集群维度描述，不应重复报大区")
	}
}

// TestAssessRisk_CrossZoneMedium 同大区跨小区 → medium（不是 high）。
func TestAssessRisk_CrossZoneMedium(t *testing.T) {
	target := &BusinessResolvedTarget{
		InstanceIDs: []uint{1, 2},
		Instances: []model.Instance{
			{ID: 1, Status: model.InstanceStatusRunning},
			{ID: 2, Status: model.InstanceStatusRunning},
		},
	}
	risk := AssessBusinessRisk(BusinessRiskInput{
		Target:     target,
		Scope:      BusinessScopeStats{ClusterCount: 1, RegionCount: 1, ZoneCount: 2},
		ActionRisk: BusinessActionRiskWrite,
	})
	assert.Equal(t, BusinessRiskMedium, risk.Level)
	assert.False(t, risk.RequiresConfirmation)
	assert.Contains(t, risk.Reasons, "跨 2 个小区（同大区）")
}

// TestAssessRisk_InstanceCountOver50 实例数 >50 直接 high（即使都在同一小区）。
func TestAssessRisk_InstanceCountOver50(t *testing.T) {
	const n = 51
	ids := make([]uint, n)
	insts := make([]model.Instance, n)
	for i := 0; i < n; i++ {
		ids[i] = uint(i + 1)
		insts[i] = model.Instance{ID: uint(i + 1), Status: model.InstanceStatusRunning}
	}
	risk := AssessBusinessRisk(BusinessRiskInput{
		Target:     &BusinessResolvedTarget{InstanceIDs: ids, Instances: insts},
		Scope:      BusinessScopeStats{ClusterCount: 1, RegionCount: 1, ZoneCount: 1},
		ActionRisk: BusinessActionRiskWrite,
	})
	assert.Equal(t, BusinessRiskHigh, risk.Level)
	assert.True(t, risk.RequiresConfirmation)
}

// TestAssessRisk_OfflineMedium 含离线实例 → 至少 medium。
func TestAssessRisk_OfflineMedium(t *testing.T) {
	target := &BusinessResolvedTarget{
		InstanceIDs: []uint{1, 2},
		Instances: []model.Instance{
			{ID: 1, Status: model.InstanceStatusRunning},
			{ID: 2, Status: model.InstanceStatusStopped},
		},
	}
	risk := AssessBusinessRisk(BusinessRiskInput{
		Target:     target,
		Scope:      BusinessScopeStats{ClusterCount: 1, RegionCount: 1, ZoneCount: 1},
		ActionRisk: BusinessActionRiskRead,
	})
	assert.Equal(t, BusinessRiskMedium, risk.Level)
	assert.Equal(t, 1, risk.Offline)
	assert.Contains(t, risk.Reasons, "含 1 个离线实例")
}

// TestAssessRisk_DestructiveCritical DESTRUCTIVE → critical + 必填 reason（且压过一切）。
func TestAssessRisk_DestructiveCritical(t *testing.T) {
	target := &BusinessResolvedTarget{InstanceIDs: []uint{1}, Instances: []model.Instance{{ID: 1, Status: model.InstanceStatusRunning}}}
	risk := AssessBusinessRisk(BusinessRiskInput{
		Target:     target,
		Scope:      BusinessScopeStats{ClusterCount: 1, RegionCount: 1, ZoneCount: 1},
		ActionRisk: BusinessActionRiskDestructive,
	})
	assert.Equal(t, BusinessRiskCritical, risk.Level, "critical 应压过 low")
	assert.True(t, risk.RequiresConfirmation)
	assert.True(t, risk.RequiresReason)
	assert.Contains(t, risk.Reasons, "动作风险为 DESTRUCTIVE（不可逆）")
}

// TestAssessRisk_TakesHighest 多条件命中取最高（跨大区 high + 离线 medium → high）。
func TestAssessRisk_TakesHighest(t *testing.T) {
	target := &BusinessResolvedTarget{
		InstanceIDs: []uint{1, 2},
		Instances: []model.Instance{
			{ID: 1, Status: model.InstanceStatusStopped}, // 离线
			{ID: 2, Status: model.InstanceStatusRunning},
		},
	}
	risk := AssessBusinessRisk(BusinessRiskInput{
		Target:     target,
		Scope:      BusinessScopeStats{ClusterCount: 1, RegionCount: 2, ZoneCount: 3},
		ActionRisk: BusinessActionRiskWrite,
	})
	assert.Equal(t, BusinessRiskHigh, risk.Level, "跨大区 high 应压过离线的 medium")
}

// TestNormalizeActionRisk 未声明/非法风险值一律按 WRITE（宁严勿宽）。
func TestNormalizeActionRisk(t *testing.T) {
	assert.Equal(t, BusinessActionRiskRead, NormalizeActionRisk("READ"))
	assert.Equal(t, BusinessActionRiskDestructive, NormalizeActionRisk("DESTRUCTIVE"))
	assert.Equal(t, BusinessActionRiskWrite, NormalizeActionRisk(""))
	assert.Equal(t, BusinessActionRiskWrite, NormalizeActionRisk("unknown"))
	assert.Equal(t, BusinessActionRiskWrite, NormalizeActionRisk("write"), "大小写不匹配也降为 WRITE")
}

// TestBusinessConfirmToken_RoundTrip 正常签发与校验。
func TestBusinessConfirmToken_RoundTrip(t *testing.T) {
	signer := newTestSigner(t)
	target := BusinessTarget{GroupIDs: []uint{3, 7}, IncludeDescendants: true}
	hash := BusinessTargetHash([]uint{1, 2, 5})

	token, expiresAt, err := signer.Issue("user:1", target, hash, BusinessRiskHigh)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	assert.True(t, expiresAt.After(time.Now()))

	claims, err := signer.Verify(token, BusinessConfirmExpectation{IssuerKey: "user:1", InstancesHash: hash})
	require.NoError(t, err)
	assert.Equal(t, "user:1", claims.IssuerKey)
	assert.Equal(t, BusinessRiskHigh, claims.RiskLevel)
	assert.Equal(t, []uint{3, 7}, claims.Target.GroupIDs)
	assert.True(t, claims.Target.IncludeDescendants)
}

// TestBusinessConfirmToken_Expired 过期令牌返回 ErrBusinessTokenExpired。
func TestBusinessConfirmToken_Expired(t *testing.T) {
	signer := newTestSigner(t)
	signer.SetTTL(time.Minute)
	base := time.Now()
	signer.SetClock(func() time.Time { return base })

	hash := BusinessTargetHash([]uint{1})
	token, _, err := signer.Issue("user:1", BusinessTarget{InstanceIDs: []uint{1}}, hash, BusinessRiskHigh)
	require.NoError(t, err)

	// 前进 2 分钟 → 已过期
	signer.SetClock(func() time.Time { return base.Add(2 * time.Minute) })
	_, err = signer.Verify(token, BusinessConfirmExpectation{IssuerKey: "user:1", InstancesHash: hash})
	require.ErrorIs(t, err, ErrBusinessTokenExpired)
}

// TestBusinessConfirmToken_TargetChanged 目标 hash 不一致返回 ErrBusinessTargetChanged。
func TestBusinessConfirmToken_TargetChanged(t *testing.T) {
	signer := newTestSigner(t)
	token, _, err := signer.Issue("user:1", BusinessTarget{GroupIDs: []uint{1}},
		BusinessTargetHash([]uint{1, 2}), BusinessRiskHigh)
	require.NoError(t, err)

	_, err = signer.Verify(token, BusinessConfirmExpectation{
		IssuerKey:     "user:1",
		InstancesHash: BusinessTargetHash([]uint{1, 2, 3}), // 分组新增了一个实例
	})
	require.ErrorIs(t, err, ErrBusinessTargetChanged)
}

// TestBusinessConfirmToken_CrossAccount 他人令牌返回 ErrBusinessTokenMismatch。
func TestBusinessConfirmToken_CrossAccount(t *testing.T) {
	signer := newTestSigner(t)
	hash := BusinessTargetHash([]uint{1})
	token, _, err := signer.Issue("user:1", BusinessTarget{InstanceIDs: []uint{1}}, hash, BusinessRiskHigh)
	require.NoError(t, err)

	_, err = signer.Verify(token, BusinessConfirmExpectation{IssuerKey: "user:2", InstancesHash: hash})
	require.ErrorIs(t, err, ErrBusinessTokenMismatch)
}

// TestBusinessConfirmToken_Tampered 篡改正文后签名失配。
func TestBusinessConfirmToken_Tampered(t *testing.T) {
	signer := newTestSigner(t)
	hash := BusinessTargetHash([]uint{1})
	token, _, err := signer.Issue("user:1", BusinessTarget{InstanceIDs: []uint{1}}, hash, BusinessRiskHigh)
	require.NoError(t, err)

	// 翻转最后一个字节
	tampered := []byte(token)
	tampered[len(tampered)-1] ^= 0xFF
	_, err = signer.Verify(string(tampered), BusinessConfirmExpectation{IssuerKey: "user:1", InstancesHash: hash})
	require.ErrorIs(t, err, ErrBusinessTokenInvalid)

	// 格式垃圾
	_, err = signer.Verify("bpt_garbage", BusinessConfirmExpectation{})
	require.ErrorIs(t, err, ErrBusinessTokenInvalid)
	_, err = signer.Verify("", BusinessConfirmExpectation{})
	require.ErrorIs(t, err, ErrBusinessTokenInvalid)
}

// TestBusinessConfirmToken_VerifyOrder 归属先于过期判定：他人过期令牌仍报 MISMATCH。
func TestBusinessConfirmToken_VerifyOrder(t *testing.T) {
	signer := newTestSigner(t)
	signer.SetTTL(time.Second)
	base := time.Now()
	signer.SetClock(func() time.Time { return base })
	hash := BusinessTargetHash([]uint{1})
	token, _, err := signer.Issue("user:1", BusinessTarget{InstanceIDs: []uint{1}}, hash, BusinessRiskHigh)
	require.NoError(t, err)

	signer.SetClock(func() time.Time { return base.Add(time.Hour) })
	_, err = signer.Verify(token, BusinessConfirmExpectation{IssuerKey: "user:2", InstancesHash: hash})
	require.ErrorIs(t, err, ErrBusinessTokenMismatch, "跨账号判定应优先于过期判定")
}

// TestNewBusinessConfirmTokenSigner_EmptySecret 空密钥拒绝签发。
func TestNewBusinessConfirmTokenSigner_EmptySecret(t *testing.T) {
	_, err := NewBusinessConfirmTokenSigner(nil)
	require.Error(t, err)
	_, err = NewBusinessConfirmTokenSigner([]byte{})
	require.Error(t, err)
}

// TestDeriveBusinessConfirmTokenSecret 域分离派生：非空、确定、且与主密钥不同。
func TestDeriveBusinessConfirmTokenSecret(t *testing.T) {
	master := []byte("master-secret")
	got := DeriveBusinessConfirmTokenSecret(master)
	require.NotEmpty(t, got)
	assert.NotEqual(t, master, got, "派生密钥不应等于主密钥")
	assert.Equal(t, got, DeriveBusinessConfirmTokenSecret(master), "同样输入必须得到同样密钥")
	assert.NotEqual(t, got, DeriveBotLoadPlanTokenSecret(master), "不同用途必须派生出不同密钥")
	assert.Nil(t, DeriveBusinessConfirmTokenSecret(nil))
}

// newTestSigner 构造测试签名器。
func newTestSigner(t *testing.T) *BusinessConfirmTokenSigner {
	t.Helper()
	signer, err := NewBusinessConfirmTokenSigner(DeriveBusinessConfirmTokenSecret([]byte("test-master")))
	require.NoError(t, err)
	return signer
}

// ── 动作风险的信任链（FR-532~537 安全修复）──────────────────────────
//
// ## 守的是什么
//
// 执行期的动作风险原先把**客户端字段** `req.ActionRisk` 当唯一依据，
// 而确认令牌里签发的 `claims.RiskLevel` 从未被比对。两条绕过路径：
//
//  1. 预览时用 DESTRUCTIVE 拿令牌 → 执行时改传 READ
//     → `RequiresReason` 变 false，绕过「critical 必须填 reason」；
//  2. 同一份客户端输入还决定 `readOnly` → 从 DispatchWrite 降级为 Dispatch
//     → operator/operatorId/taskId 不再注入，下游审计与对账断链。
//
// 修复是「请求体与令牌取高」。下面的用例分别钉住这两条路径与「取高」本身的语义。

// TestBusinessActionRiskRank_Ordering 三档动作风险的序必须严格递增。
//
// 这是「取高」正确性的前提：序错了，max 会静默选错。
func TestBusinessActionRiskRank_Ordering(t *testing.T) {
	read := businessActionRiskRank(BusinessActionRiskRead)
	write := businessActionRiskRank(BusinessActionRiskWrite)
	destructive := businessActionRiskRank(BusinessActionRiskDestructive)

	assert.Less(t, read, write, "READ 必须低于 WRITE")
	assert.Less(t, write, destructive, "WRITE 必须低于 DESTRUCTIVE")
}

// TestBusinessActionRiskRank_UnknownFallsBackToWrite 未知/空值按 WRITE 处理（宁严勿宽）。
func TestBusinessActionRiskRank_UnknownFallsBackToWrite(t *testing.T) {
	writeRank := businessActionRiskRank(BusinessActionRiskWrite)
	for _, raw := range []string{"", "read", "READONLY", "bogus", "  "} {
		assert.Equal(t, writeRank, businessActionRiskRank(raw),
			"%q 应按 WRITE 处理——与 NormalizeActionRisk「宁严勿宽」保持一致", raw)
	}
}

// TestResolveEffectiveActionRisk_ClientCannotDowngrade 客户端把 DESTRUCTIVE 降级成 READ 无效。
//
// **直接调用生产函数**（`resolveEffectiveActionRisk`）而不是在用例里重写一遍取高逻辑——
// 后者会变成空转断言：回退修复后用例照样绿，等于什么都没守。
// 本用例第一版就是这么写的，反向验证时发现拦不住回退，才改成现在这样。
func TestResolveEffectiveActionRisk_ClientCannotDowngrade(t *testing.T) {
	claims := &BusinessConfirmClaims{RiskLevel: BusinessActionRiskDestructive}

	got := resolveEffectiveActionRisk(BusinessActionRiskRead, claims)
	assert.Equal(t, BusinessActionRiskDestructive, got,
		"令牌里是 DESTRUCTIVE 时，客户端改传 READ 不得把生效风险降下来")
}

// TestResolveEffectiveActionRisk_CriticalStillRequiresReason 降级尝试仍必须填 reason。
//
// 端到端串起来：合并后的生效风险喂给 AssessBusinessRisk，必须仍是 critical + 要 reason。
func TestResolveEffectiveActionRisk_CriticalStillRequiresReason(t *testing.T) {
	target := &BusinessResolvedTarget{
		InstanceIDs: []uint{1},
		Instances:   []model.Instance{{ID: 1, Status: model.InstanceStatusRunning}},
	}
	scope := BusinessScopeStats{ClusterCount: 1, RegionCount: 1, ZoneCount: 1}

	effective := resolveEffectiveActionRisk(BusinessActionRiskRead,
		&BusinessConfirmClaims{RiskLevel: BusinessActionRiskDestructive})
	risk := AssessBusinessRisk(BusinessRiskInput{Target: target, Scope: scope, ActionRisk: effective})

	assert.Equal(t, BusinessRiskCritical, risk.Level)
	assert.True(t, risk.RequiresReason,
		"critical 必须填 reason —— 这正是「降级成 READ」想绕过的那道闸")

	// 对照：若只看客户端字段（修复前的行为），会得到 low 且不要 reason。
	// 这条对照证明本用例守的是一个**真实存在的行为差异**，不是同义反复。
	naive := AssessBusinessRisk(BusinessRiskInput{
		Target: target, Scope: scope, ActionRisk: BusinessActionRiskRead,
	})
	assert.Equal(t, BusinessRiskLow, naive.Level)
	assert.False(t, naive.RequiresReason)
}

// TestResolveEffectiveActionRisk_TokenCannotMaskClient 令牌里的低风险不得掩盖执行期更高风险。
func TestResolveEffectiveActionRisk_TokenCannotMaskClient(t *testing.T) {
	got := resolveEffectiveActionRisk(BusinessActionRiskWrite,
		&BusinessConfirmClaims{RiskLevel: BusinessActionRiskRead})
	assert.Equal(t, BusinessActionRiskWrite, got,
		"令牌是 READ、请求是 WRITE 时应取 WRITE：令牌只覆盖签发那一刻，表达不了执行期新增的风险")
}

// TestResolveEffectiveActionRisk_NilClaimsFallsBackToClient 无令牌时按客户端。
func TestResolveEffectiveActionRisk_NilClaimsFallsBackToClient(t *testing.T) {
	assert.Equal(t, BusinessActionRiskWrite, resolveEffectiveActionRisk(BusinessActionRiskWrite, nil))
	assert.Equal(t, BusinessActionRiskRead, resolveEffectiveActionRisk(BusinessActionRiskRead, nil))
	// 空 RiskLevel 视同未签发（历史令牌/字段缺失），同样回落到客户端。
	assert.Equal(t, BusinessActionRiskWrite,
		resolveEffectiveActionRisk(BusinessActionRiskWrite, &BusinessConfirmClaims{}))
}

// TestResolveEffectiveActionRisk_ReadOnlyOnlyWhenBothRead readOnly 只在两个来源都是 READ 时成立。
//
// 直接对应绕过路径 ②：readOnly 决定走 Dispatch 还是 DispatchWrite。
// 只要任一来源为写，就必须走写路径（注入 operator/taskId 并留审计）。
func TestResolveEffectiveActionRisk_ReadOnlyOnlyWhenBothRead(t *testing.T) {
	cases := []struct {
		name     string
		token    string
		client   string
		wantRisk string
	}{
		{"双 READ 才是只读", BusinessActionRiskRead, BusinessActionRiskRead, BusinessActionRiskRead},
		{"令牌写、客户端谎报读", BusinessActionRiskWrite, BusinessActionRiskRead, BusinessActionRiskWrite},
		{"令牌 DESTRUCTIVE、客户端谎报读", BusinessActionRiskDestructive, BusinessActionRiskRead, BusinessActionRiskDestructive},
		{"令牌读、客户端写", BusinessActionRiskRead, BusinessActionRiskWrite, BusinessActionRiskWrite},
		{"令牌空 → 按客户端", "", BusinessActionRiskWrite, BusinessActionRiskWrite},
		{"都空 → 兜底 WRITE", "", "", BusinessActionRiskWrite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			claims := &BusinessConfirmClaims{RiskLevel: c.token}
			effective := resolveEffectiveActionRisk(c.client, claims)
			norm := NormalizeActionRisk(effective)
			assert.Equal(t, c.wantRisk, norm, "生效动作风险不符")
			assert.Equal(t, c.wantRisk == BusinessActionRiskRead, norm == BusinessActionRiskRead,
				"readOnly 必须由生效风险决定，不能被客户端字段单独决定")
		})
	}
}
