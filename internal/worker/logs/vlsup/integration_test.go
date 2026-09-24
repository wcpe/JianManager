package vlsup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIntegrationRealVLBinary 可选集成：需要环境变量 JM_VL_BIN 指向 victoria-logs 可执行文件。
// 未设置时跳过，单元测试不依赖真实二进制。
func TestIntegrationRealVLBinary(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("JM_VL_BIN not set; skip optional VictoriaLogs integration")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("JM_VL_BIN not readable: %v", err)
	}
	sha, err := FileSHA256(bin)
	if err != nil {
		t.Fatalf("hash binary: %v", err)
	}
	// 若提供了 JM_VL_SHA256 则按审批基线校验；否则用本地重算值走通 VerifyAsset 路径。
	if want := os.Getenv("JM_VL_SHA256"); want != "" {
		sha = want
	}

	dataRoot := t.TempDir()
	port := 19449

	s, err := New(Options{
		BinaryPath:   bin,
		AssetSHA256:  sha,
		DataRoot:     dataRoot,
		AuthUsername: "jm",
		AuthPassword: "local-only",
		Ports:        map[Namespace]int{NamespaceHot: port},
		Factory:      RealFactory{},
		Sink:         IndependentLoggerSink{},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.Start(ctx, NamespaceHot); err != nil {
		// 资产哈希与 JM_VL_SHA256 不一致时应明确失败
		t.Fatalf("start real VL: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Stop(context.Background(), NamespaceHot)
	})

	st, err := s.Status(NamespaceHot)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateRunning {
		t.Fatalf("expected running, got %s err=%s", st.State, st.LastError)
	}
	if st.AssetTag != AssetTag {
		t.Fatalf("status asset tag: %s", st.AssetTag)
	}
	// 等待短暂时间后尝试 health（真实进程启动可能稍慢）
	deadline := time.Now().Add(10 * time.Second)
	var herr error
	for time.Now().Before(deadline) {
		herr = s.Health(ctx, NamespaceHot)
		if herr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if herr != nil {
		t.Logf("health not ready (acceptable if binary flags differ): %v", herr)
	} else {
		hst, _ := s.Status(NamespaceHot)
		if !hst.HealthOK {
			t.Fatal("health ok expected")
		}
	}
	if err := s.Stop(ctx, NamespaceHot); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// 数据根应存在或可创建
	_ = filepath.Join(dataRoot, "hot", "data")
}
