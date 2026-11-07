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
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/wcpe/JianManager/proto/workerpb"
)

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

