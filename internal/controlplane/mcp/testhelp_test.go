package mcp

import (
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
