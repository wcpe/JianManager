package grpc

// 本用例验的是**端到端真机链路**：实例进程的 stdout/stderr 最终能在受管 VL 里查到。
//
// # 为什么它值得保留（尽管只在 CI 有 VL 二进制时才跑）
//
// 它横跨四层：gRPC 登记 → 进程输出回调 → 采集器落盘 → 采集器写入 VL → 查询栈读回。
// 这四层之间任何一处标签/路径/时序对不上，单元测试都发现不了——而现场表现是
// 「实例在跑、日志页空白」。
//
// # 2026-11-06 改写（旧采集链路删除后）
//
// 原实现用 `ingest.New(...)` + `AppendInstanceOutput`。现在改为走**新采集器**：
//   1. 进程输出 → `logtail.StdioBridge` 落成按天文件
//   2. `logtail.Runner` 扫描该文件并写入 VL
//   3. 查询栈按 `inst:<id>` 读回
//
// 这与生产装配的形态一致（`apps/worker/main.go` 的 `logTailCollector` 分支）。

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/logassemble"
	"github.com/wcpe/JianManager/internal/worker/logs/logconf"
	"github.com/wcpe/JianManager/internal/worker/logs/logtail"
	"github.com/wcpe/JianManager/internal/worker/logs/query"
	"github.com/wcpe/JianManager/internal/worker/logs/query/vlrange"
	"github.com/wcpe/JianManager/internal/worker/logs/vlsup"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// logTailCollectorAdapter 把新采集器接到 gRPC 的登记接口上（与 apps/worker 同形）。
type logTailCollectorAdapter struct{ runner *logtail.Runner }

func (c logTailCollectorAdapter) RegisterInstance(reg logconf.InstanceRegistration) error {
	b := c.runner.Bindings()
	if b == nil {
		return nil
	}
	return b.Put(logtail.BindingEntry{
		UUID: reg.UUID, TargetID: reg.TargetID, WorkDir: reg.WorkDir, Mode: reg.Mode,
	})
}

func (c logTailCollectorAdapter) AppendInstanceOutput(uuid, stream string, data []byte) error {
	// STDIO_PRIMARY 实例的日志只在管道里，落成文件交给同一个采集器。
	return c.runner.Stdio().Append(uuid, stream, data)
}

func TestRealProcessStdoutAndStderrReachManagedVL(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("set JM_VL_BIN and approved JM_VL_SHA256 for real integration")
	}
	sha := os.Getenv("JM_VL_SHA256")
	require.NotEmpty(t, sha)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	sup, err := vlsup.New(vlsup.Options{BinaryPath: bin, AssetSHA256: sha, DataRoot: t.TempDir(),
		AuthUsername: "fixture", AuthPassword: "fixture-only", Ports: map[vlsup.Namespace]int{vlsup.NamespaceHot: port},
		MemoryAllowedBytes: map[vlsup.Namespace]int64{vlsup.NamespaceHot: 64 << 20}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	require.NoError(t, sup.Start(ctx, vlsup.NamespaceHot))
	t.Cleanup(func() { require.NoError(t, sup.Stop(context.Background(), vlsup.NamespaceHot)) })
	vl, err := sup.ClientFor(vlsup.NamespaceHot)
	require.NoError(t, err)
	ranges, err := vlrange.New(vl)
	require.NoError(t, err)
	stack := logassemble.Build(nil, ranges, "real-instance-acquisition")

	// 新采集器：stdio 落盘目录 + 绑定表都由临时目录承载。
	root := t.TempDir()
	stdioRoot := root + "/stdio"
	runner, err := logtail.Start(ctx, logtail.Config{
		Enabled:      true,
		StdioRoot:    stdioRoot,
		RegistryPath: root + "/registry.jsonl",
		// 采集周期缩短：集成用例不该等 10s 的默认扫描。
		ScanInterval:     200 * time.Millisecond,
		ProgressInterval: 100 * time.Millisecond,
		Catalog:          stack.Catalog,
	}, vl, nil)
	require.NoError(t, err)
	t.Cleanup(runner.Stop)

	collector := logTailCollectorAdapter{runner: runner}
	manager := process.NewManager(t.TempDir())
	manager.SetMemGuard(process.MemGuardConfig{ReserveMB: 128})
	manager.SetOutputHandler(func(id, stream string, data []byte) {
		if err := collector.AppendInstanceOutput(id, stream, data); err != nil {
			t.Errorf("stdio bridge: %v", err)
		}
	})
	srv := NewServer(manager, "fixture-node", nil, nil, nil)
	srv.SetInstanceLogCollector(collector)
	srv.SetLogQueryService(stack.LogRPC)
	command := "printf 'instance-stdout\\n'; printf 'instance-stderr\\n' >&2"
	if runtime.GOOS == "windows" {
		command = "echo instance-stdout & echo instance-stderr 1>&2"
	}
	created, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{InstanceUuid: "real-process", Name: "fixture", ProcessType: "direct",
		StartCommand: command, WorkDir: t.TempDir(), LogTargetId: "inst:7", LogAcquireMode: "STDIO_PRIMARY", LogSourceGeneration: "holder-1"})
	require.NoError(t, err)
	require.True(t, created.Success, created.Error)
	started, err := srv.StartInstance(ctx, &workerpb.InstanceActionRequest{InstanceUuid: "real-process"})
	require.NoError(t, err)
	require.True(t, started.Success, started.Error)

	require.Eventually(t, func() bool {
		result := stack.Query.Search(ctx, query.QueryRequest{AuthorizedTargets: []string{"inst:7"}, Budget: query.Budget{Limit: 10}})
		if result.Err != nil || !result.Coverage.Complete || len(result.Items) < 2 {
			return false
		}
		got := map[string]string{}
		for _, ev := range result.Items {
			got[ev.Stream] = strings.TrimSpace(ev.Message)
		}
		return got["stdout"] == "instance-stdout" && got["stderr"] == "instance-stderr"
	}, 40*time.Second, 300*time.Millisecond)
}
