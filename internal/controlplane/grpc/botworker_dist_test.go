package grpc_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"context"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpembed "github.com/wcpe/JianManager/internal/controlplane/embed"
	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
	"gorm.io/gorm"
)

// newBotDistHandler 建 handler 与一个已注册节点（uuid/secret 已知）。
func newBotDistHandler(t *testing.T) (*cpgrpc.ControlPlaneHandler, *model.Node) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}))
	node := &model.Node{UUID: "node-uuid-1", Name: "n1", Secret: "s3cret", Host: "127.0.0.1"}
	require.NoError(t, db.Create(node).Error)
	return cpgrpc.NewControlPlaneHandler(db, cpgrpc.NewClientPool()), node
}

// TestFetchBotWorkerArchive_AuthRejects 身份缺失/伪造一律拒（FR-308：与重注册同源校验）。
func TestFetchBotWorkerArchive_AuthRejects(t *testing.T) {
	h, node := newBotDistHandler(t)

	_, err := h.FetchBotWorkerArchive(context.Background(), &workerpb.FetchBotWorkerArchiveRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err), "空身份应 Unauthenticated")

	_, err = h.FetchBotWorkerArchive(context.Background(), &workerpb.FetchBotWorkerArchiveRequest{
		NodeUuid: node.UUID, NodeSecret: "wrong",
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "secret 不符应 PermissionDenied")

	_, err = h.FetchBotWorkerArchive(context.Background(), &workerpb.FetchBotWorkerArchiveRequest{
		NodeUuid: "ghost", NodeSecret: "s3cret",
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err), "未知 uuid 应 PermissionDenied")
}

// TestFetchBotWorkerArchive_ServeOrDegrade 合法身份按测试二进制的实际嵌入态分支断言：
// 已注入（本地跑过 make embed-botworker）→ 下发字节且指纹自洽、known_sha256 命中省流；
// 未注入（干净 checkout）→ Success=false + 可读原因（Worker 据此回退本地），而非 gRPC error。
func TestFetchBotWorkerArchive_ServeOrDegrade(t *testing.T) {
	h, node := newBotDistHandler(t)
	resp, err := h.FetchBotWorkerArchive(context.Background(), &workerpb.FetchBotWorkerArchiveRequest{
		NodeUuid: node.UUID, NodeSecret: node.Secret,
	})
	require.NoError(t, err)

	m, embedded := cpembed.EmbeddedBotWorkerManifest()
	if !embedded {
		require.False(t, resp.Success)
		require.Contains(t, resp.Error, "未内嵌")
		return
	}
	require.True(t, resp.Success)
	require.Equal(t, m.SHA256, resp.Sha256)
	sum := sha256.Sum256(resp.Archive)
	require.Equal(t, resp.Sha256, hex.EncodeToString(sum[:]), "下发字节与宣称指纹应自洽")

	// known_sha256 命中：不回字节（省流语义）。
	resp2, err := h.FetchBotWorkerArchive(context.Background(), &workerpb.FetchBotWorkerArchiveRequest{
		NodeUuid: node.UUID, NodeSecret: node.Secret, KnownSha256: m.SHA256,
	})
	require.NoError(t, err)
	require.True(t, resp2.Success)
	require.Empty(t, resp2.Archive)
}
