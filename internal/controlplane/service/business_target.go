package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 业务批量目标解析（FR-535 §2）。
//
// 解析顺序（各条件 AND 语义，从粗到细收敛）：
//  1. groupIds + includeDescendants → 递归展开分组子树为实例集合
//  2. 与 instanceIds 取交集（两者都给出时）
//  3. 应用 filters（nodeId / status / role）
//  4. 去重并按实例 ID 升序排序
//
// 分组树是 JM 自有真源（InstanceGroupNode 自引用邻接表，ADR-033），**不从 Beacon 依赖**。
// 由于是自引用邻接表，理论上允许脏数据成环，递归一律带深度上限与访问集保护。

const (
	// businessGroupTreeMaxDepth 分组树递归深度上限（防脏数据成环导致无限递归）。
	businessGroupTreeMaxDepth = 64
	// businessTargetPreviewMaxIDs 预览响应中 instanceIds 的完整返回上限（FR-535 §3.1）。
	businessTargetPreviewMaxIDs = 500
	// businessTargetPreviewHeadIDs 超过上限时只返回前 N 个 ID。
	businessTargetPreviewHeadIDs = 100
)

// BusinessTargetPreviewMaxIDs 暴露预览实例 ID 完整返回上限（路由层组装响应时使用）。
func BusinessTargetPreviewMaxIDs() int { return businessTargetPreviewMaxIDs }

// BusinessTargetPreviewHeadIDs 暴露超限时的截断长度。
func BusinessTargetPreviewHeadIDs() int { return businessTargetPreviewHeadIDs }

// BusinessBatchFilter 目标解析的附加筛选条件（对应 BatchTarget.filters）。
//
// 三个字段均可空；空表示该维度不过滤。与 InstanceBatchFilterIn 的差异：本结构只做
// 「在已解析集合上过滤」，不参与 SQL 拼接，因此可以在解析后统一应用（可测性好）。
type BusinessBatchFilter struct {
	NodeID *uint   `json:"nodeId,omitempty"`
	Status *string `json:"status,omitempty"`
	Role   *string `json:"role,omitempty"`
}

// BusinessTarget 业务批量目标（对应 BatchTarget schema）。
//
// 所有条件 AND 语义：分组展开 → 与 instanceIds 交集 → filters 收敛。
// 若 groupIds 与 instanceIds 均为空，请求视为非法（否则会退化成「全量实例」，
// 这是运维上极其危险的隐式行为）。
type BusinessTarget struct {
	GroupIDs           []uint               `json:"groupIds,omitempty"`
	IncludeDescendants bool                 `json:"includeDescendants"`
	InstanceIDs        []uint               `json:"instanceIds,omitempty"`
	Filters            *BusinessBatchFilter `json:"filters,omitempty"`
}

// Empty 判断目标是否未指定任何维度（既无分组也无实例列表）。
func (t BusinessTarget) Empty() bool {
	return len(t.GroupIDs) == 0 && len(t.InstanceIDs) == 0
}

// BusinessGroupRef 预览响应中「命中分组」的展示项。
type BusinessGroupRef struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	// Path 完整路径，形如 cluster-main/region1（根到本节点，用 / 连接）。
	Path string `json:"path"`
	// InstanceCount 本分组（按 includeDescendants 口径）命中的实例数。
	InstanceCount int `json:"instanceCount"`
}

// BusinessResolvedTarget 目标解析结果（服务层内部表示）。
type BusinessResolvedTarget struct {
	// InstanceIDs 去重并按 ID 升序排序后的实例集合。
	InstanceIDs []uint
	// Instances 与 InstanceIDs 一一对应的实例记录（含 NodeID/Status/Role/Node.UUID）。
	Instances []model.Instance
	// Groups 命中分组及其路径信息（按 groupIds 请求顺序去重）。
	Groups []BusinessGroupRef
}

// Count 返回实例总数。
func (r *BusinessResolvedTarget) Count() int {
	if r == nil {
		return 0
	}
	return len(r.InstanceIDs)
}

