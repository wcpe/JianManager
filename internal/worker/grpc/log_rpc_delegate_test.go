package grpc

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wcpe/JianManager/internal/worker/logs/query/grpcsvc"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// fakeArchiveImporter 是 grpcsvc.IngestArchiveImporter 的替身，记录被调用时的入参。
type fakeArchiveImporter struct {
	calls     int
	namespace string
	operator  string
}

func (f *fakeArchiveImporter) ImportArchivesNow(namespace, operator string) (int, int, error) {
	f.calls++
	f.namespace = namespace
	f.operator = operator
	return 1, 1, nil
}

// startLogRPCWorkerGRPC 起 bufconn 真 gRPC server 挂本包的 *Server，返回生成的客户端。
// 为什么必须穿透 gRPC 边界：直接调 grpcsvc 层会绕过 Server 的 delegate 装配，
// 「RPC 在 Server 上没接线 ⇒ 恒返回 Unimplemented」这类断链正是这样漏检的（PROTO-1）。
func startLogRPCWorkerGRPC(t *testing.T, srv *Server) workerpb.WorkerServiceClient {
	t.Helper()
	grpcServer := grpc.NewServer()
	workerpb.RegisterWorkerServiceServer(grpcServer, srv)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return workerpb.NewWorkerServiceClient(conn)
}

// newLogRPCTestServer 构造装配了日志 RPC 服务的 Worker Server（内存进程管理器即可）。
func newLogRPCTestServer(t *testing.T, importer grpcsvc.IngestArchiveImporter) *Server {
	t.Helper()
	srv := NewServer(process.NewManager(t.TempDir()), "fixture-node", nil, nil, nil)
	svc := grpcsvc.New(nil, nil)
	svc.SetIngestArchiveImporter(importer)
	srv.SetLogQueryService(svc)
	return srv
}

// TestLogImportArchives_ReachesServiceThroughGRPC 守护 PROTO-1：
// 手动归档导入入口必须在 Worker 的 gRPC Server 上有 delegate——否则调用恒返回 Unimplemented，
// 而配置注释与 CHANGELOG 都宣称该入口「始终可用」，形成「文档说能用、实际不可用」的静默断链。
// 旧实现（缺 delegate）下本用例必红：gRPC 层直接回 codes.Unimplemented。
func TestLogImportArchives_ReachesServiceThroughGRPC(t *testing.T) {
	importer := &fakeArchiveImporter{}
	client := startLogRPCWorkerGRPC(t, newLogRPCTestServer(t, importer))

	// 操作人经 metadata 携带（与 grpcsvc 的 OperatorMetadataKey 同源约定）。
	ctx := metadata.NewOutgoingContext(context.Background(),
		metadata.Pairs(grpcsvc.OperatorMetadataKey, "ops@example"))
	resp, err := client.LogImportArchives(ctx, &workerpb.LogImportArchivesRequest{StorageNamespace: " ns:game "})
	require.NoError(t, err)
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_SUCCEEDED, resp.GetState(), resp.GetError().GetMessage())
	require.Equal(t, 1, importer.calls, "请求必须穿透到服务层，而不是被 Unimplemented 兜底吃掉")
	require.Equal(t, "ops@example", importer.operator)
	require.Equal(t, "ns:game", importer.namespace, "命名空间应 trim 后下传")
}

// TestLogImportArchives_OperatorRequiredThroughGRPC 守护「不可无痕触发」在 gRPC 边界同样成立：
// 缺操作人时服务层拒绝，且不得调用导入器。旧实现（缺 delegate）下本用例在断言错误码处必红
// （收到的是 Unimplemented 而非业务拒绝）。
func TestLogImportArchives_OperatorRequiredThroughGRPC(t *testing.T) {
	importer := &fakeArchiveImporter{}
	client := startLogRPCWorkerGRPC(t, newLogRPCTestServer(t, importer))

	resp, err := client.LogImportArchives(context.Background(), &workerpb.LogImportArchivesRequest{StorageNamespace: "ns:game"})
	require.NoError(t, err)
	require.Equal(t, workerpb.LogTaskState_LOG_TASK_FAILED, resp.GetState())
	require.Contains(t, resp.GetError().GetMessage(), grpcsvc.OperatorMetadataKey)
	require.Zero(t, importer.calls, "无操作人时不得执行导入（不可无痕）")
}

// TestLogRPCs_AllHaveExplicitDelegate 系统性守住 delegate 装配：
// Server 嵌入的是 UnimplementedWorkerServiceServer，**任何** Log* RPC 只要漏写 delegate 就会
// 恒返回 Unimplemented，且不被编译期发现。
//
// 为什么读源码而不是用反射：反射无法区分「Server 自己的实现」与「嵌入字段的提升方法」——
// 提升方法的 Func 是编译器生成的包装器，指针与原型不同、也无法与对照类型比较（实测两者不等）。
// 因此直接解析本包生产源码，核对每个 Log* 方法都有 `func (s *Server) <Name>(` 声明。
func TestLogRPCs_AllHaveExplicitDelegate(t *testing.T) {
	declared := declaredServerMethods(t)
	clientIface := reflect.TypeOf((*workerpb.WorkerServiceClient)(nil)).Elem()

	checked := 0
	for i := 0; i < clientIface.NumMethod(); i++ {
		name := clientIface.Method(i).Name
		if !strings.HasPrefix(name, "Log") {
			continue
		}
		checked++
		_, ok := declared[name]
		require.True(t, ok,
			"%s 在 Worker Server 上没有显式 delegate（只有嵌入 Unimplemented 的兜底实现），调用会恒返回 Unimplemented", name)
	}
	require.Greater(t, checked, 10, "Log* RPC 数量异常，反射检查可能失效")
}

// declaredServerMethods 解析本包生产源码（跳过 _test.go），收集 `func (s *Server) Xxx` 的方法名。
func declaredServerMethods(t *testing.T) map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	require.NoError(t, err)

	out := make(map[string]struct{})
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
					continue
				}
				recv := fn.Recv.List[0].Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if ident, ok := recv.(*ast.Ident); !ok || ident.Name != "Server" {
					continue
				}
				out[fn.Name.Name] = struct{}{}
			}
		}
	}
	require.NotEmpty(t, out, "未解析到任何 Server 方法，源码解析可能失效")
	return out
}

// TestLogImportArchives_UnimplementedWithoutService 守护向后兼容路径：
// 未装配日志查询服务（老 Worker / 未启用日志平台）时仍回 Unimplemented，不 panic。
func TestLogImportArchives_UnimplementedWithoutService(t *testing.T) {
	srv := NewServer(process.NewManager(t.TempDir()), "fixture-node", nil, nil, nil)
	_, err := srv.LogImportArchives(context.Background(), &workerpb.LogImportArchivesRequest{})
	require.Error(t, err)
	require.Equal(t, codes.Unimplemented, status.Code(err))
}
