package service

import (
	"fmt"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 业务批量风险分级（FR-535 §5）。
//
// 分级只回答一件事：**这次批量影响面有多大、是否需要用户再做一次确认**。
// 动作自身的风险（READ / WRITE / DESTRUCTIVE）由插件的 manifest 声明、经业务台透传进来；
// JM **不推断风险**（ADR-106：平台不解析 manifest 内容），未声明时按 WRITE 处理。

// 业务批量风险等级字面量（对应 RiskAssessment.level）。
//
// 注意：这里刻意使用 low/medium/high/critical 四档，而非动作风险的三档
// READ/WRITE/DESTRUCTIVE——两者语义不同：前者描述「这次批量操作的影响面」，
// 后者描述「单个动作是否可逆」。契约文档把 RiskAssessment.level 指向了动作枚举，
// 与实际规格和示例（"level": "high"）不一致，本实现以规格与示例为准。
const (
	BusinessRiskLow      = "low"
	BusinessRiskMedium   = "medium"
	BusinessRiskHigh     = "high"
	BusinessRiskCritical = "critical"
)

// 动作风险枚举（插件 manifest ActionSpec.risk 的三档），与 RiskLevel schema 一致。
const (
	BusinessActionRiskRead        = "READ"
	BusinessActionRiskWrite       = "WRITE"
	BusinessActionRiskDestructive = "DESTRUCTIVE"
)

// businessActionRiskRank 给出**动作风险**（READ/WRITE/DESTRUCTIVE）的可比较序。
//
// 与 `businessRiskRank` 是两套枚举，不能混用：前者描述「这个动作能造成多大后果」
// （来自插件 manifest），后者描述「这一次批量操作的综合风险」（跨区/规模/离线叠加）。
// 混用会让 READ 被当成 low 之类的错误映射。
func businessActionRiskRank(level string) int {
	switch NormalizeActionRisk(level) {
	case BusinessActionRiskDestructive:
		return 3
	case BusinessActionRiskWrite:
		return 2
	case BusinessActionRiskRead:
		return 1
	}
	return 2 // 兜底按 WRITE：与 NormalizeActionRisk「宁严勿宽」一致
}

// resolveEffectiveActionRisk 合并「客户端声明的动作风险」与「确认令牌签发的风险」，取较高者。
//
// ## 为什么必须是「取高」而不是二选一
//
// 只信客户端（修复前的行为）：预览时用 DESTRUCTIVE 拿到令牌、执行时改传 READ，
// 就能让 `RequiresReason` 变 false（绕过「critical 必须填 reason」），
// 并让 readOnly 从 DispatchWrite 降级为 Dispatch（不注入 operator/taskId，审计断链）。
//
// 只信令牌：令牌只覆盖签发那一刻，无法表达执行期**新增**的风险
// （目标集合里多出离线实例、动作被换成同类更高危的）。那会让「预览之后风险变高」
// 这一类真实情况被令牌里过时的低风险掩盖。
//
// 取高，两边都不会被对方的低估掩盖。
func resolveEffectiveActionRisk(clientRisk string, claims *BusinessConfirmClaims) string {
	effective := clientRisk
	if claims == nil || claims.RiskLevel == "" {
		return effective
	}
	if businessActionRiskRank(claims.RiskLevel) > businessActionRiskRank(NormalizeActionRisk(effective)) {
		effective = claims.RiskLevel
	}
	return effective
}

// businessRiskRank 给出等级的可比较序（多条件命中取最高）。
func businessRiskRank(level string) int {
	switch level {
	case BusinessRiskCritical:
		return 4
	case BusinessRiskHigh:
		return 3
	case BusinessRiskMedium:
		return 2
	case BusinessRiskLow:
		return 1
	}
	return 0
}

// maxBusinessRisk 返回两者中更高的一档。
func maxBusinessRisk(a, b string) string {
	if businessRiskRank(b) > businessRiskRank(a) {
		return b
	}
	return a
}

// BusinessScopeLevels 把「分组树层级」映射为业务语义（集群 / 大区 / 小区）。
//
// 【为什么单独抽出来】「跨大区」的判定完全依赖分组树的层级约定，而这个约定是**运营约定**，
// 不是代码真理：有的部署只有「集群 → 小区」两层，有的可能多一层。把它集中成一处
// 显式可配的结构，将来调整层级顺序或新增层级时只改这里，不用去翻风险计算逻辑。
//
// 层级为 1-based：1 = 根分组所在层。当某实例的祖先链**不足**目标层级时自动降级
// （取最深的可用祖先），因此浅树环境下「跨大区」永远不会误报——所有实例都算同大区。
type BusinessScopeLevels struct {
	// Cluster 集群所在的层级（默认 1，即根分组）。
	Cluster int
	// Region 大区所在的层级（默认 2）。
	Region int
	// Zone 小区所在的层级（默认 3）。
	Zone int
}

// DefaultBusinessScopeLevels 是默认层级约定：根分组 = 集群，第二层 = 大区，第三层 = 小区。
var DefaultBusinessScopeLevels = BusinessScopeLevels{Cluster: 1, Region: 2, Zone: 3}

// Normalize 把非法（<=0）层级回退为默认值，避免配置错误导致判定静默失效。
func (l BusinessScopeLevels) Normalize() BusinessScopeLevels {
	d := DefaultBusinessScopeLevels
	if l.Cluster <= 0 {
		l.Cluster = d.Cluster
	}
	if l.Region <= 0 {
		l.Region = d.Region
	}
	if l.Zone <= 0 {
		l.Zone = d.Zone
	}
	return l
}

// BusinessScopeStats 是一次批量目标在分组树上的分布统计。
//
// 三个 Count 均按**去重后的祖先节点**计数：跨 2 个大区意味着目标分布落在 2 个不同的大区节点下。
type BusinessScopeStats struct {
	// ClusterCount 目标覆盖的集群数（>1 即跨集群）。
	ClusterCount int
	// RegionCount 目标覆盖的大区数（>1 即跨大区）。
	RegionCount int
	// ZoneCount 目标覆盖的小区数（>1 即跨小区）。
	ZoneCount int
	// Unassigned 不属于任何分组的实例数（不参与跨区判定，只作风险说明）。
	Unassigned int
	// MaxDepth 观测到的最大祖先链深度（用于诊断「分组树层级不足」导致的降级）。
	MaxDepth int
}

// ancestorAt 取祖先链（根 → 本节点）上第 level 层（1-based）的节点 ID。
//
// 链深度不足时**降级取链尾（最深的可用祖先）**，这是整套「跨大区」判定降级语义的唯一出处：
// 两层分组树里「大区」与「小区」会塌陷成同一层，跨小区/跨大区的判定自然只剩同层比较，
// 从而不会误报。
func ancestorAt(chain []uint, level int) uint {
	if len(chain) == 0 {
		return 0
	}
	idx := level - 1
	if idx >= len(chain) {
		idx = len(chain) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return chain[idx]
}

// ancestorChain 返回某分组「根 → 本节点」的节点 ID 链（带成环保护）。
func (s *groupTreeSnapshot) ancestorChain(id uint) []uint {
	if s == nil {
		return nil
	}
	chain := make([]uint, 0, 4)
	seen := map[uint]struct{}{}
	cur := id
	for {
		if _, dup := seen[cur]; dup {
			break // 祖先链成环：截断，绝不无限循环
		}
		seen[cur] = struct{}{}
		node, ok := s.byID[cur]
		if !ok {
			break
		}
		chain = append(chain, cur)
		if node.ParentID == nil {
			break
		}
		cur = *node.ParentID
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// ClassifyScope 统计目标实例在分组树上的集群/大区/小区分布。
//
// 一个实例可属于多个分组（InstanceGroupMember 是 M:N），此时它的每条祖先链都会贡献
// 各自的键——语义是「该实例确实落在这些区里」，这是最保守也最诚实的算法。
func (s *BusinessTargetResolver) ClassifyScope(target *BusinessResolvedTarget, levels BusinessScopeLevels) (BusinessScopeStats, error) {
	stats := BusinessScopeStats{}
	if s == nil || s.db == nil || target == nil || len(target.InstanceIDs) == 0 {
		return stats, nil
	}
	levels = levels.Normalize()

	snap, err := s.load()
	if err != nil {
		return stats, err
	}

	var members []model.InstanceGroupMember
	if err := s.db.Where("instance_id IN ?", target.InstanceIDs).Find(&members).Error; err != nil {
		return stats, fmt.Errorf("查询实例分组归属失败: %w", err)
	}
	groupIDsByInstance := make(map[uint][]uint, len(target.InstanceIDs))
	for _, m := range members {
		groupIDsByInstance[m.InstanceID] = append(groupIDsByInstance[m.InstanceID], m.GroupID)
	}

	clusters := map[uint]struct{}{}
	regions := map[uint]struct{}{}
	zones := map[uint]struct{}{}
	for _, instID := range target.InstanceIDs {
		groups := groupIDsByInstance[instID]
		if len(groups) == 0 {
			stats.Unassigned++
			continue
		}
		for _, gid := range groups {
			chain := snap.ancestorChain(gid)
			if len(chain) == 0 {
				continue
			}
			if len(chain) > stats.MaxDepth {
				stats.MaxDepth = len(chain)
			}
			clusters[ancestorAt(chain, levels.Cluster)] = struct{}{}
			regions[ancestorAt(chain, levels.Region)] = struct{}{}
			zones[ancestorAt(chain, levels.Zone)] = struct{}{}
		}
	}
	stats.ClusterCount = len(clusters)
	stats.RegionCount = len(regions)
	stats.ZoneCount = len(zones)
	return stats, nil
}

// BusinessRiskInput 是风险分级的输入。
type BusinessRiskInput struct {
	// Target 已解析的目标集合（决定实例数、在线/离线）。
	Target *BusinessResolvedTarget
	// Scope 分组树分布（跨集群/跨大区/跨小区）。
	Scope BusinessScopeStats
	// ActionRisk 动作自身风险：READ / WRITE / DESTRUCTIVE；空串按 WRITE 处理。
	ActionRisk string
}

// BusinessRiskAssessment 是风险评估结果（对应 RiskAssessment schema）。
type BusinessRiskAssessment struct {
	Level   string   `json:"level"`
	Reasons []string `json:"reasons"`
	// RequiresConfirmation 是否需要 confirmToken（high / critical）。
	RequiresConfirmation bool `json:"requiresConfirmation"`
	// RequiresReason 是否必须填写 reason（critical）。
	RequiresReason bool `json:"requiresReason"`
	// Online / Offline 目标集合的在线/离线实例数（便于调用方直接展示）。
	Online  int `json:"online"`
	Offline int `json:"offline"`
}

// NormalizeActionRisk 归一动作风险：仅 DESTRUCTIVE / READ 被识别，其余（含空串、非法值）
// 一律按 WRITE 处理——与规格「插件未声明时按 WRITE 处理」一致，宁严勿宽。
func NormalizeActionRisk(raw string) string {
	switch raw {
	case BusinessActionRiskRead:
		return BusinessActionRiskRead
	case BusinessActionRiskDestructive:
		return BusinessActionRiskDestructive
	}
	return BusinessActionRiskWrite
}

// businessRiskThresholds 是分级阈值，集中一处便于按运营口径调整（对应规格 §11 待定项 1）。
type businessRiskThresholds struct {
	// SameZoneMaxLow 同小区不超过该数量时为 low。
	SameZoneMaxLow int
	// HighInstanceCount 实例数超过该值直接 high。
	HighInstanceCount int
}

var defaultBusinessRiskThresholds = businessRiskThresholds{
	SameZoneMaxLow:    10,
	HighInstanceCount: 50,
}

// AssessBusinessRisk 按规格 §5 的规则计算风险等级（多条件命中取最高）。
//
// 规则与实现的一一对应：
//
//	单实例 / 同小区 ≤10 台        → low
//	同小区 >10 台                 → medium
//	跨小区、同大区                → medium
//	含离线实例                    → medium
//	跨大区 / 跨集群               → high
//	实例数 > 50                   → high
//	动作 risk=DESTRUCTIVE         → critical
func AssessBusinessRisk(in BusinessRiskInput) BusinessRiskAssessment {
	th := defaultBusinessRiskThresholds
	out := BusinessRiskAssessment{Level: BusinessRiskLow, Reasons: []string{}}

	total := 0
	if in.Target != nil {
		total = in.Target.Count()
		online, offline := in.Target.OnlineOffline()
		out.Online, out.Offline = online, offline
	}
	scope := in.Scope
	actionRisk := NormalizeActionRisk(in.ActionRisk)

	// 跨集群 / 跨大区 / 跨小区：先判最粗的维度，命中即不再按细维度降级描述。
	crossCluster := scope.ClusterCount > 1
	crossRegion := scope.RegionCount > 1
	crossZone := scope.ZoneCount > 1

	if crossCluster {
		out.Level = maxBusinessRisk(out.Level, BusinessRiskHigh)
		out.Reasons = append(out.Reasons, fmt.Sprintf("跨 %d 个集群", scope.ClusterCount))
	} else if crossRegion {
		out.Level = maxBusinessRisk(out.Level, BusinessRiskHigh)
		out.Reasons = append(out.Reasons, fmt.Sprintf("跨 %d 个大区", scope.RegionCount))
	} else if crossZone {
		out.Level = maxBusinessRisk(out.Level, BusinessRiskMedium)
		out.Reasons = append(out.Reasons, fmt.Sprintf("跨 %d 个小区（同大区）", scope.ZoneCount))
	}

	// 规模：同一维度上的数量本身也是风险来源。
	if total > th.HighInstanceCount {
		out.Level = maxBusinessRisk(out.Level, BusinessRiskHigh)
	}
	out.Reasons = append(out.Reasons, fmt.Sprintf("影响 %d 个实例", total))

	// 同小区批量（未跨区的前提下）超过阈值升 medium。
	if !crossCluster && !crossRegion && !crossZone && total > th.SameZoneMaxLow {
		out.Level = maxBusinessRisk(out.Level, BusinessRiskMedium)
		out.Reasons = append(out.Reasons, fmt.Sprintf("同一小区 %d 台（超过 %d 台）", total, th.SameZoneMaxLow))
	}

	// 离线实例：不影响能否执行（逐实例 offline），但要让操作者知道有一部分不会生效。
	if out.Offline > 0 {
		out.Level = maxBusinessRisk(out.Level, BusinessRiskMedium)
		out.Reasons = append(out.Reasons, fmt.Sprintf("含 %d 个离线实例", out.Offline))
	}

	// 未分组实例不参与跨区判定，但仍是影响面的一部分，给出说明避免「以为全在某个区里」。
	if scope.Unassigned > 0 {
		out.Reasons = append(out.Reasons, fmt.Sprintf("含 %d 个未分组实例", scope.Unassigned))
	}

	// 动作自身不可逆：最高档，且必须填原因。
	if actionRisk == BusinessActionRiskDestructive {
		out.Level = BusinessRiskCritical
		out.Reasons = append(out.Reasons, "动作风险为 DESTRUCTIVE（不可逆）")
	}

	out.RequiresConfirmation = businessRiskRank(out.Level) >= businessRiskRank(BusinessRiskHigh)
	out.RequiresReason = out.Level == BusinessRiskCritical
	return out
}
