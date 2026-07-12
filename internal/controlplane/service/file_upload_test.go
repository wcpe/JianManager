package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeUploadStream 记录 Send 的分片；CloseAndRecv 默认模拟真实 Worker 行为
// （零帧=业务级失败「缺少首帧」，有帧=成功并回报累计字节数），可经 owner 覆盖。
type fakeUploadStream struct {
	grpc.ClientStream
	owner *fakeUploadWorker
	sent  []*workerpb.UploadFileChunk
}

func (f *fakeUploadStream) Send(c *workerpb.UploadFileChunk) error {
	f.sent = append(f.sent, c)
	return nil
}

func (f *fakeUploadStream) CloseAndRecv() (*workerpb.UploadFileResponse, error) {
	if len(f.sent) == 0 {
		return &workerpb.UploadFileResponse{Success: false, Error: "缺少首帧"}, nil
	}
	if f.owner.respOverride != nil {
		return f.owner.respOverride, nil
	}
	var n int64
	for _, c := range f.sent {
		n += int64(len(c.Content))
	}
	return &workerpb.UploadFileResponse{Success: true, BytesWritten: n}, nil
}

// fakeUploadWorker 可编排 UploadFile 建流错误（模拟老 Worker Unimplemented）、
// 覆盖响应（模拟完整性不符），并捕获回退路径的 WriteFile 请求。
type fakeUploadWorker struct {
	workerpb.WorkerServiceClient
	uploadErr    error
	respOverride *workerpb.UploadFileResponse
	streams      []*fakeUploadStream
	wrote        *workerpb.WriteFileRequest
}

func (f *fakeUploadWorker) UploadFile(_ context.Context, _ ...grpc.CallOption) (workerpb.WorkerService_UploadFileClient, error) {
	if f.uploadErr != nil {
		return nil, f.uploadErr
	}
	s := &fakeUploadStream{owner: f}
	f.streams = append(f.streams, s)
	return s, nil
}

func (f *fakeUploadWorker) WriteFile(_ context.Context, req *workerpb.WriteFileRequest, _ ...grpc.CallOption) (*workerpb.WriteFileResponse, error) {
	f.wrote = req
	return &workerpb.WriteFileResponse{Success: true}, nil
}

func seedFileUploadService(t *testing.T, worker *fakeUploadWorker) (*FileService, *model.Instance) {
	t.Helper()
	db := newSearchTestDB(t)
	node := &model.Node{UUID: "node-upload", Name: "node-upload", Host: "127.0.0.1", GRPCPort: 9100, WSPort: 9101, Secret: "secret", Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(node).Error)
	inst := &model.Instance{UUID: "inst-upload", NodeID: node.ID, Name: "inst-upload", Type: model.InstanceTypeGeneric, Role: model.InstanceRoleUniversal, ProcessType: model.ProcessTypeDirect, StartCommand: "noop", Status: model.InstanceStatusStopped, WorkDir: "/srv/upload"}
	require.NoError(t, db.Create(inst).Error)

	pool := cpgrpc.NewClientPool()
	pool.SetWorkerClientForTest(node.UUID, worker)
	return NewFileService(db, pool), inst
}

// TestFileService_UploadFile_StreamsChunks 新 Worker：先零帧探测，再按 64KB 分片流式发送，
// 首帧携带元信息、内容逐字节一致、完整性校验通过。
func TestFileService_UploadFile_StreamsChunks(t *testing.T) {
	worker := &fakeUploadWorker{}
	svc, inst := seedFileUploadService(t, worker)

	content := make([]byte, 150*1024) // 3 个分片：64+64+22KB
	for i := range content {
		content[i] = byte(i % 249)
	}
	require.NoError(t, svc.UploadFile(context.Background(), inst.ID, "plugins/a.jar", bytes.NewReader(content)))

	require.Len(t, worker.streams, 2, "应先探测（零帧流）再真传")
	require.Empty(t, worker.streams[0].sent, "探测流不得发送任何帧")

	frames := worker.streams[1].sent
	require.NotEmpty(t, frames)
	require.Equal(t, "inst-upload", frames[0].InstanceUuid, "首帧应携带实例 UUID")
	require.Equal(t, "plugins/a.jar", frames[0].Path, "首帧应携带目标路径")
	var got []byte
	for i, fr := range frames {
		if i > 0 {
			require.Empty(t, fr.InstanceUuid, "非首帧不得携带元信息")
			require.Empty(t, fr.Path)
		}
		require.LessOrEqual(t, len(fr.Content), uploadChunkSize)
		got = append(got, fr.Content...)
	}
	require.Equal(t, content, got, "分片拼接应与源内容一致")
	require.Nil(t, worker.wrote, "新 Worker 不得走 WriteFile 回退")
}

