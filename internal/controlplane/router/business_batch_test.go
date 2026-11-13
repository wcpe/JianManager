package router

import (
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// newTestBusinessBatchService 构造测试用业务批量服务（固定测试密钥，令牌可跨用例复算）。
func newTestBusinessBatchService(db *gorm.DB, pool *cpgrpc.ClientPool) *service.BusinessBatchService {
	signer, err := service.NewBusinessConfirmTokenSigner(
		service.DeriveBusinessConfirmTokenSecret([]byte("test-jwt-secret")))
	if err != nil {
		return nil
	}
	return service.NewBusinessBatchService(db, service.NewBusinessService(db, pool), signer)
}