// OnlineOffline 统计在线/离线实例数。
//
// 「离线」判定口径：实例状态非 RUNNING 即计入离线（未下发）。
// 这里刻意不去探针名册做实时探测——预览必须是廉价的只读操作（§7.1 无副作用、可重复调用），
// 且执行阶段会再按真实下发结果给出精确的 offline 计数。
func (r *BusinessResolvedTarget) OnlineOffline() (online, offline int) {
	if r == nil {
		return 0, 0
	}
	for i := range r.Instances {
		if r.Instances[i].Status == model.InstanceStatusRunning {
			online++
		} else {
			offline++
		}
	}
	return online, offline
}

// Hash 计算实例集合的稳定指纹（用于 confirmToken 绑定，FR-535 §4.1）。
//
// 输入已是排序去重的 ID 列表，直接以逗号拼接后取 SHA-256，得到固定长度十六进制串。
// 之所以不用 JSON：目标集合的唯一语义就是「哪些 ID」，任何序列化细节（空格/括号）
// 都不应影响指纹，拼串最简单也最不可能因编码差异漂移。
func (r *BusinessResolvedTarget) Hash() string {
	return BusinessTargetHash(r.InstanceIDs)
}

// BusinessTargetHash 计算实例 ID 集合的稳定指纹。不修改入参，内部自行排序去重。
func BusinessTargetHash(ids []uint) string {
	canonical := canonicalInstanceIDs(ids)
	parts := make([]string, len(canonical))
	for i, id := range canonical {
		parts[i] = strconv.FormatUint(uint64(id), 10)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, ",")))
	return hex.EncodeToString(sum[:])
}

// canonicalInstanceIDs 排序 + 去重，返回新切片（不改动入参）。
func canonicalInstanceIDs(ids []uint) []uint {
	if len(ids) == 0 {
		return nil
	}
	out := append([]uint(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	deduped := out[:0]
	for i, id := range out {
		if i > 0 && id == out[i-1] {
			continue
		}
		deduped = append(deduped, id)
	}
	return deduped
}

// BusinessTargetResolver 解析业务批量目标。
//
// 无状态：每次解析都从 DB 现读分组树与实例表，因此「预览 → 执行」之间分组发生增删
// 能被 confirmToken 的 hash 比对捕获（§4.2）。
type BusinessTargetResolver struct {
	db *gorm.DB
}

// NewBusinessTargetResolver 创建目标解析器。
func NewBusinessTargetResolver(db *gorm.DB) *BusinessTargetResolver {
	return &BusinessTargetResolver{db: db}
}

// groupTreeSnapshot 是一次加载的分组树快照，供递归展开与路径计算复用（避免 N 次查树）。
type groupTreeSnapshot struct {
	byID     map[uint]model.InstanceGroupNode
	children map[uint][]uint
}

// load 一次性加载全部分组节点并建立「父 → 子」索引。
func (s *BusinessTargetResolver) load() (*groupTreeSnapshot, error) {
	var nodes []model.InstanceGroupNode
	if err := s.db.Order("id").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询分组树失败: %w", err)
	}
	snap := &groupTreeSnapshot{
		byID:     make(map[uint]model.InstanceGroupNode, len(nodes)),
		children: make(map[uint][]uint, len(nodes)),
	}
	for _, n := range nodes {
		snap.byID[n.ID] = n
		if n.ParentID != nil {
			snap.children[*n.ParentID] = append(snap.children[*n.ParentID], n.ID)
		}
	}
	return snap, nil
}

// subtreeIDs 返回某分组自身 + 全部后代的节点 ID 集合（深度与访问集双保护防环）。
func (s *groupTreeSnapshot) subtreeIDs(root uint, includeDescendants bool) []uint {
	if s == nil {
		return nil
	}
	if _, ok := s.byID[root]; !ok {
		return nil
	}
	visited := map[uint]struct{}{root: {}}
	order := []uint{root}
	if !includeDescendants {
		return order
	}
	stack := []uint{root}
	depth := 0
	for len(stack) > 0 && depth < businessGroupTreeMaxDepth {
		next := make([]uint, 0, len(stack))
		for _, cur := range stack {
			for _, child := range s.children[cur] {
				if _, seen := visited[child]; seen {
					// 自引用邻接表的脏数据可能成环：已访问即跳过，绝不重复展开。
					continue
				}
				visited[child] = struct{}{}
				order = append(order, child)
				next = append(next, child)
			}
		}
		stack = next
		depth++
	}
	return order
}

// path 返回根到该节点的名称路径（`cluster-main/region1`）。祖先链上成环时截断。
func (s *groupTreeSnapshot) path(id uint) string {
	if s == nil {
		return ""
	}
	names := make([]string, 0, 4)
	seen := map[uint]struct{}{}
	cur := id
	for {
		if _, dup := seen[cur]; dup {
			break // 祖先链成环，截断避免死循环
		}
		seen[cur] = struct{}{}
		node, ok := s.byID[cur]
		if !ok {
			break
		}
		names = append(names, node.Name)
		if node.ParentID == nil {
			break
		}
		cur = *node.ParentID
	}
	// 收集顺序是「本节点 → 根」，反转成「根 → 本节点」。
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}
	return strings.Join(names, "/")
}

