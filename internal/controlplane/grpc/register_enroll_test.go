// 外部测试包：service 已 import grpc，grpc 包内测试不可再 import service（循环）。
// 故以 grpc_test 外部包同时引用 grpc + service，并以字面量复用 metadata header 常量。
package grpc_test

import (
	"testing"
	"time"

	"context"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
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

// TestRegister_ExistingNode_MissingIdentityRejected 已存在节点重注册必须同时提供 UUID 和密钥。
func TestRegister_ExistingNode_MissingIdentityRejected(t *testing.T) {
	h, db, _ := newEnrollRegisterHandler(t)
	existing := &model.Node{
		Name: "edge-old", Host: "127.0.0.1", GRPCPort: 0, WSPort: 0,
		Secret: "existing-secret", Status: model.NodeStatusOffline,
	}
	require.NoError(t, db.Create(existing).Error)

	_, err := h.Register(context.Background(), registerReq("edge-old"))
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	var node model.Node
	require.NoError(t, db.Where("uuid = ?", existing.UUID).First(&node).Error)
	require.Equal(t, "existing-secret", node.Secret)
}

// TestRegister_NewNode_UsesEnrollTokenPresetName worker 未上报名（req.Name 空）时，新节点采用
// enrollment token 的预设节点名（setup.go 承诺「留空由 CP/token 预设名生效」）。复现真机缺陷：
// 仅设 JIANMANAGER_NAME（未映射到 setup 上报名）→ req.Name="" → 原实现节点名为空、一键搭建
// 「选择节点」按名过滤取不到该节点、建实例被堵。
func TestRegister_NewNode_UsesEnrollTokenPresetName(t *testing.T) {
	h, db, enrollSvc := newEnrollRegisterHandler(t)
	_, plaintext, err := enrollSvc.Issue("preset-edge", 30, 1) // 添加节点时预设的名
	require.NoError(t, err)

	resp, err := h.Register(ctxWithToken(plaintext), registerReq("")) // worker 上报空名
	require.NoError(t, err)

	var node model.Node
	require.NoError(t, db.Where("uuid = ?", resp.NodeUuid).First(&node).Error)
	require.Equal(t, "preset-edge", node.Name, "req.Name 空时应采用 token 预设名，而非留空")
}

// TestRegister_ExistingNode_EmptyNameKeepsName worker 经 UUID 身份重注册时上报空名，不得把既有
// 非空节点名清空——否则真机 worker 每次重启都把名字抹成空（identity.NodeName 为空时）、选择器取不到。
func TestRegister_ExistingNode_EmptyNameKeepsName(t *testing.T) {
	h, db, _ := newEnrollRegisterHandler(t)
	existing := &model.Node{
		Name: "edge-keep", Host: "127.0.0.1", GRPCPort: 0, WSPort: 0,
		Secret: "sec-keep", Status: model.NodeStatusOffline,
	}
	require.NoError(t, db.Create(existing).Error)

	md := metadata.New(map[string]string{"node-uuid": existing.UUID, "node-secret": "sec-keep"})
	ctx := metadata.NewIncomingContext(context.Background(), md)
	resp, err := h.Register(ctx, registerReq("")) // 空名重注册
	require.NoError(t, err)
	require.Equal(t, existing.UUID, resp.NodeUuid)

	var node model.Node
	require.NoError(t, db.Where("uuid = ?", existing.UUID).First(&node).Error)
	require.Equal(t, "edge-keep", node.Name, "空名重注册不应清空既有节点名")
}
