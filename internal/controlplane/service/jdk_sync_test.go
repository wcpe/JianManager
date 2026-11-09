package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

type fakeFR033JDKWorker struct {
	workerpb.WorkerServiceClient
	jdks        []*workerpb.JDKInfo
	removedPath string
}

func (f *fakeFR033JDKWorker) ListJDKs(context.Context, *workerpb.ListJDKsRequest, ...grpc.CallOption) (*workerpb.ListJDKsResponse, error) {
	return &workerpb.ListJDKsResponse{Jdks: f.jdks}, nil
}

func (f *fakeFR033JDKWorker) RemoveJDK(_ context.Context, req *workerpb.RemoveJDKRequest, _ ...grpc.CallOption) (*workerpb.RemoveJDKResponse, error) {
	f.removedPath = req.Path
	return &workerpb.RemoveJDKResponse{Success: true}, nil
}

func TestFR033JDKServiceSyncsWorkerJDKsAndResolvesRuntime(t *testing.T) {
	db := newJDKTestDB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{Name: "fr033-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	pool.SetWorkerClientForTest(node.UUID, &fakeFR033JDKWorker{jdks: []*workerpb.JDKInfo{
		{Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/opt/jdks/temurin-21", Managed: true},
		{Vendor: "Temurin", MajorVersion: 17, Version: "17.0.12+7", Arch: "x64", Path: "/opt/jdks/temurin-17", Managed: true},
	}})
	svc := NewJDKService(db, pool)

	rows, err := svc.List(node.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, 21, rows[0].MajorVersion)
	require.Equal(t, "/opt/jdks/temurin-21", rows[0].Path)
	require.True(t, rows[0].Managed)

	resolved, err := svc.ResolveForInstance(node.ID, 0, 17)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	require.Equal(t, "/opt/jdks/temurin-17", resolved.Path)
}

func TestFR033JDKServiceDeletesManagedRuntimeFilesOnlyWhenUnused(t *testing.T) {
	db := newJDKTestDB(t)
	pool := cpgrpc.NewClientPool()
	node := &model.Node{Name: "fr033-node", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "s", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	fake := &fakeFR033JDKWorker{}
	pool.SetWorkerClientForTest(node.UUID, fake)
	svc := NewJDKService(db, pool)
	jdk := &model.NodeJDK{NodeID: node.ID, Vendor: "Temurin", MajorVersion: 21, Version: "21.0.4+9", Arch: "x64", Path: "/opt/jdks/temurin-21", Managed: true}
	require.NoError(t, db.Create(jdk).Error)

	used, err := svc.Delete(node.ID, jdk.ID)
	require.NoError(t, err)
	require.Empty(t, used)
	require.Equal(t, "/opt/jdks/temurin-21", fake.removedPath)

	var count int64
	require.NoError(t, db.Model(&model.NodeJDK{}).Where("id = ?", jdk.ID).Count(&count).Error)
	require.Zero(t, count)
}
