package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// uptr 返回指向 v 的 *uint，便于构造 ParentID。
func uptr(v uint) *uint { return &v }

func node(id uint, parent *uint) model.InstanceGroupNode {
	return model.InstanceGroupNode{ID: id, ParentID: parent}
}

func TestWouldCreateCycle(t *testing.T) {
	// 树：1 → 2 → 3，1 → 4
	nodes := []model.InstanceGroupNode{
		node(1, nil),
		node(2, uptr(1)),
		node(3, uptr(2)),
		node(4, uptr(1)),
	}

	tests := []struct {
		name        string
		nodeID      uint
		newParentID *uint
		want        bool
	}{
		{"移到根（清空 parent）不成环", 2, nil, false},
		{"移到兄弟子树下不成环", 3, uptr(4), false},
		{"移到自身 = 成环", 2, uptr(2), true},
		{"移到直接子节点下 = 成环", 1, uptr(2), true},
		{"移到孙节点下 = 成环", 1, uptr(3), true},
		{"移到非子孙节点下不成环", 4, uptr(3), false},
		{"父不变（自身当前父）不成环", 3, uptr(2), false},
		{"新父不存在按不成环处理（由上层校验存在性）", 2, uptr(999), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wouldCreateCycle(nodes, tt.nodeID, tt.newParentID)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSubtreeCounts(t *testing.T) {
	// 树：1 → 2 → 3，1 → 4（5 为独立根）
	nodes := []model.InstanceGroupNode{
		node(1, nil),
		node(2, uptr(1)),
		node(3, uptr(2)),
		node(4, uptr(1)),
		node(5, nil),
	}

	tests := []struct {
		name    string
		members map[uint][]uint
		want    map[uint]int
	}{
		{
			name:    "空成员全为 0",
			members: map[uint][]uint{},
			want:    map[uint]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 0},
		},
		{
			name: "子树聚合到祖先",
			// 3 有实例 100；4 有实例 200；2 自身无 → 2 聚合 {100}，1 聚合 {100,200}
			members: map[uint][]uint{
				3: {100},
				4: {200},
			},
			want: map[uint]int{1: 2, 2: 1, 3: 1, 4: 1, 5: 0},
		},
		{
			name: "同一实例在祖先与后代去重只计一次",
			// 实例 100 同时挂在 1（祖先）与 3（后代）→ 1 的子树聚合去重后仍为 1
			members: map[uint][]uint{
				1: {100},
				3: {100},
			},
			want: map[uint]int{1: 1, 2: 1, 3: 1, 4: 0, 5: 0},
		},
		{
			name: "同节点内重复实例 ID 去重",
			members: map[uint][]uint{
				3: {100, 100, 101},
			},
			want: map[uint]int{1: 2, 2: 2, 3: 2, 4: 0, 5: 0},
		},
		{
			name: "独立根互不影响",
			members: map[uint][]uint{
				5: {300, 301},
			},
			want: map[uint]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := subtreeCounts(nodes, tt.members)
			assert.Equal(t, tt.want, got)
		})
	}
}
