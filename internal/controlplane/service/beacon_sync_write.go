package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// Beacon 拓扑映射的事务内写入段（FR-444 §4.4）。
//
// 本文件所有函数都在 BeaconSyncService.apply 的同一事务内调用：任一函数返回错误，
// 整个事务回滚——既保证「拉取失败/数据不合法 → 本地零改动」，也保证失败不留半棵树。

// ensurePlanGroups 在事务内确保计划中的分组节点全部存在（父先于子），返回 key → 分组 ID。
//
// 冲突处理（FR-444 §4.3）：同名同父的既有节点直接复用，不重复创建。
// 返回本轮实际新建的节点数。
func ensurePlanGroups(tx *gorm.DB, groups *InstanceGroupService, p *beaconPlan, resolved map[string]uint) (map[string]uint, error) {
	groupIDByKey := make(map[string]uint, len(resolved)+len(p.nodes))
	for k, v := range resolved {
		groupIDByKey[k] = v
	}
	// 逐个建（不并发）：Beacon 结构树规模是「集群 × 大区 × 小区」量级，串行建组开销可忽略，
	// 换来的是确定的父子顺序与更易读的失败路径。
	for i := range p.nodes {
		node := &p.nodes[i]
		if _, ok := groupIDByKey[node.key]; ok {
			continue
		}
		var parentID *uint
		if node.parentKey != "" {
			pid, ok := groupIDByKey[node.parentKey]
			if !ok {
				return nil, fmt.Errorf("%w: 分组 %s 的父节点未建立", ErrBeaconTopologyInvalid, node.key)
			}
			parentID = &pid
		}
		created, err := groups.Create(node.name, parentID)
		if err != nil {
			return nil, fmt.Errorf("创建分组 %s 失败: %w", node.name, err)
		}
		groupIDByKey[node.key] = created.ID
	}
	return groupIDByKey, nil
}

// applyAssignments 在事务内把实例移入 Beacon 对应分组（Beacon 为拓扑真源，FR-444 §4.3）。
//
// 只 ADD 成员关系、不删除实例的其它分组归属：分组树是「一个人为归类 + Beacon 归属」的叠加视图，
// 一台实例可同时属于多个分组（模型即 M:N），删除关系会破坏人工归类。重复拉取因唯一约束天然幂等。
func applyAssignments(tx *gorm.DB, p *beaconPlan, groupIDByKey map[string]uint) error {
	for i := range p.assignments {
		a := &p.assignments[i]
		groupID, ok := groupIDByKey[a.zoneKey]
		if !ok {
			return fmt.Errorf("%w: 实例 %d 的目标小区分组未建立", ErrBeaconTopologyInvalid, a.instanceID)
		}
		var count int64
		if err := tx.Model(&model.InstanceGroupMember{}).
			Where("group_id = ? AND instance_id = ?", groupID, a.instanceID).
			Count(&count).Error; err != nil {
			return fmt.Errorf("检查分组成员失败: %w", err)
		}
		if count > 0 {
			continue
		}
		if err := tx.Create(&model.InstanceGroupMember{GroupID: groupID, InstanceID: a.instanceID}).Error; err != nil {
			return fmt.Errorf("把实例 %d 加入分组 %d 失败: %w", a.instanceID, groupID, err)
		}
	}
	return nil
}

// applyTags 在事务内为实例覆盖 region:/zone:/role: 维度标签（FR-444 §4.1）。
//
// 刻意直接更新 tags 列而不经 InstanceService.Update：后者会触发 Worker 启动规格同步与
// FR-443 拓扑推送（改名/改归属视为拓扑变更）。补标签是 Beacon 侧的派生结果，不应反向
// 触发一次推送，否则「拉取 → 推送」会形成回声。其它维度标签（如 env:）原样保留。
func applyTags(tx *gorm.DB, p *beaconPlan, instancesByID map[uint]*model.Instance, groupIDByKey map[string]uint) error {
	for i := range p.assignments {
		a := &p.assignments[i]
		inst := instancesByID[a.instanceID]
		if inst == nil {
			continue
		}
		merged := mergeBeaconTags(model.ParseTags(inst.Tags), a.tags)
		raw, err := json.Marshal(merged)
		if err != nil {
			return fmt.Errorf("序列化实例 %d 标签失败: %w", a.instanceID, err)
		}
		newRaw := string(raw)
		if strings.TrimSpace(newRaw) == strings.TrimSpace(inst.Tags) {
			continue // 标签无变化：不写库，幂等拉取不产生无谓的 updated_at 抖动
		}
		if err := tx.Model(&model.Instance{}).Where("id = ?", a.instanceID).
			Update("tags", newRaw).Error; err != nil {
			return fmt.Errorf("更新实例 %d 标签失败: %w", a.instanceID, err)
		}
	}
	return nil
}
