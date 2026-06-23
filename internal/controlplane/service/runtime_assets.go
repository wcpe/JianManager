package service

import (
	"fmt"
	"sort"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// RuntimeAssetsService 是「运行时与制品全局页」（FR-082）的只读聚合服务。
//
// 它不引入新的存储或 proto，只跨现有表（nodes / node_jdks / instances / assets）聚合出
// 「按实例区分的引用关系」与可视化所需的占用/去重/冷热统计：
//   - JDK 引用关系可由 instances.jdk_id（直接绑定）与 instances.java_major_version（大版本绑定）
//     真实推导——这是 FR-033 既有的绑定语义；
//   - 制品（assets）当前不持久化「实例↔制品」连接（FR-045 消费侧引用计数为占位，见 ADR-011），
//     故制品区按类型聚合占用/去重/冷热 + 暴露既有 ref_count，引用明细以诚实的「按类型」粒度给出，
//     不臆造不存在的实例连接。
//
// 删除受引用项的拒绝与占用方提示复用 FR-033（JDKService.Delete）/ FR-045（AssetService.Delete），
// 本服务只负责「展示」引用关系，不重复实现删除逻辑。
type RuntimeAssetsService struct {
	db *gorm.DB
}

// NewRuntimeAssetsService 创建运行时与制品聚合服务。
func NewRuntimeAssetsService(db *gorm.DB) *RuntimeAssetsService {
	return &RuntimeAssetsService{db: db}
}

// RuntimeAssetsOverview 全局页一次性聚合载荷。
type RuntimeAssetsOverview struct {
	// JDKs 跨节点 JDK 矩阵（每项含其引用实例清单）。
	JDKs []JDKMatrixItem `json:"jdks"`
	// JDKSummary JDK 区汇总（节点数 / JDK 总数 / 被引用数 / 实例引用数）。
	JDKSummary JDKSummary `json:"jdkSummary"`
	// Assets 制品按类型分组（每组含占用/去重/冷热统计）。
	Assets []AssetTypeGroup `json:"assets"`
	// AssetSummary 制品区汇总（资产总数 / 总占用 / 去重省下 / 被引用数）。
	AssetSummary AssetSummary `json:"assetSummary"`
}

// JDKRefInstance 引用某 JDK 的实例（引用关系下钻 / 删除占用方提示）。
type JDKRefInstance struct {
	ID     uint                 `json:"id"`
	UUID   string               `json:"uuid"`
	Name   string               `json:"name"`
	Status model.InstanceStatus `json:"status"`
	// Binding 绑定方式：direct=按具体 JDK（jdk_id）；major=按 Java 大版本（java_major_version）解析到本 JDK。
	Binding string `json:"binding"`
}

// JDKMatrixItem 跨节点 JDK 矩阵的一项 = 一个节点上的一个 JDK + 其引用实例。
type JDKMatrixItem struct {
	ID           uint   `json:"id"`
	NodeID       uint   `json:"nodeId"`
	NodeName     string `json:"nodeName"`
	NodeOnline   bool   `json:"nodeOnline"`
	Vendor       string `json:"vendor"`
	MajorVersion int    `json:"majorVersion"`
	Version      string `json:"version"`
	Arch         string `json:"arch"`
	Path         string `json:"path"`
	Managed      bool   `json:"managed"`
	// Instances 引用本 JDK 的实例（直接绑定 + 大版本解析命中）。
	Instances []JDKRefInstance `json:"instances"`
	// RefCount 引用实例数（= len(Instances)），便于前端排序/冷热标记。
	RefCount int `json:"refCount"`
}

// JDKSummary JDK 区汇总统计。
type JDKSummary struct {
	NodeCount     int `json:"nodeCount"`
	JDKCount      int `json:"jdkCount"`
	ReferencedJDK int `json:"referencedJdk"` // ref_count>0 的 JDK 数
	InstanceRefs  int `json:"instanceRefs"`  // 解析出的「实例→JDK」引用边总数
}

// AssetTypeGroup 制品按类型分组（core/plugin/image/...）。
type AssetTypeGroup struct {
	Type model.AssetType `json:"type"`
	// Items 该类型下的资产（已含 ref_count / 冷热 / 占用），按 id desc。
	Items []model.Asset `json:"items"`
	// Count 资产数。
	Count int `json:"count"`
	// TotalSize 该类型资产物理占用字节合计（去重后，每条记录即一份物理）。
	TotalSize int64 `json:"totalSize"`
	// ReferencedCount ref_count>0 的资产数。
	ReferencedCount int `json:"referencedCount"`
	// HotCount / ArchivedCount / ExternalCount 冷热分布。
	HotCount      int `json:"hotCount"`
	ArchivedCount int `json:"archivedCount"`
	ExternalCount int `json:"externalCount"`
}

// AssetSummary 制品区汇总统计。
type AssetSummary struct {
	AssetCount      int   `json:"assetCount"`
	TotalSize       int64 `json:"totalSize"`
	ReferencedCount int   `json:"referencedCount"`
	HotCount        int   `json:"hotCount"`
	ArchivedCount   int   `json:"archivedCount"`
	ExternalCount   int   `json:"externalCount"`
}

// Overview 加载现有表并聚合出全局页载荷。纯聚合逻辑下沉到 buildJDKMatrix / groupAssetsByType，便于单测。
func (s *RuntimeAssetsService) Overview() (*RuntimeAssetsOverview, error) {
	var nodes []model.Node
	if err := s.db.Order("id asc").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	var jdks []model.NodeJDK
	if err := s.db.Order("major_version desc, id desc").Find(&jdks).Error; err != nil {
		return nil, fmt.Errorf("查询 JDK 失败: %w", err)
	}
	// 仅取聚合所需字段，避免拉全实例（含敏感列）。
	var instances []model.Instance
	if err := s.db.Select("id", "uuid", "name", "status", "node_id", "jdk_id", "java_major_version").
		Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}
	var assets []model.Asset
	if err := s.db.Order("id desc").Find(&assets).Error; err != nil {
		return nil, fmt.Errorf("查询资产失败: %w", err)
	}

	matrix, jdkSummary := buildJDKMatrix(nodes, jdks, instances)
	groups, assetSummary := groupAssetsByType(assets)

	return &RuntimeAssetsOverview{
		JDKs:         matrix,
		JDKSummary:   jdkSummary,
		Assets:       groups,
		AssetSummary: assetSummary,
	}, nil
}

