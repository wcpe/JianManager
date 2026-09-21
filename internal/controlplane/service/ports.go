package service

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 端口分配范围（FR-032）。MC 默认 server-port=25565。RCON 已退役（FR-067，见 ADR-016）：
// 不再分配 rcon 端口；治理改走 ServerProbe 探针。共用同一个「已占用」集合，保证同节点上任一 TCP 端口号唯一。
const (
	serverPortBase = 25565
	probePortBase  = 29940 // ServerProbe /metrics 端口起点（FR-010），避开 server 段
	portRangeSize  = 2000  // 每个起点向上探测的端口数
)

// AllocatedPorts 为新 MC 实例分配的端口集合。
type AllocatedPorts struct {
	ServerPort int
	QueryPort  int
	ProbePort  int
}

// PortRanges 描述端口池的分配范围，用于前端展示与冲突预检。
type PortRanges struct {
	ServerPortBase int `json:"serverPortBase"`
	RangeSize      int `json:"rangeSize"`
}

// DefaultPortRanges 返回当前端口池分配范围。
func DefaultPortRanges() PortRanges {
	return PortRanges{ServerPortBase: serverPortBase, RangeSize: portRangeSize}
}

// NodePortsResult 是 GET /nodes/:id/ports 的响应体。
type NodePortsResult struct {
	NodeID   uint        `json:"nodeId"`
	Ranges   PortRanges  `json:"ranges"`
	Occupied []PortUsage `json:"occupied"`
}

// PortUsage 描述某节点上一个实例占用的端口集合（RCON 已退役，FR-067）。
type PortUsage struct {
	InstanceID uint               `json:"instanceId"`
	Name       string             `json:"name"`
	Role       model.InstanceRole `json:"role"`
	ServerPort int                `json:"serverPort"`
	QueryPort  int                `json:"queryPort"`
	ProbePort  int                `json:"probePort"`
}

// NodePortUsage 返回某节点上各实例的端口占用（系统分配端口的可视化，FR-032）。
// 仅列出至少占用一个端口的实例；已软删除的实例不计入。
func NodePortUsage(db *gorm.DB, nodeID uint) ([]PortUsage, error) {
	var instances []model.Instance
	if err := db.Where("node_id = ?", nodeID).Order("server_port asc").Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询节点端口占用失败: %w", err)
	}
	usage := make([]PortUsage, 0, len(instances))
	for _, in := range instances {
		if in.ServerPort == 0 && in.QueryPort == 0 && in.ProbePort == 0 {
			continue
		}
		usage = append(usage, PortUsage{
			InstanceID: in.ID,
			Name:       in.Name,
			Role:       in.Role,
			ServerPort: in.ServerPort,
			QueryPort:  in.QueryPort,
			ProbePort:  in.ProbePort,
		})
	}
	return usage, nil
}

// allocPortsForNode 为节点上的新实例分配同节点唯一的 server 端口，
// query 端口约定与 server-port 一致（MC query 默认走 server-port，UDP 与 TCP 端口空间独立）。
// 在各自范围内取最低的、未被本节点其它实例占用的端口；已软删除的实例不计入占用。
// RCON 已退役（FR-067）：不再分配 rcon 端口，但仍把历史实例残留的 rcon 端口计入占用集合避免撞号。
//
// probeApplicable 为 false 时不分配探针端口（FR-454：ServerProbe 是 Bukkit 插件，代理/通用二进制/
// Beacon 均无法加载），返回的 ProbePort 为 0；server/query 仍照常分配（binary/beacon 的通用端口约定）。
func allocPortsForNode(db *gorm.DB, nodeID uint, probeApplicable bool) (AllocatedPorts, error) {
	used, err := occupiedPortsForNode(db, nodeID)
	if err != nil {
		return AllocatedPorts{}, err
	}

	server, err := pickPort(used, serverPortBase)
	if err != nil {
		return AllocatedPorts{}, err
	}
	out := AllocatedPorts{ServerPort: server, QueryPort: server}
	if !probeApplicable {
		return out, nil
	}
	probe, err := pickPort(used, probePortBase)
	if err != nil {
		return AllocatedPorts{}, err
	}
	out.ProbePort = probe
	return out, nil
}

// allocProbePortForNode 只为节点分配一个空闲探针端口（FR-411 补口）。
// 导入实例（FR-302）与历史实例不经过 allocPortsForNode，probe_port 保持 0——这类实例
// 安装探针时写出的 config 是 port: 0（探针绑到 OS 随机端口），Worker 侧又按 ProbePort>0
// 过滤采集，形成「探针桥已连接但监控永无数据」的静默断链。部署探针前经此函数补口。
//
// 并发安全：本函数只做「占用快照 + 选口」，快照是无事务的——跨实例并发互斥由调用方
// 持 InstanceService.nodePortAllocMu 保证（EnsureProbePort 的补口、建服/克隆/代理的
// allocPortsForNode 均从快照到端口落库全程持锁）。边界：进程内互斥，生产单 CP 实例
// 前提下成立；多 CP 实例部署需 DB 层唯一约束 + 冲突重试，暂超范围。
func allocProbePortForNode(db *gorm.DB, nodeID uint) (int, error) {
	used, err := occupiedPortsForNode(db, nodeID)
	if err != nil {
		return 0, err
	}
	return pickPort(used, probePortBase)
}

// occupiedPortsForNode 汇总某节点已占用端口集合（server/rcon 残留/query/probe），已软删除实例不计入。
func occupiedPortsForNode(db *gorm.DB, nodeID uint) (map[int]bool, error) {
	var instances []model.Instance
	if err := db.Where("node_id = ?", nodeID).Find(&instances).Error; err != nil {
		return nil, fmt.Errorf("查询节点实例端口失败: %w", err)
	}

	used := make(map[int]bool)
	for _, in := range instances {
		// in.RCONPort 为历史实例残留（新实例不再分配）：仍计入占用以免新端口撞上旧 rcon 端口。
		//nolint:staticcheck // 为避免新分配端口撞上历史 RCON 端口，仍须读取兼容字段。
		for _, p := range []int{in.ServerPort, in.RCONPort, in.QueryPort, in.ProbePort} {
			if p > 0 {
				used[p] = true
			}
		}
	}
	return used, nil
}

// pickPort 在 [base, base+portRangeSize) 内取未被占用的最低端口；已取端口写入 used 防同批撞号。
func pickPort(used map[int]bool, base int) (int, error) {
	for p := base; p < base+portRangeSize; p++ {
		if !used[p] {
			used[p] = true
			return p, nil
		}
	}
	return 0, fmt.Errorf("端口范围 [%d,%d) 已耗尽", base, base+portRangeSize)
}
