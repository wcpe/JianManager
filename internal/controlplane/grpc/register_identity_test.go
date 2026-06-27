// 外部测试包：service 已 import grpc，grpc 包内测试不可再 import service（循环）。
// 故以 grpc_test 外部包同时引用 grpc + service，并以字面量复用 metadata header 常量。
package grpc_test

import (
	"testing"

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

// nodeUUIDHeader / nodeSecretHeader 与 grpc / worker register / heartbeat 中常量一致（wire 约定，ADR-039）。
const (
	nodeUUIDHeader   = "node-uuid"
	nodeSecretHeader = "node-secret"
)

// newIdentityRegisterHandler 建带真实 EnrollTokenService 的 Register handler 与底层 DB（ADR-039）。
func newIdentityRegisterHandler(t *testing.T) (*cpgrpc.ControlPlaneHandler, *gorm.DB, *service.EnrollTokenService) {
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

// regReqHost 构造一个不触发反向连接（GrpcPort=0）、指定 host 的注册请求。
func regReqHost(name, host string) *workerpb.RegisterRequest {
	return &workerpb.RegisterRequest{Name: name, Host: host, GrpcPort: 0, WsPort: 0, Os: "linux", Arch: "amd64", CpuCores: 1}
}

// ctxWithIdentity 把 node_uuid/node_secret 注入入站 metadata（模拟升级后 Worker 经 metadata 出示身份）。
func ctxWithIdentity(uuid, secret string) context.Context {
	md := metadata.New(map[string]string{nodeUUIDHeader: uuid, nodeSecretHeader: secret})
	return metadata.NewIncomingContext(context.Background(), md)
}

// ctxWithEnrollToken 把 enrollment token 注入入站 metadata。
func ctxWithEnrollToken(token string) context.Context {
	md := metadata.New(map[string]string{enrollTokenHeader: token})
	return metadata.NewIncomingContext(context.Background(), md)
}

// seedNode 直接落库一个既有节点，返回其 UUID。
func seedNode(t *testing.T, db *gorm.DB, name, host, secret string) string {
	t.Helper()
	n := &model.Node{Name: name, Host: host, GRPCPort: 0, WSPort: 0, Secret: secret, Status: model.NodeStatusOffline}
	require.NoError(t, db.Create(n).Error)
	return n.UUID
}

// TestRegister_UUIDProof_Reregisters 升级后 Worker 持 uuid+secret 重注册：按 UUID 命中、更新 host/port、放行（ADR-039 §1.2-1）。
func TestRegister_UUIDProof_Reregisters(t *testing.T) {
	h, db, _ := newIdentityRegisterHandler(t)
	uuid := seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	// 同一身份从新 host 上报（如机器换了 IP）：UUID/secret 命中即放行并更新 host。
	resp, err := h.Register(ctxWithIdentity(uuid, "secret-a"), regReqHost("edge-a", "10.0.0.99"))
	require.NoError(t, err)
	require.Equal(t, uuid, resp.NodeUuid)
	require.Equal(t, "secret-a", resp.NodeSecret)

	var node model.Node
	require.NoError(t, db.Where("uuid = ?", uuid).First(&node).Error)
	require.Equal(t, "10.0.0.99", node.Host, "UUID 命中重注册应更新 host")
}

// TestRegister_UUIDProof_AllowsRename 升级后 Worker 持 uuid+secret 改名重注册：允许改名（受唯一约束）（ADR-039 §1.2-1）。
func TestRegister_UUIDProof_AllowsRename(t *testing.T) {
	h, db, _ := newIdentityRegisterHandler(t)
	uuid := seedNode(t, db, "edge-old-name", "10.0.0.1", "secret-a")

	resp, err := h.Register(ctxWithIdentity(uuid, "secret-a"), regReqHost("edge-new-name", "10.0.0.1"))
	require.NoError(t, err)
	require.Equal(t, uuid, resp.NodeUuid)

	var node model.Node
	require.NoError(t, db.Where("uuid = ?", uuid).First(&node).Error)
	require.Equal(t, "edge-new-name", node.Name, "UUID 命中重注册应允许改名")
}

// TestRegister_UUIDProof_SecretMismatch 持 uuid 但 secret 不符：拒绝（PermissionDenied），不覆写（ADR-039 §1.2-1）。
func TestRegister_UUIDProof_SecretMismatch(t *testing.T) {
	h, db, _ := newIdentityRegisterHandler(t)
	uuid := seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	_, err := h.Register(ctxWithIdentity(uuid, "WRONG-secret"), regReqHost("edge-a", "10.0.0.99"))
	require.Error(t, err)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// 原节点 host 未被篡改。
	var node model.Node
	require.NoError(t, db.Where("uuid = ?", uuid).First(&node).Error)
	require.Equal(t, "10.0.0.1", node.Host, "secret 不符不得覆写 host")
}

// TestRegister_SameHostLegacy_Reregisters 未升级旧 Worker（只带 name）、host 与库存一致：同机重启信号，放行（ADR-039 §1.2-2）。
func TestRegister_SameHostLegacy_Reregisters(t *testing.T) {
	h, db, _ := newIdentityRegisterHandler(t)
	uuid := seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	// 无 uuid/secret、无 token，但 host 与登记 host 一致 → 同机重启，放行重注册。
	resp, err := h.Register(context.Background(), regReqHost("edge-a", "10.0.0.1"))
	require.NoError(t, err)
	require.Equal(t, uuid, resp.NodeUuid, "同机重启应返回既有身份")
	require.Equal(t, "secret-a", resp.NodeSecret)
}

// TestRegister_SameNameDifferentHost_Rejected 核心 BUG-A：另一台机器（host 不同）用同名、无 uuid、带有效 token 注册
// → 撞名拒绝，绝不覆写旧节点身份/host（ADR-039 §1.2-3）。
func TestRegister_SameNameDifferentHost_Rejected(t *testing.T) {
	h, db, enrollSvc := newIdentityRegisterHandler(t)
	uuid := seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	_, plaintext, err := enrollSvc.Issue("", 30, 1)
	require.NoError(t, err)

	// 陌生机器：host 不同、无身份、带有效 token。
	_, err = h.Register(ctxWithEnrollToken(plaintext), regReqHost("edge-a", "192.168.1.50"))
	require.Error(t, err, "异机同名应被拒绝")
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// 旧节点身份/host 必须原封不动（不被覆写）。
	var node model.Node
	require.NoError(t, db.Where("uuid = ?", uuid).First(&node).Error)
	require.Equal(t, "10.0.0.1", node.Host, "异机同名注册不得覆写旧节点 host")
	require.Equal(t, "secret-a", node.Secret, "异机同名注册不得改旧节点 secret")

	// 不应新建第二个同名节点行。
	var count int64
	db.Model(&model.Node{}).Where("name = ?", "edge-a").Count(&count)
	require.Equal(t, int64(1), count, "撞名不得新建节点行")
}

// TestRegister_NewNameValidToken_CreatesFreshNode 新名 + 有效 token：建全新 UUID 节点（ADR-039 §1.2-3）。
func TestRegister_NewNameValidToken_CreatesFreshNode(t *testing.T) {
	h, db, enrollSvc := newIdentityRegisterHandler(t)
	_ = seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	_, plaintext, err := enrollSvc.Issue("", 30, 1)
	require.NoError(t, err)

	resp, err := h.Register(ctxWithEnrollToken(plaintext), regReqHost("edge-b", "192.168.1.50"))
	require.NoError(t, err)
	require.NotEmpty(t, resp.NodeUuid)

	var count int64
	db.Model(&model.Node{}).Count(&count)
	require.Equal(t, int64(2), count, "新名带 token 应建第二个节点")
}

// ctxWithIdentityAndToken 同时注入身份与 enrollment token 的入站 metadata。
func ctxWithIdentityAndToken(uuid, secret, token string) context.Context {
	md := metadata.New(map[string]string{
		nodeUUIDHeader:    uuid,
		nodeSecretHeader:  secret,
		enrollTokenHeader: token,
	})
	return metadata.NewIncomingContext(context.Background(), md)
}

// TestRegister_UnknownUUID_FallsToTokenPath 持一个库中不存在的 uuid（如残留旧身份指向已删节点）：
// 不命中 UUID 分支，落到 token 新建路径——撞名则拒、新名则建（ADR-039 §1.2 边界）。
func TestRegister_UnknownUUID_FallsToTokenPath(t *testing.T) {
	h, db, enrollSvc := newIdentityRegisterHandler(t)
	_ = seedNode(t, db, "edge-a", "10.0.0.1", "secret-a")

	_, plaintext, err := enrollSvc.Issue("", 30, 1)
	require.NoError(t, err)

	// uuid 不存在于库、host 不同、带 token、新名 → 走 token 新建。
	resp, err := h.Register(
		ctxWithIdentityAndToken("00000000-0000-0000-0000-000000000000", "whatever", plaintext),
		regReqHost("edge-c", "192.168.1.77"))
	require.NoError(t, err)
	require.NotEmpty(t, resp.NodeUuid)
}