// buildJDKMatrix 由节点 / JDK / 实例推导跨节点 JDK 矩阵 + 每项的引用实例清单（纯函数）。
//
// 引用解析规则（与 FR-033 绑定语义一致）：
//   - 直接绑定：instance.jdk_id == jdk.id 且同节点 → binding=direct；
//   - 大版本绑定：instance.jdk_id==0 且 instance.java_major_version==jdk.major_version 且同节点，
//     解析到「同节点同大版本中 id 最大」的那一个 JDK（与 JDKService.ResolveForInstance 的 `id desc` 选择一致），
//     binding=major。
//
// 这样矩阵里每个 JDK 的 instances 即为「真实会用到它」的实例集合，可直接用于引用关系可视化与删除占用方提示。
func buildJDKMatrix(nodes []model.Node, jdks []model.NodeJDK, instances []model.Instance) ([]JDKMatrixItem, JDKSummary) {
	nodeByID := make(map[uint]model.Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}

	// 预解析：每个节点上「大版本 → 解析命中的 jdk_id」（同节点同大版本取 id 最大）。
	resolvedMajor := resolveMajorBinding(jdks)

	// jdkID → 引用实例。
	refs := make(map[uint][]JDKRefInstance, len(jdks))
	instanceRefs := 0
	for _, inst := range instances {
		var targetJDK uint
		binding := ""
		switch {
		case inst.JDKID != 0:
			targetJDK = inst.JDKID
			binding = "direct"
		case inst.JavaMajorVersion != 0:
			if jid, ok := resolvedMajor[majorKey{nodeID: inst.NodeID, major: inst.JavaMajorVersion}]; ok {
				targetJDK = jid
				binding = "major"
			}
		}
		if targetJDK == 0 {
			continue
		}
		refs[targetJDK] = append(refs[targetJDK], JDKRefInstance{
			ID:     inst.ID,
			UUID:   inst.UUID,
			Name:   inst.Name,
			Status: inst.Status,
			Binding: binding,
		})
		instanceRefs++
	}

	items := make([]JDKMatrixItem, 0, len(jdks))
	referenced := 0
	for _, j := range jdks {
		node := nodeByID[j.NodeID]
		insts := refs[j.ID]
		// 稳定排序：先按实例名再按 id，便于 UI 与测试确定。
		sort.Slice(insts, func(a, b int) bool {
			if insts[a].Name != insts[b].Name {
				return insts[a].Name < insts[b].Name
			}
			return insts[a].ID < insts[b].ID
		})
		if len(insts) > 0 {
			referenced++
		}
		items = append(items, JDKMatrixItem{
			ID:           j.ID,
			NodeID:       j.NodeID,
			NodeName:     node.Name,
			NodeOnline:   node.Status == model.NodeStatusOnline,
			Vendor:       j.Vendor,
			MajorVersion: j.MajorVersion,
			Version:      j.Version,
			Arch:         j.Arch,
			Path:         j.Path,
			Managed:      j.Managed,
			Instances:    insts,
			RefCount:     len(insts),
		})
	}

	summary := JDKSummary{
		NodeCount:     len(nodes),
		JDKCount:      len(jdks),
		ReferencedJDK: referenced,
		InstanceRefs:  instanceRefs,
	}
	return items, summary
}

