package service

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// Beacon 拉取映射相关常量（FR-444，见 ADR-090 §5）。
const (
	// BeaconTagRegionPrefix / BeaconTagZonePrefix / BeaconTagRolePrefix 是拉取时补齐的标签前缀。
	// 与 FR-047 的 `env:` 同属「前缀即维度」约定，不新增字段、不新增迁移。
	BeaconTagRegionPrefix = "region:"
	BeaconTagZonePrefix   = "zone:"
	BeaconTagRolePrefix   = "role:"

	// BeaconRoleLobby / BeaconRoleGame 是 role 标签的两个取值：
	// 由 Beacon 的 isDefaultEntry 推断——默认入口 = 大厅服（lobby），其余 = 游戏服（game）。
	BeaconRoleLobby = "lobby"
	BeaconRoleGame  = "game"

	// 拉取审计动作（FR-444）。与 FR-443 的 instance.beacon_push_* 分列，便于按方向排查。
	AuditActionBeaconPullOK   = "instance.beacon_pull_ok"
	AuditActionBeaconPullFail = "instance.beacon_pull_fail"
	// 拉取写入的审计目标类型（分组树整体，非单个实例）。
	AuditTargetBeaconPull = "instance_group_tree"
)

// Beacon 拉取侧错误。
var (
	// ErrBeaconSyncDisabled 拉取服务未配置或未启用（可选协同未开启）。
	ErrBeaconSyncDisabled = fmt.Errorf("Beacon 拓扑拉取未启用")

	// ErrBeaconMappingRootNotFound 配置的挂载分组/团队不存在。
	ErrBeaconMappingRootNotFound = fmt.Errorf("Beacon 映射挂载分组不存在")

	// ErrBeaconTopologyInvalid 拉取到的拓扑通过校验不通过（缺 code、引用悬空等）。
	// 出现在写入前，故本地零改动。
	ErrBeaconTopologyInvalid = fmt.Errorf("Beacon 拓扑数据不合法")
)

// BeaconSyncConfig 是 Beacon 拉取映射服务配置（FR-444）。
type BeaconSyncConfig struct {
	// NamespaceID Beacon namespace；0 表示不限（Beacon 侧全量）。
	NamespaceID uint
}

// BeaconPullResult 是一次拉取的映射结果摘要（供手动触发端点与审计使用）。
type BeaconPullResult struct {
	// NamespaceID 本次拉取的 namespace。
	NamespaceID uint `json:"namespaceId"`
	// Clusters / Regions / Zones 是本轮映射得到的分组数（含复用，均为根级/父子三层）。
	Clusters int `json:"clusters"`
	Regions  int `json:"regions"`
	Zones    int `json:"zones"`
	// CreatedGroups 本轮新建的分组节点数（重复拉取应为 0，即幂等）。
	CreatedGroups int `json:"createdGroups"`
	// MatchedInstances 成功匹配到本地实例的 server 数。
	MatchedInstances int `json:"matchedInstances"`
	// RetaggedInstances 因本轮补标签而发生变更的实例数。
	RetaggedInstances int `json:"retaggedInstances"`
	// MovedInstances 因归属变化而被移入 Beacon 分组的实例数。
	MovedInstances int `json:"movedInstances"`
	// SkippedServers 本地无对应实例而跳过的 server（Beacon 有而本地无，FR-444 §4.3）。
	SkippedServers []string `json:"skippedServers"`
	// Warnings 非致命的映射告警（如 code 缺失被跳过），逐条供运维核对。
	Warnings []string `json:"warnings"`
}

// BeaconSyncService 实现 FR-444「从 Beacon 拉取拓扑并映射为分组树」。
//
// 两个硬约束（ADR-090 §5）：
//  1. 可选协同，绝非依赖——未配置 endpoint 时全部方法返回 ErrBeaconSyncDisabled，
//     不触碰任何本地数据；本平台其余功能完全不受影响。
//  2. 失败零改动——先全量拉取到内存并校验，全部通过后才在**单事务**内写入
//     （分组树 + 成员归属 + 标签）；任一步失败整体回滚，不留下半棵树。
type BeaconSyncService struct {
	db     *gorm.DB
	client *BeaconClient
	groups *InstanceGroupService
	cfg    BeaconSyncConfig
	// audit 可选；nil 时跳过审计（单测轻量场景）。
	audit *AuditService
}

