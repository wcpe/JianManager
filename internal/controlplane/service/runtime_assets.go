package service

import (
	"fmt"
	"sort"
	"time"

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
	// sync 单节点库存强制同步器（FR-301 手动刷新）。main 装配阶段经 SetJDKSync 注入
	// JDKService；未注入时 Refresh 报错（Overview 只读聚合不受影响）。
	sync RuntimeSyncer
}

// RuntimeSyncer 单节点运行时库存强制同步（FR-301）。生产实现为 JDKService.SyncFromWorker
// （node_runtimes 无 Worker 侧清单可同步：外部登记真相源即 CP 库，托管 Node 随 FR-299 再议）；
// 接口化便于单测注入失败场景。
type RuntimeSyncer interface {
	SyncFromWorker(nodeID uint) error
}

// NewRuntimeAssetsService 创建运行时与制品聚合服务。
func NewRuntimeAssetsService(db *gorm.DB) *RuntimeAssetsService {
	return &RuntimeAssetsService{db: db}
}

// SetJDKSync 注入库存同步器（FR-301 手动刷新）。main 装配阶段调用。
func (s *RuntimeAssetsService) SetJDKSync(sync RuntimeSyncer) {
	s.sync = sync
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
	// Runtimes 跨节点多运行时矩阵（FR-301 加性扩展）：node_jdks(type=jdk，含引用实例)
	// 与 node_runtimes（nodejs / python 预留）读侧拼装；不改 JDKs 等老字段。
	Runtimes []RuntimeMatrixItem `json:"runtimes"`
	// RuntimeSyncs 每节点上次库存同步状态（FR-301）：SyncedAt=nil 表示从未同步。
	RuntimeSyncs []RuntimeNodeSync `json:"runtimeSyncs"`
	// SyncedAt 整体上次同步时间 = 各节点 runtime_synced_at 的最大值（FR-301）；nil=全部未同步。
	SyncedAt *time.Time `json:"syncedAt"`
	// ArtifactChannels 制品存储渠道引用（FR-349 加性扩展）：前端把资产行的
	// storageChannelId 映射为渠道名（「存储位置」列），不回凭证任何信息。
	ArtifactChannels []ArtifactChannelRef `json:"artifactChannels"`
}

// ArtifactChannelRef 制品存储渠道的最小引用（id/名称/类型），供列表映射展示（FR-349）。
type ArtifactChannelRef struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// RuntimeMatrixItem 跨节点多运行时矩阵的一项（FR-301）：一个节点上的一个运行时。
// type=jdk 行来自 node_jdks（Name=厂商，Instances 语义同 JDKMatrixItem）；
// 其它类型来自 node_runtimes——当前无「实例↔非 JDK 运行时」的引用消费者，
// Instances 恒空 / RefCount 恒 0（诚实留空，不臆造连接）。
type RuntimeMatrixItem struct {
	ID           uint             `json:"id"`
	NodeID       uint             `json:"nodeId"`
	NodeName     string           `json:"nodeName"`
	NodeOnline   bool             `json:"nodeOnline"`
	Type         string           `json:"type"`
	Name         string           `json:"name"`
	MajorVersion int              `json:"majorVersion"`
	Version      string           `json:"version"`
	Arch         string           `json:"arch"`
	Path         string           `json:"path"`
	Managed      bool             `json:"managed"`
	Instances    []JDKRefInstance `json:"instances"`
	RefCount     int              `json:"refCount"`
}

// RuntimeNodeSync 一个节点的库存同步状态（FR-301）。
type RuntimeNodeSync struct {
	NodeID   uint       `json:"nodeId"`
	NodeName string     `json:"nodeName"`
	Online   bool       `json:"online"`
	SyncedAt *time.Time `json:"syncedAt"`
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
	// LostCount 失效资产数（FR-349：索引在、外置对象缺失）。
	LostCount int `json:"lostCount"`
}

// AssetSummary 制品区汇总统计。
type AssetSummary struct {
	AssetCount      int   `json:"assetCount"`
	TotalSize       int64 `json:"totalSize"`
	ReferencedCount int   `json:"referencedCount"`
	HotCount        int   `json:"hotCount"`
	ArchivedCount   int   `json:"archivedCount"`
	ExternalCount   int   `json:"externalCount"`
	// LostCount 失效资产数（FR-349）。
	LostCount int `json:"lostCount"`
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
	// 非 JDK 运行时（FR-301）：与 node_runtimes 的统一视图排序一致（type 升序 → major 降序）。
	var runtimes []model.NodeRuntime
	if err := s.db.Order("type asc, major desc, id desc").Find(&runtimes).Error; err != nil {
		return nil, fmt.Errorf("查询运行时失败: %w", err)
	}
	// 制品存储渠道最小引用（FR-349）：仅 id/name/type，供「存储位置」列映射，不带凭证信息。
	var channels []model.ArtifactStorageChannel
	if err := s.db.Select("id", "name", "type").Order("id asc").Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("查询制品存储渠道失败: %w", err)
	}
	channelRefs := make([]ArtifactChannelRef, 0, len(channels))
	for _, ch := range channels {
		channelRefs = append(channelRefs, ArtifactChannelRef{ID: ch.ID, Name: ch.Name, Type: string(ch.Type)})
	}

	matrix, jdkSummary := buildJDKMatrix(nodes, jdks, instances)
	groups, assetSummary := groupAssetsByType(assets)
	syncs, syncedAt := buildRuntimeSyncs(nodes)

	return &RuntimeAssetsOverview{
		JDKs:             matrix,
		JDKSummary:       jdkSummary,
		Assets:           groups,
		AssetSummary:     assetSummary,
		Runtimes:         buildRuntimeMatrix(nodes, matrix, runtimes),
		RuntimeSyncs:     syncs,
		SyncedAt:         syncedAt,
		ArtifactChannels: channelRefs,
	}, nil
}

