package router

// 本文件只放一个共享测试夹具，供多个 log-runtime 路由用例复用。
//
// # 为什么单独一个文件
//
// 这个 helper 原本住在 `log_resolve_gaps_operator_test.go`。那个文件整份验证的是
// 「解缺口」授权，随旧采集链路的缺口机制一起删除了；但 helper 本身是通用的
// （建一个带 LogRuntime 服务的 operator 路由），仍有四个用例在用。
// 删文件时若不把它移出来，四个不相关的用例会一起编译失败。

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

func setupLogRuntimeOperatorRouter(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool) *gin.Engine {
	t.Helper()
	jwtCfg := config.JWTConfig{Secret: "test-key", AccessTTL: 15 * time.Minute, RefreshTTL: 7 * 24 * time.Hour}
	groupSvc := service.NewGroupService(db)
	authzSvc := service.NewAuthzService(db)
	instanceSvc := service.NewInstanceService(db, groupSvc, pool)
	instanceSvc.SetDelegateBypassForTest(true)
	nodeSvc := service.NewNodeService(db)
	nodeSvc.SetInstanceService(instanceSvc)
	return Setup(&Services{
		Auth:              service.NewAuthService(db, jwtCfg),
		User:              service.NewUserService(db),
		Group:             groupSvc,
		Node:              nodeSvc,
		Instance:          instanceSvc,
		Audit:             service.NewAuditService(db),
		Authz:             authzSvc,
		LogRuntimePool:    pool,
		LogRuntimeHolders: service.NewLogTargetHolderService(db),
	}, jwtCfg.Secret)
}
