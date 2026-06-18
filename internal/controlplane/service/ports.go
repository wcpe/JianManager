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
	portRangeSize  = 2000 // 每个起点向上探测的端口数
)

// AllocatedPorts 为新 MC 实例分配的端口集合。
type AllocatedPorts struct {
	ServerPort int
	RCONPort   int
	QueryPort  int
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
		for _, p := range []int{in.ServerPort, in.RCONPort, in.QueryPort} {
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
	return AllocatedPorts{ServerPort: server, RCONPort: rcon, QueryPort: server}, nil
}
