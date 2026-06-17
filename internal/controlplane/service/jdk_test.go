package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

func newJDKTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NodeJDK{}, &model.Instance{}))
	return db
}

// 占用的实例不会阻拦 JDK 删除；未占用应直接删除并返回 nil。
func TestJDKService_Delete_OK(t *testing.T) {
	db := newJDKTestDB(t)
	svc := NewJDKService(db, nil)

	node := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk"}
	require.NoError(t, db.Create(jdk).Error)

	used, err := svc.Delete(node.ID, jdk.ID)
	require.NoError(t, err)
	require.Empty(t, used)

	var n int64
	db.Model(&model.NodeJDK{}).Where("id = ?", jdk.ID).Count(&n)
	require.Zero(t, n)
}

// 有实例占用时返回 ErrJDKInUse 和占用实例列表，不删除 JDK。
func TestJDKService_Delete_InUse(t *testing.T) {
	db := newJDKTestDB(t)
	svc := NewJDKService(db, nil)

	node := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk"}
	require.NoError(t, db.Create(jdk).Error)
	inst := &model.Instance{NodeID: node.ID, Name: "i1", Type: model.InstanceTypeGeneric, ProcessType: model.ProcessTypeDirect, JDKID: jdk.ID}
	require.NoError(t, db.Create(inst).Error)

	used, err := svc.Delete(node.ID, jdk.ID)
	require.ErrorIs(t, err, ErrJDKInUse)
	require.Len(t, used, 1)
	require.Equal(t, inst.ID, used[0].ID)

	var n int64
	db.Model(&model.NodeJDK{}).Where("id = ?", jdk.ID).Count(&n)
	require.Equal(t, int64(1), n, "占用时不应删除 JDK")
}

// ResolveForInstance：优先按 jdkId 匹配，否则按大版本倒序。
func TestJDKService_ResolveForInstance(t *testing.T) {
	db := newJDKTestDB(t)
	svc := NewJDKService(db, nil)

	node := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	a := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 17, Version: "17.0.12", Arch: "x64", Path: "/opt/jdk-17"}
	b := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4", Arch: "x64", Path: "/opt/jdk-21"}
	require.NoError(t, db.Create(a).Error)
	require.NoError(t, db.Create(b).Error)

	got, err := svc.ResolveForInstance(node.ID, 0, 21)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, b.Path, got.Path)
}