// NewBeaconSyncService 创建 Beacon 拉取映射服务。
// client 为 nil（未配置 beacon.endpoint）时服务仍构造成功，但所有拉取返回 ErrBeaconSyncDisabled。
func NewBeaconSyncService(db *gorm.DB, client *BeaconClient, cfg BeaconSyncConfig) *BeaconSyncService {
	return &BeaconSyncService{
		db:     db,
		client: client,
		groups: NewInstanceGroupService(db),
		cfg:    cfg,
	}
}

// SetAuditService 注入审计服务（在 main 装配阶段调用）。nil 时不写审计。
func (s *BeaconSyncService) SetAuditService(a *AuditService) { s.audit = a }

// Configured 报告是否已配置 Beacon 协同端点（beacon.endpoint 非空）。
// 与 Enabled 的区别：配置了端点但没开 pull-enabled 时 Configured=true、Enabled=false，
// 前端据此区分「未部署 Beacon」（隐藏入口）与「已部署但未开启拉取」（提示开关）。
func (s *BeaconSyncService) Configured() bool {
	return s != nil && s.client.Enabled()
}

// Enabled 报告拉取是否可用（已配置端点 + 已启用 + 已就绪）。
func (s *BeaconSyncService) Enabled() bool {
	return s != nil && s.client.Enabled() && s.client.cfg.PullEnabled
}

// PullTopology 从 Beacon 拉取拓扑并映射为本地分组树（FR-444）。
//
// 调用方为「手动触发」（ADR-090：首次拉取必须手动，不自动建树）。
// 流程严格两段：
//
//	① 拉取 + 校验：全量读入内存，不写任何本地数据；失败即返回错误（本地零改动）。
//	② 事务写入：分组树 + 成员归属 + 标签在同一事务内落地，任一步失败整体回滚。
//
// userID 用于审计主体；ip 记录触发来源。
func (s *BeaconSyncService) PullTopology(ctx context.Context, userID uint, ip string) (*BeaconPullResult, error) {
	if !s.Enabled() {
		return nil, ErrBeaconSyncDisabled
	}

	topo, err := s.client.PullTopology(ctx, s.cfg.NamespaceID)
	if err != nil {
		// 拉取失败：本地数据一个字节都没动（FR-444 §4.4）。
		s.recordPull(userID, ip, nil, err)
		return nil, err
	}

	plan, err := s.plan(topo)
	if err != nil {
		s.recordPull(userID, ip, nil, err)
		return nil, err
	}

	result, err := s.apply(plan)
	if err != nil {
		s.recordPull(userID, ip, plan.result(), err)
		return nil, err
	}
	s.recordPull(userID, ip, result, nil)
	return result, nil
}

// beaconPlan 是「已拉取 + 已校验」的映射计划：纯数据，尚未触碰本地库。
type beaconPlan struct {
	namespaceID uint
	// nodes 按父子顺序排列的待确保分组（父先于子）。根级节点即 bc_cluster（parentKey 为空）。
	nodes []beaconPlanNode
	// assignments 待落地的「实例 → 小组」归属 + 标签。
	assignments []beaconPlanAssignment
	// skippedServers / warnings 承载非致命告警。
	skippedServers []string
	warnings       []string
}

// beaconPlanNode 是计划中的分组节点。
type beaconPlanNode struct {
	// key 是树内唯一键（用于父子引用），形如 bc:<clusterCode> / region:<code> / zone:<code>。
	key string
	// parentKey 为空表示根分组（FR-444 §4.1：bc_cluster → 根分组）。
	parentKey string
	name      string
}

// beaconPlanAssignment 是计划中的一次实例归宿。
type beaconPlanAssignment struct {
	zoneKey    string
	instanceID uint
	tags       []string
}

// result 返回计划对应的结果骨架（失败审计用；计数在 apply 后补齐）。
func (p *beaconPlan) result() *BeaconPullResult {
	return &BeaconPullResult{
		NamespaceID:    p.namespaceID,
		SkippedServers: p.skippedServers,
		Warnings:       p.warnings,
	}
}