// buildRuntimeMatrix 拼装跨节点多运行时矩阵（纯函数，FR-301）：
// jdk 行直接由既有 JDK 矩阵映射（保留引用实例），其后追加 node_runtimes 行（引用恒空）。
func buildRuntimeMatrix(nodes []model.Node, jdkItems []JDKMatrixItem, runtimes []model.NodeRuntime) []RuntimeMatrixItem {
	nodeByID := make(map[uint]model.Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}

	items := make([]RuntimeMatrixItem, 0, len(jdkItems)+len(runtimes))
	for _, j := range jdkItems {
		items = append(items, RuntimeMatrixItem{
			ID:           j.ID,
			NodeID:       j.NodeID,
			NodeName:     j.NodeName,
			NodeOnline:   j.NodeOnline,
			Type:         "jdk",
			Name:         j.Vendor,
			MajorVersion: j.MajorVersion,
			Version:      j.Version,
			Arch:         j.Arch,
			Path:         j.Path,
			Managed:      j.Managed,
			Instances:    j.Instances,
			RefCount:     j.RefCount,
		})
	}
	for _, rt := range runtimes {
		node := nodeByID[rt.NodeID]
		items = append(items, RuntimeMatrixItem{
			ID:           rt.ID,
			NodeID:       rt.NodeID,
			NodeName:     node.Name,
			NodeOnline:   node.Status == model.NodeStatusOnline,
			Type:         rt.Type,
			Name:         rt.Name,
			MajorVersion: rt.Major,
			Version:      rt.Version,
			Arch:         rt.Arch,
			Path:         rt.Path,
			Managed:      rt.Managed,
			Instances:    []JDKRefInstance{},
			RefCount:     0,
		})
	}
	return items
}

// buildRuntimeSyncs 由节点行导出每节点同步状态与整体 syncedAt（= 各节点最大值，纯函数）。
func buildRuntimeSyncs(nodes []model.Node) ([]RuntimeNodeSync, *time.Time) {
	syncs := make([]RuntimeNodeSync, 0, len(nodes))
	var latest *time.Time
	for _, n := range nodes {
		syncs = append(syncs, RuntimeNodeSync{
			NodeID:   n.ID,
			NodeName: n.Name,
			Online:   n.Status == model.NodeStatusOnline,
			SyncedAt: n.RuntimeSyncedAt,
		})
		if n.RuntimeSyncedAt != nil && (latest == nil || n.RuntimeSyncedAt.After(*latest)) {
			latest = n.RuntimeSyncedAt
		}
	}
	return syncs, latest
}

// RuntimeRefreshResult 单节点强制同步结果（FR-301）。失败时 SyncedAt 保留旧时间戳
// （nil = 从未同步过），供前端「显旧数据 + 提示」。
type RuntimeRefreshResult struct {
	NodeID   uint       `json:"nodeId"`
	NodeName string     `json:"nodeName"`
	OK       bool       `json:"ok"`
	Error    string     `json:"error,omitempty"`
	SyncedAt *time.Time `json:"syncedAt"`
}

// RuntimeRefreshOutcome POST /runtime-assets/refresh 载荷（FR-301）。
type RuntimeRefreshOutcome struct {
	Results  []RuntimeRefreshResult `json:"results"`
	SyncedAt *time.Time             `json:"syncedAt"`
}

// Refresh 强制全节点库存同步（FR-301 手动刷新）：逐节点 syncFromWorker，
// 单节点失败不阻断整体（失败容忍：结果逐节点回报 ok/error，DB 旧数据保留供显示）。
func (s *RuntimeAssetsService) Refresh() (*RuntimeRefreshOutcome, error) {
	if s.sync == nil {
		return nil, fmt.Errorf("运行时同步器未装配")
	}
	var nodes []model.Node
	if err := s.db.Order("id asc").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}

	out := &RuntimeRefreshOutcome{Results: make([]RuntimeRefreshResult, 0, len(nodes))}
	for _, n := range nodes {
		res := RuntimeRefreshResult{NodeID: n.ID, NodeName: n.Name}
		if err := s.sync.SyncFromWorker(n.ID); err != nil {
			res.Error = err.Error()
			res.SyncedAt = n.RuntimeSyncedAt // 失败：保留旧时间戳，前端显旧数据
		} else {
			res.OK = true
			// 成功路径 syncFromWorker 已更新 runtime_synced_at，重读取最新值。
			var refreshed model.Node
			if err := s.db.Select("runtime_synced_at").First(&refreshed, n.ID).Error; err == nil {
				res.SyncedAt = refreshed.RuntimeSyncedAt
			}
		}
		if res.SyncedAt != nil && (out.SyncedAt == nil || res.SyncedAt.After(*out.SyncedAt)) {
			out.SyncedAt = res.SyncedAt
		}
		out.Results = append(out.Results, res)
	}
	return out, nil
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
			ID:      inst.ID,
			UUID:    inst.UUID,
			Name:    inst.Name,
			Status:  inst.Status,
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
		case model.AssetStorageLost:
			// 失效（FR-349）：单列统计，不混入 hot/external 三态。
			g.LostCount++
			summary.LostCount++
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
