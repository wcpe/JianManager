package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newGroupTreeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{},
		&model.InstanceGroupNode{},
		&model.InstanceGroupMember{},
	))
	return db
}

func TestInstanceGroup_TreeCRUD(t *testing.T) {
	db := newGroupTreeTestDB(t)
	svc := NewInstanceGroupService(db)

	// 建根组
	root, err := svc.Create("亚洲区", nil)
	require.NoError(t, err)
	require.NotEmpty(t, root.UUID)
	require.Nil(t, root.ParentID)

	// 嵌套子组
	child, err := svc.Create("生存", &root.ID)
	require.NoError(t, err)
	require.Equal(t, root.ID, *child.ParentID)

	// 父不存在 → 拒绝
	_, err = svc.Create("孤儿", uptr(9999))
	require.ErrorIs(t, err, ErrInstanceGroupParentNotFound)

	// 改名
	updated, err := svc.Update(child.ID, strptr("生存服"), nil)
	require.NoError(t, err)
	require.Equal(t, "生存服", updated.Name)

	// 树视图：两个节点，子树计数初始为 0
	tree, err := svc.Tree()
	require.NoError(t, err)
	require.Len(t, tree, 2)
}

func TestInstanceGroup_MoveCyclePrevention(t *testing.T) {
	db := newGroupTreeTestDB(t)
	svc := NewInstanceGroupService(db)

	a, err := svc.Create("A", nil)
	require.NoError(t, err)
	b, err := svc.Create("B", &a.ID)
	require.NoError(t, err)

	// 把 A 移到其子 B 下 = 成环 → 拒绝
	_, err = svc.Update(a.ID, nil, ptrParent(b.ID))
	require.ErrorIs(t, err, ErrInstanceGroupCycle)

	// 把 A 移到自身下 = 成环 → 拒绝
	_, err = svc.Update(a.ID, nil, ptrParent(a.ID))
	require.ErrorIs(t, err, ErrInstanceGroupCycle)

	// 合法移动：B 移到根（清空父）
	moved, err := svc.Update(b.ID, nil, ptrParent(0))
	require.NoError(t, err)
	require.Nil(t, moved.ParentID)
}

func TestInstanceGroup_DeleteNonEmptyRejected(t *testing.T) {
	db := newGroupTreeTestDB(t)
	svc := NewInstanceGroupService(db)

	root, err := svc.Create("根", nil)
	require.NoError(t, err)

	// 有子组 → 拒删
	child, err := svc.Create("子", &root.ID)
	require.NoError(t, err)
	require.ErrorIs(t, svc.Delete(root.ID), ErrInstanceGroupNotEmpty)

	// 删空子组成功
	require.NoError(t, svc.Delete(child.ID))

	// 有成员 → 拒删
	inst := mkRoleInstance(t, db, "srv-1", model.InstanceRoleBackend)
	_, _, err = svc.AddMembers(root.ID, []uint{inst.ID})
	require.NoError(t, err)
	require.ErrorIs(t, svc.Delete(root.ID), ErrInstanceGroupNotEmpty)

	// 清空成员后可删
	require.NoError(t, svc.RemoveMembers(root.ID, []uint{inst.ID}))
	require.NoError(t, svc.Delete(root.ID))

	// 删组不删实例
	var instCount int64
	db.Model(&model.Instance{}).Where("id = ?", inst.ID).Count(&instCount)
	require.Equal(t, int64(1), instCount)
}

func TestInstanceGroup_MembersMNAndDedupCount(t *testing.T) {
	db := newGroupTreeTestDB(t)
	svc := NewInstanceGroupService(db)

	root, err := svc.Create("根", nil)
	require.NoError(t, err)
	leaf, err := svc.Create("叶", &root.ID)
	require.NoError(t, err)

	i1 := mkRoleInstance(t, db, "s1", model.InstanceRoleBackend)
	i2 := mkRoleInstance(t, db, "s2", model.InstanceRoleBackend)

	// 加成员：去重 + 跳过不存在实例
	added, _, err := svc.AddMembers(leaf.ID, []uint{i1.ID, i2.ID, i1.ID, 9999})
	require.NoError(t, err)
	require.Equal(t, 2, added)

	// 一实例可属多组（M:N）：i1 也加入 root
	added, _, err = svc.AddMembers(root.ID, []uint{i1.ID})
	require.NoError(t, err)
	require.Equal(t, 1, added)

	// 子树聚合去重：root 子树含 {i1(root), i1+i2(leaf)} → 去重后 2；leaf 自身 2
	tree, err := svc.Tree()
	require.NoError(t, err)
	counts := map[uint]int{}
	for _, n := range tree {
		counts[n.ID] = n.InstanceCount
	}
	require.Equal(t, 2, counts[root.ID])
	require.Equal(t, 2, counts[leaf.ID])

	// 移除成员
	require.NoError(t, svc.RemoveMembers(leaf.ID, []uint{i2.ID}))
	members, err := svc.Members(leaf.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)
}

func strptr(s string) *string { return &s }

// ptrParent 把「目标父 ID」包装为 service.Update 的 parentId 入参语义：
// 0 表示移到根（清空父），非 0 表示移到该父下。返回 **uint：外层非 nil=本次要改父。
func ptrParent(id uint) **uint {
	if id == 0 {
		var nilParent *uint
		return &nilParent
	}
	p := &id
	return &p
}
