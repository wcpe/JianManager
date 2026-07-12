package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
)

// TestPMConfigGet_EmptyRegistriesMarshalsArray 无任何 registry 配置的节点（全新节点）
// Get 视图的 registries 必须序列化为 `[]` 而非 null——Go nil 切片 marshal 成 null 会让
// 前端 `data.registries.length` 直接 TypeError 白屏（v0.15.0 真机测试抓出：
// /nodes?tab=jdk 整页空白，node-main/win-node 均复现）。
func TestPMConfigGet_EmptyRegistriesMarshalsArray(t *testing.T) {
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NodePMConfig{}))
	node := &model.Node{Name: "fresh", UUID: "u-fresh"}
	require.NoError(t, db.Create(node).Error)

	svc := NewPMConfigService(db, cpgrpc.NewClientPool())
	view, err := svc.Get(node.ID)
	require.NoError(t, err)

	raw, err := json.Marshal(view)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(raw), `"registries":[]`),
		"空配置节点 registries 应序列化为 []，实际: %s", string(raw))
}
