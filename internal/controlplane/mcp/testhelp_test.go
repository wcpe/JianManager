package mcp

import (
	"fmt"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// testPrincipal 构造测试用鉴权主体。
//
// 本 helper 被多个测试文件共享（sse_conn_test.go / handler_stateless_test.go 等），
// 移动或删除单个测试文件时须一并搬运，勿随之删除。
func testPrincipal(id uint, name string) *service.AgentPrincipal {
	return &service.AgentPrincipal{
		TokenID:     id,
		Name:        name,
		TokenPrefix: "jmat_ab12",
	}
}

// newStatelessTestDB 建内存库并迁移 MCP 传输层测试所需的表。
//
// 只迁移「Token 鉴权 + FR-390 调用流水」两张表：传输层测试要验证的是
// 「请求头 → 中间件归一 → 流水落库」这条链，不涉及工具业务表。
func newStatelessTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// 内存库：避免 Windows 上 TempDir 清理时 sqlite 文件仍被占用。
	dsn := fmt.Sprintf("file:mcptransport_%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AgentToken{}, &model.AgentCallLog{}))
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// newStatelessTestToken 签发一个 V1 Agent Token，返回鉴权服务与可用的明文。
//
// 不给任何 scope：agent_whoami 的资源类型是 AgentResourceNone，无需 scope 即可发现，
// 由此把测试聚焦在传输层行为上，不被授权矩阵干扰。
func newStatelessTestToken(t *testing.T, db *gorm.DB) (*service.AgentTokenService, string) {
	t.Helper()
	svc := service.NewAgentTokenService(db)
	_, plain, err := svc.Issue(service.IssueAgentTokenRequest{Name: "mcp-transport-test", TTLDays: 30})
	require.NoError(t, err)
	require.NotEmpty(t, plain)
	return svc, plain
}