// plan 把 Beacon 拓扑转换为纯内存的写入计划并做全量校验（只读本地库，不写）。
// 校验不过即返回错误，此时本地尚未发生任何写入。
func (s *BeaconSyncService) plan(topo *BeaconTopology) (*beaconPlan, error) {
	if topo == nil {
		return nil, fmt.Errorf("%w: 拓扑响应为空", ErrBeaconTopologyInvalid)
	}
	p := &beaconPlan{namespaceID: s.cfg.NamespaceID}

	// ① Beacon 结构 → 计划节点（FR-444 §4.1 映射规则）：
	//    bc_cluster → 根分组；region → 子分组；zone → 孙分组；server → 实例加入 zone 分组。
	// 同时建立 zone.ID → 分组 key/code 索引，供 server 归位与标签生成。
	zoneKeyByID := map[uint]string{}
	zoneCodeByID := map[uint]string{}
	regionCodeByZoneID := map[uint]string{}
	for ci := range topo.Clusters {
		cluster := &topo.Clusters[ci]
		clusterName := beaconNodeName(cluster.Code, cluster.Name, cluster.DisplayName)
		if clusterName == "" {
			p.warnings = append(p.warnings, fmt.Sprintf("跳过 BC 集群（id=%d）：code/name 均为空", cluster.ID))
			continue
		}
		clusterKey := "bc:" + clusterName
		p.nodes = append(p.nodes, beaconPlanNode{key: clusterKey, name: clusterName})

		for ri := range cluster.Regions {
			region := &cluster.Regions[ri]
			regionName := beaconNodeName(region.Code, region.Name, region.DisplayName)
			if regionName == "" {
				p.warnings = append(p.warnings, fmt.Sprintf("跳过大区（id=%d，集群 %s）：code/name 均为空", region.ID, clusterName))
				continue
			}
			regionKey := clusterKey + "/region:" + regionName
			p.nodes = append(p.nodes, beaconPlanNode{key: regionKey, parentKey: clusterKey, name: regionName})

			for zi := range region.Zones {
				zone := &region.Zones[zi]
				zoneName := beaconNodeName(zone.Code, zone.Name, zone.DisplayName)
				if zoneName == "" {
					p.warnings = append(p.warnings, fmt.Sprintf("跳过小区（id=%d，大区 %s）：code/name 均为空", zone.ID, regionName))
					continue
				}
				zoneKey := regionKey + "/zone:" + zoneName
				p.nodes = append(p.nodes, beaconPlanNode{key: zoneKey, parentKey: regionKey, name: zoneName})
				zoneKeyByID[zone.ID] = zoneKey
				zoneCodeByID[zone.ID] = zoneName
				regionCodeByZoneID[zone.ID] = regionName
			}
		}
	}

	// ② server 归属 → 实例集合。Beacon 有而本地无 → 跳过并警告（不同步创建实例）。
	byName, err := s.loadInstanceIndexByName()
	if err != nil {
		return nil, err
	}
	claimedZones := map[string]struct{}{}
	for i := range topo.Servers {
		server := &topo.Servers[i]
		if !beaconServerParticipates(server) {
			continue
		}
		serverName := strings.TrimSpace(server.ServerID)
		if serverName == "" {
			p.warnings = append(p.warnings, fmt.Sprintf("跳过 server（Beacon id=%d）：serverId 为空", server.ID))
			continue
		}
		if server.ZoneID == nil {
			// 未分派到小区的 server 不建树（Beacon 侧 unassigned）。
			p.warnings = append(p.warnings, fmt.Sprintf("跳过 server %s：在 Beacon 侧未分派小区", serverName))
			continue
		}
		zoneKey, ok := zoneKeyByID[*server.ZoneID]
		if !ok {
			p.warnings = append(p.warnings, fmt.Sprintf("跳过 server %s：所属小区（Beacon zoneId=%d）不在结构树中", serverName, *server.ZoneID))
			continue
		}
		inst, ok := byName[serverName]
		if !ok {
			p.skippedServers = append(p.skippedServers, serverName)
			continue
		}
		p.assignments = append(p.assignments, beaconPlanAssignment{
			zoneKey:    zoneKey,
			instanceID: inst.ID,
			tags: beaconTags(
				regionCodeByZoneID[*server.ZoneID],
				zoneCodeByID[*server.ZoneID],
				server.IsDefaultEntry,
			),
		})
		claimedZones[zoneKey] = struct{}{}
	}
	sort.Strings(p.skippedServers)
	sort.Strings(p.warnings)

	// ③ 校验：拉取结果里没有任何结构却要写树 = 拓扑不可用，宁可报错不改本地。
	if len(p.nodes) == 0 {
		return nil, fmt.Errorf("%w: 未解析出任何 BC 集群/大区/小区（namespaceId=%d）", ErrBeaconTopologyInvalid, s.cfg.NamespaceID)
	}
	return p, nil
}