// majorKey 是 (节点, 大版本) 解析键。
type majorKey struct {
	nodeID uint
	major  int
}

// resolveMajorBinding 计算每个 (节点, 大版本) 解析命中的 jdk_id（取 id 最大，与 ResolveForInstance 一致）。
func resolveMajorBinding(jdks []model.NodeJDK) map[majorKey]uint {
	out := make(map[majorKey]uint)
	for _, j := range jdks {
		k := majorKey{nodeID: j.NodeID, major: j.MajorVersion}
		if cur, ok := out[k]; !ok || j.ID > cur {
			out[k] = j.ID
		}
	}
	return out
}

// groupAssetsByType 把资产按类型分组并算占用/去重/冷热统计（纯函数）。
// 分组按类型名升序；组内 items 保持传入顺序（调用方已 id desc）。
func groupAssetsByType(assets []model.Asset) ([]AssetTypeGroup, AssetSummary) {
	byType := make(map[model.AssetType]*AssetTypeGroup)
	var summary AssetSummary
	for i := range assets {
		a := assets[i]
		g := byType[a.Type]
		if g == nil {
			g = &AssetTypeGroup{Type: a.Type}
			byType[a.Type] = g
		}
		g.Items = append(g.Items, a)
		g.Count++
		g.TotalSize += a.Size
		summary.AssetCount++
		summary.TotalSize += a.Size
		if a.RefCount > 0 {
			g.ReferencedCount++
			summary.ReferencedCount++
		}
		switch a.StorageState {
		case model.AssetStorageArchived:
			g.ArchivedCount++
			summary.ArchivedCount++
		case model.AssetStorageExternal:
			g.ExternalCount++
			summary.ExternalCount++
		default:
			g.HotCount++
			summary.HotCount++
		}
	}

	groups := make([]AssetTypeGroup, 0, len(byType))
	for _, g := range byType {
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(a, b int) bool { return groups[a].Type < groups[b].Type })
	return groups, summary
}
