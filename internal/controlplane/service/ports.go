package service

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

// 端口分配范围（FR-032）。MC 默认 server-port=25565、rcon=25575。
// 共用同一个「已占用」集合，保证同节点上任一 TCP 端口号唯一。
const (
	serverPortBase = 25565
	rconPortBase   = 25575
	probePortBase  = 29940 // ServerProbe /metrics 端口起点（FR-010），避开 server/rcon 段
	portRangeSize  = 2000  // 每个起点向上探测的端口数
)

// AllocatedPorts 为新 MC 实例分配的端口集合。
type AllocatedPorts struct {
	ServerPort int
	RCONPort   int
	QueryPort  int
	ProbePort  int
}

// PortRanges 描述端口池的分配范围，用于前端展示与冲突预检。
type PortRanges struct {
	ServerPortBase int `json:"serverPortBase"`
	RCONPortBase   int `json:"rconPortBase"`
	RangeSize      int `json:"rangeSize"`
}

// DefaultPortRanges 返回当前端口池分配范围。
func DefaultPortRanges() PortRanges {
	return PortRanges{ServerPortBase: serverPortBase, RCONPortBase: rconPortBase, RangeSize: portRangeSize}
}

// NodePortsResult 是 GET /nodes/:id/ports 的响应体。
type NodePortsResult struct {
	NodeID   uint        `json:"nodeId"`
	Ranges   PortRanges  `json:"ranges"`
	Occupied []PortUsage `json:"occupied"`
}

// PortUsage 描述某节点上一个实例占用的端口集合。
type PortUsage struct {
	InstanceID uint               `json:"instanceId"`
	Name       string             `json:"name"`
	Role       model.InstanceRole `json:"role"`
	ServerPort int                `json:"serverPort"`
	RCONPort   int                `json:"rconPort"`
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
		if in.ServerPort == 0 && in.RCONPort == 0 && in.QueryPort == 0 && in.ProbePort == 0 {
			continue
		}
		usage = append(usage, PortUsage{
			InstanceID: in.ID,
			Name:       in.Name,
			Role:       in.Role,
			ServerPort: in.ServerPort,
			RCONPort:   in.RCONPort,
			QueryPort:  in.QueryPort,
			ProbePort:  in.ProbePort,
		})
	}
	return usage, nil
}

// allocPortsForNode 为节点上的新 MC 实例分配同节点唯一的 server / rcon 端口，
// query 端口约定与 server-port 一致（MC query 默认走 server-port，UDP 与 TCP 端口空间独立）。
// 在各自范围内取最低的、未被本节点其它实例占用的端口；已软删除的实例不计入占用。
func allocPortsForNode(db *gorm.DB, nodeID uint) (AllocatedPorts, error) {
	var instances []model.Instance
	if err := db.Where("node_id = ?", nodeID).Find(&instances).Error; err != nil {
		return AllocatedPorts{}, fmt.Errorf("查询节点实例端口失败: %w", err)
	}

	used := make(map[int]bool)
	for _, in := range instances {
		for _, p := range []int{in.ServerPort, in.RCONPort, in.QueryPort, in.ProbePort} {
			if p > 0 {
				used[p] = true
			}
		}
	}

	pick := func(base int) (int, error) {
		for p := base; p < base+portRangeSize; p++ {
			if !used[p] {
				used[p] = true // 防止本次分配内的多个端口相互撞号
				return p, nil
			}
		}
		return 0, fmt.Errorf("端口范围 [%d,%d) 已耗尽", base, base+portRangeSize)
	}

	server, err := pick(serverPortBase)
	if err != nil {
		return AllocatedPorts{}, err
	}
	rcon, err := pick(rconPortBase)
	if err != nil {
		return AllocatedPorts{}, err
	}
	probe, err := pick(probePortBase)
	if err != nil {
		return AllocatedPorts{}, err
	}
	return AllocatedPorts{ServerPort: server, RCONPort: rcon, QueryPort: server, ProbePort: probe}, nil
}