// resolveGroupInstanceIDs 展开分组集合（含可选后代）对应的实例 ID 集合。
//
// 同时返回每个**请求分组**（groupIds 里的那个）命中的实例数，供预览的 groups 字段展示。
func (s *BusinessTargetResolver) resolveGroupInstanceIDs(
	target BusinessTarget, snap *groupTreeSnapshot,
) (map[uint]struct{}, map[uint]int, error) {
	if len(target.GroupIDs) == 0 {
		return nil, nil, nil
	}
	// 请求分组 → 展开的节点 ID 集合（含重复节点，下面合并成一张表）。
	perGroupNodes := make(map[uint][]uint, len(target.GroupIDs))
	allNodes := map[uint]struct{}{}
	for _, gid := range target.GroupIDs {
		nodes := snap.subtreeIDs(gid, target.IncludeDescendants)
		if len(nodes) == 0 {
			// 分组不存在：按空集合处理（不报错），由最终 TARGET_EMPTY 统一表达。
			perGroupNodes[gid] = nil
			continue
		}
		perGroupNodes[gid] = nodes
		for _, n := range nodes {
			allNodes[n] = struct{}{}
		}
	}
	if len(allNodes) == 0 {
		return map[uint]struct{}{}, make(map[uint]int, len(target.GroupIDs)), nil
	}
	nodeIDs := make([]uint, 0, len(allNodes))
	for n := range allNodes {
		nodeIDs = append(nodeIDs, n)
	}

	// 一次查询拿到「节点 → 实例」关系，避免按组循环打库。
	var members []model.InstanceGroupMember
	if err := s.db.Where("group_id IN ?", nodeIDs).Find(&members).Error; err != nil {
		return nil, nil, fmt.Errorf("查询分组实例关系失败: %w", err)
	}
	instByNode := make(map[uint][]uint, len(nodeIDs))
	for _, m := range members {
		instByNode[m.GroupID] = append(instByNode[m.GroupID], m.InstanceID)
	}

	result := map[uint]struct{}{}
	for _, ids := range instByNode {
		for _, id := range ids {
			result[id] = struct{}{}
		}
	}

	counts := make(map[uint]int, len(target.GroupIDs))
	for gid, nodes := range perGroupNodes {
		seen := map[uint]struct{}{}
		for _, n := range nodes {
			for _, id := range instByNode[n] {
				seen[id] = struct{}{}
			}
		}
		counts[gid] = len(seen)
	}
	return result, counts, nil
}

// BusinessTargetScope 是调用方的可访问实例范围（跨组隔离）。
//
// Scoped=false 表示平台管理员（不收敛）；Scoped=true 且 IDs 为空表示调用方没有任何
// 可访问实例，此时解析结果必为空（存在性隐藏：越权目标不出现在结果里，也不报错）。
type BusinessTargetScope struct {
	IDs    []uint
	Scoped bool
}

// allows 判断某实例是否在范围内。
func (sc BusinessTargetScope) allows(id uint) bool {
	if !sc.Scoped {
		return true
	}
	for _, allowed := range sc.IDs {
		if allowed == id {
			return true
		}
	}
	return false
}

// Resolve 解析业务批量目标（不收敛调用方范围，供内部/管理员路径使用）。
func (s *BusinessTargetResolver) Resolve(target BusinessTarget) (*BusinessResolvedTarget, error) {
	return s.ResolveScoped(target, BusinessTargetScope{})
}

