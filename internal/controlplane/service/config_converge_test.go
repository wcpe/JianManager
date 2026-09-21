package service

import (
	"strconv"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newBaselineTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Instance{}, &model.Node{}, &model.InstanceConfigVersion{},
		&model.ConfigBaseline{}, &model.InstanceGroupNode{}, &model.InstanceGroupMember{},
	))
	return db
}

func insertVersion(t *testing.T, db *gorm.DB, instanceID uint, content string) {
	t.Helper()
	require.NoError(t, db.Create(&model.InstanceConfigVersion{
		InstanceID: instanceID, FilePath: "server.properties",
		ContentHash: hashContent(content), Content: content,
	}).Error)
}

func TestConfigBaseline_DetectDrift(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 2)
	svc := NewConfigBaselineService(db, nil)

	bl, err := svc.UpsertBaseline(BaselineInput{ScopeKey: "all", FilePath: "server.properties", Content: "A"}, 1)
	require.NoError(t, err)
	require.Equal(t, hashContent("A"), bl.ContentHash)

	insertVersion(t, db, ids[0], "A") // 与基线一致
	insertVersion(t, db, ids[1], "B") // 漂移

	items, err := svc.DetectDrift(bl.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)
	byID := map[uint]DriftItem{}
	for _, it := range items {
		byID[it.InstanceID] = it
	}
	require.False(t, byID[ids[0]].Drift)
	require.Equal(t, hashContent("A"), byID[ids[0]].CurrentHash)
	require.True(t, byID[ids[1]].Drift)
	require.Equal(t, hashContent("B"), byID[ids[1]].CurrentHash)
	require.Equal(t, bl.ContentHash, byID[ids[1]].BaselineHash)
}

func TestConfigBaseline_Converge(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 2)
	svc := NewConfigBaselineService(db, nil)

	bl, err := svc.UpsertBaseline(BaselineInput{ScopeKey: "all", FilePath: "server.properties", Content: "A"}, 1)
	require.NoError(t, err)
	insertVersion(t, db, ids[0], "A")
	insertVersion(t, db, ids[1], "B")

	// 假写入：落一条与基线一致的版本，使收敛后复核无残余漂移。
	var written []uint
	svc.SetConvergeWriterForTest(func(instanceID uint, filePath, content, message string, authorID uint) (uint, error) {
		written = append(written, instanceID)
		v := &model.InstanceConfigVersion{InstanceID: instanceID, FilePath: filePath, ContentHash: hashContent(content), Content: content}
		require.NoError(t, db.Create(v).Error)
		return v.ID, nil
	})

	res, err := svc.Converge(bl.ID, ConvergeOptions{}, 1)
	require.NoError(t, err)
	require.Equal(t, 1, res.Targeted, "仅漂移实例进入收敛")
	require.Equal(t, 1, res.Succeeded)
	require.Equal(t, 0, res.Failed)
	require.Equal(t, []uint{ids[1]}, written)
	require.Empty(t, res.ResidualDrift, "收敛后复核应无残余漂移")
}

func TestConfigBaseline_ScopeGroup(t *testing.T) {
	db := newBaselineTestDB(t)
	ids := seedPlainInstances(t, db, 2)
	svc := NewConfigBaselineService(db, nil)

	group := &model.InstanceGroupNode{Name: "g1"}
	require.NoError(t, db.Create(group).Error)
	require.NoError(t, db.Create(&model.InstanceGroupMember{GroupID: group.ID, InstanceID: ids[0]}).Error)

	bl, err := svc.UpsertBaseline(BaselineInput{
		ScopeKey: "group:" + strconv.FormatUint(uint64(group.ID), 10), FilePath: "server.properties", Content: "A",
	}, 1)
	require.NoError(t, err)

	insertVersion(t, db, ids[0], "X")
	insertVersion(t, db, ids[1], "Y")

	items, err := svc.DetectDrift(bl.ID)
	require.NoError(t, err)
	require.Len(t, items, 1, "group scope 仅覆盖成员实例")
	require.Equal(t, ids[0], items[0].InstanceID)
	require.True(t, items[0].Drift)
}

func TestConfigBaseline_UpsertRejectsBadScope(t *testing.T) {
	db := newBaselineTestDB(t)
	svc := NewConfigBaselineService(db, nil)
	_, err := svc.UpsertBaseline(BaselineInput{ScopeKey: "bogus", FilePath: "server.properties", Content: "A"}, 1)
	require.Error(t, err)
}