// TestFileService_UploadFile_EmptyFile 空文件也要发一个携带元信息的首帧。
func TestFileService_UploadFile_EmptyFile(t *testing.T) {
	worker := &fakeUploadWorker{}
	svc, inst := seedFileUploadService(t, worker)

	require.NoError(t, svc.UploadFile(context.Background(), inst.ID, "empty.txt", bytes.NewReader(nil)))
	require.Len(t, worker.streams, 2)
	frames := worker.streams[1].sent
	require.Len(t, frames, 1)
	require.Equal(t, "empty.txt", frames[0].Path)
	require.Empty(t, frames[0].Content)
}

// TestFileService_UploadFile_FallbackLegacySmall 老 Worker（Unimplemented）+ 小文件：
// 回退 WriteFile unary，内容完整送达。
func TestFileService_UploadFile_FallbackLegacySmall(t *testing.T) {
	worker := &fakeUploadWorker{uploadErr: status.Error(codes.Unimplemented, "unknown method UploadFile")}
	svc, inst := seedFileUploadService(t, worker)

	content := bytes.Repeat([]byte{0xAB}, 1024*1024)
	require.NoError(t, svc.UploadFile(context.Background(), inst.ID, "mods/x.jar", bytes.NewReader(content)))

	require.NotNil(t, worker.wrote, "老 Worker 应回退 WriteFile")
	require.Equal(t, "inst-upload", worker.wrote.InstanceUuid)
	require.Equal(t, "mods/x.jar", worker.wrote.Path)
	require.Equal(t, content, worker.wrote.Content)
}

// TestFileService_UploadFile_FallbackLegacyTooLarge 老 Worker + 超限文件：
// 明确报错引导升级节点，不盲发注定被拒的 WriteFile。
func TestFileService_UploadFile_FallbackLegacyTooLarge(t *testing.T) {
	worker := &fakeUploadWorker{uploadErr: status.Error(codes.Unimplemented, "unknown method UploadFile")}
	svc, inst := seedFileUploadService(t, worker)

	content := make([]byte, legacyUploadMaxBytes+1)
	err := svc.UploadFile(context.Background(), inst.ID, "big.bin", bytes.NewReader(content))
	require.Error(t, err)
	require.Contains(t, err.Error(), "升级节点")
	require.Nil(t, worker.wrote, "超限不得回退 WriteFile")
}

// TestFileService_UploadFile_BytesWrittenMismatch Worker 回报落盘字节数与已发送不符时报完整性错误。
func TestFileService_UploadFile_BytesWrittenMismatch(t *testing.T) {
	worker := &fakeUploadWorker{respOverride: &workerpb.UploadFileResponse{Success: true, BytesWritten: 1}}
	svc, inst := seedFileUploadService(t, worker)

	err := svc.UploadFile(context.Background(), inst.ID, "a.bin", bytes.NewReader([]byte("hello")))
	require.Error(t, err)
	require.Contains(t, err.Error(), "完整性")
}

// TestFileService_UploadFile_ProbeTransportError 探测遇非 Unimplemented 传输错误：
// 如实上抛，不误入回退分支。
func TestFileService_UploadFile_ProbeTransportError(t *testing.T) {
	worker := &fakeUploadWorker{uploadErr: status.Error(codes.Unavailable, "worker boom")}
	svc, inst := seedFileUploadService(t, worker)

	err := svc.UploadFile(context.Background(), inst.ID, "a.bin", bytes.NewReader([]byte("x")))
	require.Error(t, err)
	require.Contains(t, err.Error(), "worker boom")
	require.Nil(t, worker.wrote)
}

// TestFileService_UploadFile_RejectsTraversal 路径校验先行，越界路径不发起任何 RPC。
func TestFileService_UploadFile_RejectsTraversal(t *testing.T) {
	worker := &fakeUploadWorker{}
	svc, inst := seedFileUploadService(t, worker)

	err := svc.UploadFile(context.Background(), inst.ID, "../escape.txt", bytes.NewReader([]byte("x")))
	require.Error(t, err)
	require.Empty(t, worker.streams, "越界路径不得建流")
	require.Nil(t, worker.wrote)
}