// apply 在单事务内落地计划：分组树 → 成员归属 → 标签。
// 任一步失败整体回滚，保证「Beacon 不可达/数据不合法时本地零改动」以及失败不留半棵树。
func (s *BeaconSyncService) apply(p *beaconPlan) (*BeaconPullResult, error) {
	result := p.result()
	// 事务外先读取既有节点与成员关系：读失败同样不写任何数据。
	existing, err := s.groups.loadNodes()
	if err != nil {
		return nil, err
	}
	groupIDByKey, created, err := s.resolvePlanNodes(existing, p)
	if err != nil {
		return nil, err
	}
	membersByInstance, err := s.loadGroupMembersByInstance()
	if err != nil {
		return nil, err
	}
	instancesByID, err := s.loadInstancesByID(p)
	if err != nil {
		return nil, err
	}

	// 统计移入数与标签变更数（纯内存计算，供结果摘要）。
	moved := 0
	retagged := 0
	for i := range p.assignments {
		a := &p.assignments[i]
		inst := instancesByID[a.instanceID]
		if inst == nil {
			continue
		}
		if !memberOf(membersByInstance[a.instanceID], groupIDByKey[a.zoneKey]) {
			moved++
		}
		if !sameTags(model.ParseTags(inst.Tags), a.tags) {
			retagged++
		}
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		groupSvc := NewInstanceGroupService(tx)

		// 1) 分组树：按顺序确保存在（复用同名节点，不重复创建）。
		groupIDByKey, err = ensurePlanGroups(tx, groupSvc, p, groupIDByKey)
		if err != nil {
			return err
		}
		// 2) 成员归属：实例移入 Beacon 对应分组（Beacon 为拓扑真源）。
		if err := applyAssignments(tx, p, groupIDByKey); err != nil {
			return err
		}
		// 3) 标签：region:/zone:/role: 整体落库（就地更新，不触发 Worker 同步与推送）。
		return applyTags(tx, p, instancesByID, groupIDByKey)
	})
	if err != nil {
		return nil, fmt.Errorf("写入 Beacon 拓扑映射失败（已回滚，本地零改动）: %w", err)
	}

	result.CreatedGroups = created
	result.Clusters, result.Regions, result.Zones = countPlanGroups(p)
	result.MatchedInstances = len(p.assignments)
	result.MovedInstances = moved
	result.RetaggedInstances = retagged
	slog.Info("Beacon 拓扑拉取完成",
		"namespaceId", result.NamespaceID,
		"clusters", result.Clusters, "regions", result.Regions, "zones", result.Zones,
		"createdGroups", result.CreatedGroups,
		"matchedInstances", result.MatchedInstances,
		"movedInstances", result.MovedInstances,
		"retaggedInstances", result.RetaggedInstances,
		"skippedServers", len(result.SkippedServers))
	return result, nil
}

