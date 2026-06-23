// 外部测试包：service 已 import grpc，grpc 包内测试不可再 import service（循环）。
// 故以 grpc_test 外部包同时引用 grpc + service，并以字面量复用 metadata header 常量。
package grpc_test

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// enrollTokenHeader 与 internal/controlplane/grpc、internal/worker/register 中的常量一致（wire 约定）。
const enrollTokenHeader = "enroll-token"

// newEnrollRegisterHandler 建带真实 EnrollTokenService 的 Register handler 与底层 DB（FR-080，见 ADR-020）。
func newEnrollRegisterHandler(t *testing.T) (*cpgrpc.ControlPlaneHandler, *gorm.DB, *service.EnrollTokenService) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}, &model.NodeEnrollToken{}))

	enrollSvc := service.NewEnrollTokenService(db)
	h := cpgrpc.NewControlPlaneHandler(db, cpgrpc.NewClientPool())
	h.SetEnrollmentValidator(enrollSvc)
	return h, db, enrollSvc
}

// registerReq 构造一个不触发反向连接（GrpcPort=0）的注册请求。
func registerReq(name string) *workerpb.RegisterRequest {
	return &workerpb.RegisterRequest{Name: name, Host: "127.0.0.1", GrpcPort: 0, WsPort: 0, Os: "linux", Arch: "amd64", CpuCores: 1}
}

// ctxWithToken 把 enrollment token 注入入站 metadata（模拟 Worker 经 metadata 传递）。
func ctxWithToken(token string) context.Context {
	md := metadata.New(map[string]string{enrollTokenHeader: token})
	return metadata.NewIncomingContext(context.Background(), md)
}

// TestRegister_NewNode_ValidToken 新节点带有效 token：放行、建库、消费 token、换发身份。
func TestRegister_NewNode_ValidToken(t *testing.T) {
	h, db, enrollSvc := newEnrollRegisterHandler(t)
	_, plaintext, err := enrollSvc.Issue("", 30, 1)
	require.NoError(t, err)

	resp, err := h.Register(ctxWithToken(plaintext), registerReq("edge-new"))
	require.NoError(t, err)
	require.NotEmpty(t, resp.NodeUuid)
	require.NotEmpty(t, resp.NodeSecret)

	// 节点已落库。
	var node model.Node
	require.NoError(t, db.Where("name = ?", "edge-new").First(&node).Error)
	// token 已被消费且记录消费节点。
	var tok model.NodeEnrollToken
	require.NoError(t, db.First(&tok).Error)
	require.True(t, tok.Used)
	require.Equal(t, resp.NodeUuid, tok.UsedByNode)
}

// TestRegister_NewNode_MissingToken 新节点无 token：拒绝（PermissionDenied），不建库。
func TestRegister_NewNode_MissingToken(t *testing.T) {
	h, db, _ := newEnrollRegisterHandler(t)

	_, err := h.Register(context.Background(), registerReq("edge-no-token"))
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	var count int64
	db.Model(&model.Node{}).Where("name = ?", "edge-no-token").Count(&count)
	require.Zero(t, count, "被拒的新节点不应落库")
}

// TestRegister_NewNode_ExpiredToken 新节点带过期 token：拒绝。
func TestRegister_NewNode_ExpiredToken(t *testing.T) {
	h, db, enrollSvc := newEnrollRegisterHandler(t)
	tok, plaintext, err := enrollSvc.Issue("", 30, 1)
	require.NoError(t, err)
	// 直接改库使其过期。
	require.NoError(t, db.Model(&model.NodeEnrollToken{}).Where("id = ?", tok.ID).
		Update("expires_at", time.Now().Add(-time.Minute)).Error)

	_, err = h.Register(ctxWithToken(plaintext), registerReq("edge-expired"))
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestRegister_NewNode_UsedToken 新节点带已消费 token：拒绝（一次性）。
func TestRegister_NewNode_UsedToken(t *testing.T) {
	h, _, enrollSvc := newEnrollRegisterHandler(t)
	_, plaintext, err := enrollSvc.Issue("", 30, 1)
	require.NoError(t, err)

	// 第一次成功消费。
	_, err = h.Register(ctxWithToken(plaintext), registerReq("edge-first"))
	require.NoError(t, err)

	// 同一 token 第二次（即便换个新节点名）被拒。
	_, err = h.Register(ctxWithToken(plaintext), registerReq("edge-second"))
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestRegister_ExistingNode_NoTokenReregisters 老节点（name 命中）重注册不强制 token：放行，返回既有身份。
func TestRegister_ExistingNode_NoTokenReregisters(t *testing.T) {
	h, db, _ := newEnrollRegisterHandler(t)
	existing := &model.Node{
		Name: "edge-old", Host: "127.0.0.1", GRPCPort: 0, WSPort: 0,
		Secret: "existing-secret", Status: model.NodeStatusOffline,
	}
	require.NoError(t, db.Create(existing).Error)

	resp, err := h.Register(context.Background(), registerReq("edge-old"))
	require.NoError(t, err)
	require.Equal(t, existing.UUID, resp.NodeUuid)
	require.Equal(t, "existing-secret", resp.NodeSecret, "重注册应返回既有 secret，不重签")
}
