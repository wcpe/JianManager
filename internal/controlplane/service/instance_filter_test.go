package service

import (
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

func newFilterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{}, &model.Network{}, &model.NetworkMember{}, &model.GroupInstance{},
	))
	return db
}

// mkTaggedInstance 落库一个带 tags 的实例，便于环境/标签过滤测试。
func mkTaggedInstance(t *testing.T, db *gorm.DB, name string, nodeID uint, tags []string) *model.Instance {
	t.Helper()
	raw, _ := json.Marshal(tags)
	inst := &model.Instance{
		Name: name, NodeID: nodeID, Type: model.InstanceTypeMinecraftJava,
		Role: model.InstanceRoleBackend, ProcessType: model.ProcessTypeDaemon,
		StartCommand: "x", Status: model.InstanceStatusStopped, Tags: string(raw),
	}
	require.NoError(t, db.Create(inst).Error)
	return inst
}

func names(insts []model.Instance) []string {
	out := make([]string, 0, len(insts))
	for _, i := range insts {
		out = append(out, i.Name)
	}
	return out
}

func TestInstanceList_FilterByEnvAndTag(t *testing.T) {
	db := newFilterTestDB(t)
	svc := NewInstanceService(db, nil, nil)

	mkTaggedInstance(t, db, "prod-smp", 1, []string{"env:prod", "survival"})
	mkTaggedInstance(t, db, "dev-smp", 1, []string{"env:dev", "survival"})
	mkTaggedInstance(t, db, "prod-lobby", 2, []string{"env:prod", "lobby"})
	// 关键：标签 "production" 含子串 "prod"，DB LIKE 会粗命中 env=prod，
	// 必须靠应用层精确过滤排除——回归用例。
	mkTaggedInstance(t, db, "staging", 1, []string{"production-notes"})
	mkTaggedInstance(t, db, "no-tags", 1, nil)

	t.Run("按环境过滤精确不误命中子串", func(t *testing.T) {
		got, err := svc.List(InstanceFilter{Env: "prod"})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"prod-smp", "prod-lobby"}, names(got))
	})

	t.Run("按标签过滤", func(t *testing.T) {
		got, err := svc.List(InstanceFilter{Tag: "survival"})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"prod-smp", "dev-smp"}, names(got))
	})

	t.Run("环境+标签+节点组合", func(t *testing.T) {
		node := uint(1)
		got, err := svc.List(InstanceFilter{Env: "prod", Tag: "survival", NodeID: &node})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"prod-smp"}, names(got))
	})

	t.Run("无过滤返回全部", func(t *testing.T) {
		got, err := svc.List(InstanceFilter{})
		require.NoError(t, err)
		require.Len(t, got, 5)
	})
}

func TestInstanceList_FilterByNetwork(t *testing.T) {
	db := newFilterTestDB(t)
	svc := NewInstanceService(db, nil, nil)

	a := mkTaggedInstance(t, db, "a", 1, []string{"env:prod"})
	b := mkTaggedInstance(t, db, "b", 1, []string{"env:dev"})
	mkTaggedInstance(t, db, "c", 1, nil)

	net := &model.Network{Name: "survival"}
	require.NoError(t, db.Create(net).Error)
	require.NoError(t, db.Create(&model.NetworkMember{NetworkID: net.ID, InstanceID: a.ID}).Error)
	require.NoError(t, db.Create(&model.NetworkMember{NetworkID: net.ID, InstanceID: b.ID}).Error)

	t.Run("按群组过滤", func(t *testing.T) {
		got, err := svc.List(InstanceFilter{NetworkID: &net.ID})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"a", "b"}, names(got))
	})

	t.Run("群组+环境组合", func(t *testing.T) {
		got, err := svc.List(InstanceFilter{NetworkID: &net.ID, Env: "prod"})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"a"}, names(got))
	})
}

func TestInstanceListByGroups_WithFilter(t *testing.T) {
	db := newFilterTestDB(t)
	svc := NewInstanceService(db, nil, nil)

	a := mkTaggedInstance(t, db, "a", 1, []string{"env:prod"})
	b := mkTaggedInstance(t, db, "b", 1, []string{"env:dev"})
	c := mkTaggedInstance(t, db, "c", 1, []string{"env:prod"})
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: 10, InstanceID: a.ID}).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: 10, InstanceID: b.ID}).Error)
	require.NoError(t, db.Create(&model.GroupInstance{GroupID: 20, InstanceID: c.ID}).Error)

	t.Run("空组集合返回空", func(t *testing.T) {
		got, err := svc.ListByGroups(nil, InstanceFilter{})
		require.NoError(t, err)
		require.Empty(t, got)
	})

	t.Run("权限组约束叠加环境过滤", func(t *testing.T) {
		got, err := svc.ListByGroups([]uint{10}, InstanceFilter{Env: "prod"})
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"a"}, names(got))
	})
}

func TestInstanceUpdate_Tags(t *testing.T) {
	db := newFilterTestDB(t)
	svc := NewInstanceService(db, nil, nil)
	inst := mkTaggedInstance(t, db, "x", 1, nil)

	t.Run("写入并规范化", func(t *testing.T) {
		tags := []string{" env:prod ", "survival", "survival", ""}
		updated, err := svc.Update(inst.ID, UpdateInstanceFields{Tags: &tags})
		require.NoError(t, err)
		require.Equal(t, []string{"env:prod", "survival"}, model.ParseTags(updated.Tags))
	})

	t.Run("空数组清空标签", func(t *testing.T) {
		empty := []string{}
		updated, err := svc.Update(inst.ID, UpdateInstanceFields{Tags: &empty})
		require.NoError(t, err)
		require.Empty(t, model.ParseTags(updated.Tags))
	})

	t.Run("nil 不改动其它字段", func(t *testing.T) {
		tags := []string{"env:dev"}
		_, err := svc.Update(inst.ID, UpdateInstanceFields{Tags: &tags})
		require.NoError(t, err)
		newName := "renamed"
		updated, err := svc.Update(inst.ID, UpdateInstanceFields{Name: &newName})
		require.NoError(t, err)
		require.Equal(t, "renamed", updated.Name)
		require.Equal(t, []string{"env:dev"}, model.ParseTags(updated.Tags)) // tags 不变
	})
}