// resolvePlanNodes 在既有节点中定位计划节点：已存在同名同父节点则复用，否则标记待建。
// 返回 key → 现有节点 ID 的映射（新建节点不在其中，由事务内补齐）与本轮待建数。
//
// FR-444 §4.1：bc_cluster 是**根分组**（ParentID=nil），故根层匹配在同层节点间按名比对；
// 大区/小区则按「父 ID + 名」在同父兄弟间比对。
func (s *BeaconSyncService) resolvePlanNodes(existing []model.InstanceGroupNode, p *beaconPlan) (map[string]uint, int, error) {
	byKey := map[string]uint{}
	rootChildren := map[string]uint{}
	for i := range existing {
		n := &existing[i]
		if n.ParentID == nil {
			if _, dup := rootChildren[n.Name]; !dup {
				rootChildren[n.Name] = n.ID
			}
		}
	}
	created := 0
	// 逐级解析：簇（根）→ 大区（挂簇）→ 小区（挂大区）。
	for i := range p.nodes {
		node := &p.nodes[i]
		if node.parentKey == "" {
			if id, ok := rootChildren[node.name]; ok {
				byKey[node.key] = id
			} else {
				created++
			}
			continue
		}
		parentID, ok := byKey[node.parentKey]
		if !ok {
			// 父节点本轮将新建，子节点必然也是新建。
			created++
			continue
		}
		if id, ok := findChildByName(existing, &parentID, node.name); ok {
			byKey[node.key] = id
		} else {
			created++
		}
	}
	return byKey, created, nil
}

// recordPull 写拉取审计（成功 ok / 失败 fail + 错误详情）。
// 审计失败不阻断主流程（与 FR-443 推送同口径）。
func (s *BeaconSyncService) recordPull(userID uint, ip string, result *BeaconPullResult, pullErr error) {
	if s.audit == nil {
		return
	}
	if pullErr != nil {
		s.audit.RecordResultSafe(userID, AuditActionBeaconPullFail, AuditTargetBeaconPull, "", "", ip, false, pullErr.Error())
		return
	}
	detail := ""
	if result != nil {
		detail = fmt.Sprintf("namespaceId=%d 集群=%d 大区=%d 小区=%d 新建分组=%d 匹配实例=%d 移入=%d 补标签=%d 跳过=%d 告警=%d",
			result.NamespaceID, result.Clusters, result.Regions, result.Zones, result.CreatedGroups,
			result.MatchedInstances, result.MovedInstances, result.RetaggedInstances,
			len(result.SkippedServers), len(result.Warnings))
	}
	s.audit.RecordResultSafe(userID, AuditActionBeaconPullOK, AuditTargetBeaconPull, "", detail, ip, true, "")
}

// beaconNodeName 取 Beacon 节点的分组名：优先 code（跨系统稳定的机器标识），
// 回退 name / displayName；均空时返回空串由调用方跳过。
func beaconNodeName(code, name, displayName string) string {
	for _, candidate := range []string{code, name, displayName} {
		if v := strings.TrimSpace(candidate); v != "" {
			return v
		}
	}
	return ""
}

// beaconServerParticipates 判断 server 是否参与建树：
// 已墓碑（永久删除）或非生效态的记录跳过，避免把归档数据映射成分组与归属。
func beaconServerParticipates(s *BeaconServerView) bool {
	if s == nil || s.Tombstone != nil {
		return false
	}
	switch s.LifecycleStatus {
	case "", "active":
		return true
	default:
		return s.EffectiveActive
	}
}

// beaconTags 生成 FR-444 §4.1 约定的三个标签：region / zone / role。
// role 由 isDefaultEntry 推断：默认入口 = lobby（大厅），其余 = game（游戏）。
func beaconTags(regionCode, zoneCode string, isDefaultEntry bool) []string {
	tags := make([]string, 0, 3)
	if v := strings.TrimSpace(regionCode); v != "" {
		tags = append(tags, BeaconTagRegionPrefix+v)
	}
	if v := strings.TrimSpace(zoneCode); v != "" {
		tags = append(tags, BeaconTagZonePrefix+v)
	}
	role := BeaconRoleGame
	if isDefaultEntry {
		role = BeaconRoleLobby
	}
	return append(tags, BeaconTagRolePrefix+role)
}

// mergeBeaconTags 把 Beacon 维度标签并入既有标签集合（就地覆盖同维度旧值，保留其它标签）。
// 例如旧值 [env:prod, region:old] 合并 region:r1 得 [env:prod, region:r1]。
func mergeBeaconTags(existing []string, desired []string) []string {
	if len(desired) == 0 {
		return model.NormalizeTags(existing)
	}
	prefixes := make([]string, 0, len(desired))
	for _, t := range desired {
		if i := strings.Index(t, ":"); i >= 0 {
			prefixes = append(prefixes, t[:i+1])
		}
	}
	out := make([]string, 0, len(existing)+len(desired))
	for _, t := range existing {
		replaced := false
		for _, p := range prefixes {
			if strings.HasPrefix(t, p) {
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, t)
		}
	}
	return model.NormalizeTags(append(out, desired...))
}

