package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakeFileSearchWorker struct {
	workerpb.WorkerServiceClient
	resp *workerpb.SearchFilesResponse
	err  error
	got  *workerpb.SearchFilesRequest
}

func (f *fakeFileSearchWorker) SearchFiles(_ context.Context, req *workerpb.SearchFilesRequest, _ ...grpc.CallOption) (*workerpb.SearchFilesResponse, error) {
	f.got = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func seedFileSearchService(t *testing.T, worker *fakeFileSearchWorker) (*FileService, *model.Instance) {
	t.Helper()
	db := newSearchTestDB(t)
	node := &model.Node{UUID: "node-search", Name: "node-search", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "secret", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{UUID: "inst-search", NodeID: node.ID, Name: "inst-search", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDirect, StartCommand: "noop", Status: model.InstanceStatusStopped, WorkDir: "/srv/search"}
	require.NoError(t, db.Create(inst).Error)

	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	return NewFileService(db, pool), inst
}

func TestFileService_SearchFiles_透传Indexing(t *testing.T) {
	worker := &fakeFileSearchWorker{resp: &workerpb.SearchFilesResponse{Indexing: true}}
	svc, inst := seedFileSearchService(t, worker)

	res, err := svc.SearchFiles(inst.ID, "online-mode", "filename", 25)
	require.NoError(t, err)
	require.True(t, res.Indexing)
	require.False(t, res.Truncated)
	require.Empty(t, res.Hits)
	require.Equal(t, "inst-search", worker.got.InstanceUuid)
	require.Equal(t, "online-mode", worker.got.Query)
	require.Equal(t, "filename", worker.got.Mode)
	require.Equal(t, int32(25), worker.got.MaxResults)
}

func TestFileService_SearchFiles_委托错误(t *testing.T) {
	worker := &fakeFileSearchWorker{err: errors.New("worker boom")}
	svc, inst := seedFileSearchService(t, worker)

	res, err := svc.SearchFiles(inst.ID, "online-mode", "content", 10)
	require.Nil(t, res)
	require.Error(t, err)
	require.Contains(t, err.Error(), "搜索失败")
	require.Contains(t, err.Error(), "worker boom")
}
