package service

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ErrRepairNotConfirmed 破坏性修复操作未二次确认（confirm=false）。参见 ADR-039 §2、FR-059。
var ErrRepairNotConfirmed = errors.New("破坏性操作需二次确认（confirm=true）")

// dedupNameSuffixRe 匹配迁移去重时给重名活跃节点追加的 "-dup-<id>" 后缀（见 database.dedupeActiveNodeNames）。
// 命中即说明该节点曾因重名（BUG-A 的典型遗留）被自动改名，是坏节点的强信号。
var dedupNameSuffixRe = regexp.MustCompile(`-dup-\d+$`)

// NodeRepairService 坏节点检测与修复（见 ADR-039 §2，修复 BUG-A 污染的存量数据）。
//
// 历史上 Register 按 name 锚定身份，另一台机器用同名注册会覆写旧节点的 host/身份，
// 致旧节点上按 node_id 外键挂的 JDK/实例错误路由。本服务提供：
//   - 检测：标出疑似被串改/重名的节点（只读诊断）。
//   - 重新 enroll：为被挤占的机器轮换全新 UUID/secret，切断与被冒用旧身份的关联。
//   - 孤儿清理：清理/失效该节点上孤立的 JDK 与实例引用。
//
// 重新 enroll 与孤儿清理均为破坏性操作，要求 confirm=true（二次确认），审计由调用方记录（FR-059/FR-015）。
type NodeRepairService struct {
	db *gorm.DB
}

// NewNodeRepairService 创建坏节点修复服务。
func NewNodeRepairService(db *gorm.DB) *NodeRepairService {
	return &NodeRepairService{db: db}
}

// SuspectNode 一条疑似坏节点诊断记录。
type SuspectNode struct {
	Node    model.Node `json:"node"`
	Reasons []string   `json:"reasons"`
}

// ListSuspects 扫描节点表，返回疑似被串改/重名的活跃节点（只读诊断，见 ADR-039 §2）。
// 信号：① 名字带迁移去重后缀 "-dup-<id>"（曾因重名被自动改名）；② 仍存在的同名活跃节点组
// （历史覆写或并发注册遗留）。无可用「心跳来源 host」历史，故以名字冲突这一确定信号为准。
func (s *NodeRepairService) ListSuspects() ([]SuspectNode, error) {
	var nodes []model.Node
	if err := s.db.Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}

	// 统计活跃节点名出现次数，识别仍存在的重名组。
	nameCount := make(map[string]int, len(nodes))
	for _, n := range nodes {
		nameCount[n.Name]++
	}

	suspects := make([]SuspectNode, 0)
	for _, n := range nodes {
		var reasons []string
		if dedupNameSuffixRe.MatchString(n.Name) {
			reasons = append(reasons, "节点名带迁移去重后缀（曾因重名被自动改名，疑似 BUG-A 遗留）")
		}
		if nameCount[n.Name] > 1 {
			reasons = append(reasons, "存在同名活跃节点（历史覆写或并发注册遗留）")
		}
		if len(reasons) > 0 {
			suspects = append(suspects, SuspectNode{Node: n, Reasons: reasons})
		}
	}
	return suspects, nil
}

// ReenrollResult 重新 enroll 结果，返回轮换后的新身份（new secret 仅此一次随响应返回）。
type ReenrollResult struct {
	NodeID     uint   `json:"nodeId"`
	NewUUID    string `json:"newUuid"`
	NewSecret  string `json:"newSecret"`
	OldUUID    string `json:"oldUuid"`
}

// Reenroll 把被挤占的机器作为新节点重新 enroll（见 ADR-039 §2）：为该节点行轮换全新 UUID + secret，
// 切断其与被冒用旧身份的关联（旧 Worker/冒用者持的旧 secret 即刻失效）。
// 节点上按 node_id（整型主键，非 UUID）外键挂的 JDK/实例随行保留，不受 UUID 轮换影响。
// confirm 必须为 true（破坏性，二次确认）。
func (s *NodeRepairService) Reenroll(nodeID uint, confirm bool) (*ReenrollResult, error) {
	if !confirm {
		return nil, ErrRepairNotConfirmed
	}
	var node model.Node
	if err := s.db.First(&node, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}

	oldUUID := node.UUID
	newUUID := uuid.New().String()
	newSecret, err := generateSecret()
	if err != nil {
		return nil, fmt.Errorf("生成节点密钥失败: %w", err)
	}
	if err := s.db.Model(&model.Node{}).Where("id = ?", nodeID).Updates(map[string]any{
		"uuid":   newUUID,
		"secret": newSecret,
		"status": model.NodeStatusOffline, // 旧连接已失效，待新身份重注册上线
	}).Error; err != nil {
		return nil, fmt.Errorf("轮换节点身份失败: %w", err)
	}
	return &ReenrollResult{NodeID: nodeID, NewUUID: newUUID, NewSecret: newSecret, OldUUID: oldUUID}, nil
}

// OrphanReport 某节点上孤立资源引用的统计（只读，见 ADR-039 §2）。
type OrphanReport struct {
	NodeID       uint  `json:"nodeId"`
	JDKCount     int64 `json:"jdkCount"`
	InstanceCount int64 `json:"instanceCount"`
}

// OrphanReport 统计指定节点上的 JDK 与实例数量，供修复前评估影响面（只读）。
func (s *NodeRepairService) OrphanReport(nodeID uint) (*OrphanReport, error) {
	var node model.Node
	if err := s.db.First(&node, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	rep := &OrphanReport{NodeID: nodeID}
	if err := s.db.Model(&model.NodeJDK{}).Where("node_id = ?", nodeID).Count(&rep.JDKCount).Error; err != nil {
		return nil, fmt.Errorf("统计节点 JDK 失败: %w", err)
	}
	if err := s.db.Model(&model.Instance{}).Where("node_id = ?", nodeID).Count(&rep.InstanceCount).Error; err != nil {
		return nil, fmt.Errorf("统计节点实例失败: %w", err)
	}
	return rep, nil
}

// PurgeOrphansResult 孤儿清理结果。
type PurgeOrphansResult struct {
	NodeID          uint  `json:"nodeId"`
	JDKDeleted      int64 `json:"jdkDeleted"`
	InstancesPurged int64 `json:"instancesPurged"`
}

// PurgeOrphans 清理指定节点上孤立的 JDK 与实例引用（见 ADR-039 §2）：
// 硬删该节点的 NodeJDK 行（卸不掉的 JDK 登记），软删该节点的实例（解除错误路由的实例归属）。
// 用于「被挤占机器重新 enroll 后」清掉冒用期间错误挂到该节点的残留资源。
// confirm 必须为 true（破坏性，二次确认）。
func (s *NodeRepairService) PurgeOrphans(nodeID uint, confirm bool) (*PurgeOrphansResult, error) {
	if !confirm {
		return nil, ErrRepairNotConfirmed
	}
	var node model.Node
	if err := s.db.First(&node, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}

	res := &PurgeOrphansResult{NodeID: nodeID}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		jdk := tx.Where("node_id = ?", nodeID).Delete(&model.NodeJDK{})
		if jdk.Error != nil {
			return fmt.Errorf("清理节点 JDK 失败: %w", jdk.Error)
		}
		res.JDKDeleted = jdk.RowsAffected

		inst := tx.Where("node_id = ?", nodeID).Delete(&model.Instance{})
		if inst.Error != nil {
			return fmt.Errorf("清理节点实例失败: %w", inst.Error)
		}
		res.InstancesPurged = inst.RowsAffected
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