// ResolveScoped 解析业务批量目标并收敛到调用方可访问的实例集合。
//
// 返回 (解析结果, error)。目标为空（解析后无任何实例）不返回 error——由调用方映射为
// TARGET_EMPTY（400），好让路由层统一决定状态码。
func (s *BusinessTargetResolver) ResolveScoped(target BusinessTarget, scope BusinessTargetScope) (*BusinessResolvedTarget, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("目标解析器未初始化")
	}
	if target.Empty() {
		return nil, ErrBusinessTargetUnspecified
	}
	if scope.Scoped && len(scope.IDs) == 0 {
		// 无任何可访问实例：直接空结果，不必打库。
		return &BusinessResolvedTarget{}, nil
	}

	snap, err := s.load()
	if err != nil {
		return nil, err
	}

	// ① 分组展开
	groupInstances, groupCounts, err := s.resolveGroupInstanceIDs(target, snap)
	if err != nil {
		return nil, err
	}

	// ② 与 instanceIds 取交集
	candidate := groupInstances
	if candidate == nil {
		// 只给了 instanceIds：直接以它为候选集
		candidate = make(map[uint]struct{}, len(target.InstanceIDs))
		for _, id := range target.InstanceIDs {
			candidate[id] = struct{}{}
		}
	} else if len(target.InstanceIDs) > 0 {
		explicit := make(map[uint]struct{}, len(target.InstanceIDs))
		for _, id := range target.InstanceIDs {
			explicit[id] = struct{}{}
		}
		intersect := make(map[uint]struct{}, len(candidate))
		for id := range candidate {
			if _, ok := explicit[id]; ok {
				intersect[id] = struct{}{}
			}
		}
		candidate = intersect
	}

	if len(candidate) == 0 {
		return &BusinessResolvedTarget{
			Groups: buildGroupRefs(target.GroupIDs, snap, groupCounts),
		}, nil
	}

	// ③ 查实例并按 filters 收敛（存在性同样在这一步收敛：不存在的 ID 自然被丢弃）
	ids := make([]uint, 0, len(candidate))
	for id := range candidate {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	q := s.db.Model(&model.Instance{}).Preload("Node").Where("instances.id IN ?", ids)
	if scope.Scoped {
		// 跨组隔离下沉为 SQL：越权实例自然被剔除，表现为「解析后为空」而非越权报错。
		q = q.Where("instances.id IN ?", scope.IDs)
	}
	if f := target.Filters; f != nil {
		if f.NodeID != nil {
			q = q.Where("instances.node_id = ?", *f.NodeID)
		}
		if f.Status != nil && strings.TrimSpace(*f.Status) != "" {
			q = q.Where("instances.status = ?", model.InstanceStatus(strings.TrimSpace(*f.Status)))
		}
		if f.Role != nil && strings.TrimSpace(*f.Role) != "" {
			q = q.Where("instances.role = ?", model.InstanceRole(strings.TrimSpace(*f.Role)))
		}
	}
	var instances []model.Instance
	if err := q.Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询目标实例失败: %w", err)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].ID < instances[j].ID })

	finalIDs := make([]uint, len(instances))
	for i := range instances {
		finalIDs[i] = instances[i].ID
	}
	return &BusinessResolvedTarget{
		InstanceIDs: finalIDs,
		Instances:   instances,
		Groups:      buildGroupRefs(target.GroupIDs, snap, groupCounts),
	}, nil
}

// buildGroupRefs 按请求顺序（去重）构造命中分组展示项；不存在的分组跳过。
func buildGroupRefs(groupIDs []uint, snap *groupTreeSnapshot, counts map[uint]int) []BusinessGroupRef {
	if len(groupIDs) == 0 || snap == nil {
		return nil
	}
	seen := map[uint]struct{}{}
	refs := make([]BusinessGroupRef, 0, len(groupIDs))
	for _, gid := range groupIDs {
		if _, dup := seen[gid]; dup {
			continue
		}
		seen[gid] = struct{}{}
		node, ok := snap.byID[gid]
		if !ok {
			continue
		}
		refs = append(refs, BusinessGroupRef{
			ID:            node.ID,
			Name:          node.Name,
			Path:          snap.path(node.ID),
			InstanceCount: counts[gid],
		})
	}
	return refs
}

// ErrBusinessTargetUnspecified 目标未指定任何维度（既无 groupIds 也无 instanceIds）。
var ErrBusinessTargetUnspecified = fmt.Errorf("未指定目标：需提供 groupIds 或 instanceIds")