// sameTags 判断两组标签规范化后是否等价（用于统计补标签变更数）。
func sameTags(a, b []string) bool {
	na, nb := model.NormalizeTags(a), model.NormalizeTags(b)
	if len(na) != len(nb) {
		return false
	}
	for i := range na {
		if na[i] != nb[i] {
			return false
		}
	}
	return true
}

// loadInstanceIndexByName 建立「实例名 → 实例」索引。
// Beacon 的 serverId 与 JianManager 的实例名对应（FR-444 §4.1 的映射约定）。
func (s *BeaconSyncService) loadInstanceIndexByName() (map[string]*model.Instance, error) {
	var instances []model.Instance
	if err := s.db.Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}
	byName := make(map[string]*model.Instance, len(instances))
	for i := range instances {
		inst := &instances[i]
		name := strings.TrimSpace(inst.Name)
		if name == "" {
			continue
		}
		// 同名冲突时保留 ID 较小者（确定性），并在映射阶段按需跳过歧义项。
		if _, dup := byName[name]; !dup {
			byName[name] = inst
		}
	}
	return byName, nil
}

// loadGroupMembersByInstance 返回 instanceID → 已归属的分组 ID 集合（仅现有实例）。
func (s *BeaconSyncService) loadGroupMembersByInstance() (map[uint][]uint, error) {
	type row struct {
		GroupID    uint
		InstanceID uint
	}
	var rows []row
	// 与 InstanceGroupService.memberIDsByGroup 同口径：JOIN 过滤悬空成员。
	if err := s.db.Model(&model.InstanceGroupMember{}).
		Select("instance_group_members.group_id, instance_group_members.instance_id").
		Joins("JOIN instances ON instances.id = instance_group_members.instance_id AND instances.deleted_at IS NULL").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询分组成员失败: %w", err)
	}
	out := map[uint][]uint{}
	for _, r := range rows {
		out[r.InstanceID] = append(out[r.InstanceID], r.GroupID)
	}
	return out, nil
}

// loadInstancesByID 按计划涉及的实例 ID 批量取实例行（事务外读取，供变更统计）。
func (s *BeaconSyncService) loadInstancesByID(p *beaconPlan) (map[uint]*model.Instance, error) {
	ids := make([]uint, 0, len(p.assignments))
	for i := range p.assignments {
		ids = append(ids, p.assignments[i].instanceID)
	}
	out := make(map[uint]*model.Instance, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var instances []model.Instance
	if err := s.db.Where("id IN ?", ids).Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}
	for i := range instances {
		out[instances[i].ID] = &instances[i]
	}
	return out, nil
}

// findChildByName 在节点集合中查找指定父下的同名子节点（parentID=nil 表示根级）。
func findChildByName(nodes []model.InstanceGroupNode, parentID *uint, name string) (uint, bool) {
	for i := range nodes {
		n := &nodes[i]
		if n.Name != name {
			continue
		}
		switch {
		case parentID == nil && n.ParentID == nil:
			return n.ID, true
		case parentID != nil && n.ParentID != nil && *n.ParentID == *parentID:
			return n.ID, true
		}
	}
	return 0, false
}

// memberOf 判断 groupIDs 是否含目标分组。
func memberOf(groupIDs []uint, target uint) bool {
	for _, id := range groupIDs {
		if id == target {
			return true
		}
	}
	return false
}

// countPlanGroups 统计计划中各层级的分组节点数。
func countPlanGroups(p *beaconPlan) (clusters, regions, zones int) {
	for i := range p.nodes {
		switch {
		case p.nodes[i].parentKey == "":
			clusters++
		case strings.Contains(p.nodes[i].key, "/region:") && !strings.Contains(p.nodes[i].key, "/zone:"):
			regions++
		default:
			zones++
		}
	}
	return clusters, regions, zones
}
