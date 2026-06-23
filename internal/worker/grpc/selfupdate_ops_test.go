package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/version"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// newSelfUpdateTestServer 构造一个带临时数据根的 Worker Server（cache 目录可用）。
func newSelfUpdateTestServer(t *testing.T) *Server {
	t.Helper()
	root, err := dataroot.Init(t.TempDir())
	if err != nil {
		t.Fatalf("初始化数据根失败: %v", err)
	}
	return NewServer(process.NewManager(t.TempDir()), "test-node", nil, nil, root)
}

func TestGetVersion(t *testing.T) {
	srv := newSelfUpdateTestServer(t)
	resp, err := srv.GetVersion(context.Background(), &workerpb.GetVersionRequest{})
	if err != nil {
		t.Fatalf("GetVersion 失败: %v", err)
	}
	if resp.Version != version.Version {
		t.Fatalf("版本不符: 期望 %s 实得 %s", version.Version, resp.Version)
	}
	if resp.Os != runtime.GOOS || resp.Arch != runtime.GOARCH {
		t.Fatalf("平台不符: 期望 %s/%s 实得 %s/%s", runtime.GOOS, runtime.GOARCH, resp.Os, resp.Arch)
	}
}

func TestUpgradeWorker_EmptyURL(t *testing.T) {
	srv := newSelfUpdateTestServer(t)
	resp, err := srv.UpgradeWorker(context.Background(), &workerpb.UpgradeWorkerRequest{})
	if err != nil {
		t.Fatalf("UpgradeWorker 不应返回 gRPC error: %v", err)
	}
	if resp.Success {
		t.Fatal("空下载地址应 success=false")
	}
}

func TestUpgradeWorker_ChecksumMismatch_NoReplace(t *testing.T) {
	srv := newSelfUpdateTestServer(t)

	// 准备一个「假二进制」作为待替换目标，断言校验失败时它不被改动。
	fakeExe := filepath.Join(t.TempDir(), "worker-bin")
	if err := os.WriteFile(fakeExe, []byte("ORIGINAL"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.SetExecutablePath(fakeExe)
	restarted := false
	srv.SetRestartFunc(func() { restarted = true })

	body := []byte("new-binary")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	resp, err := srv.UpgradeWorker(context.Background(), &workerpb.UpgradeWorkerRequest{
		DownloadUrl:   ts.URL,
		Sha256:        sha256Hex([]byte("WRONG")), // 故意不匹配
		TargetVersion: "9.9.9",
		AllowInsecure: true, // httptest 是 http://
	})
	if err != nil {
		t.Fatalf("UpgradeWorker 不应返回 gRPC error: %v", err)
	}
	if resp.Success {
		t.Fatal("校验不符应 success=false")
	}
	// 目标二进制绝不能被改动。
	got, _ := os.ReadFile(fakeExe)
	if string(got) != "ORIGINAL" {
		t.Fatalf("校验失败后目标二进制不应改动，实得 %q", got)
	}
	if restarted {
		t.Fatal("校验失败不应触发重启")
	}
}

func TestUpgradeWorker_Success_ReplacesAndRestarts(t *testing.T) {
	srv := newSelfUpdateTestServer(t)

	fakeExe := filepath.Join(t.TempDir(), "worker-bin")
	if err := os.WriteFile(fakeExe, []byte("OLD-VERSION"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.SetExecutablePath(fakeExe)

	var mu sync.Mutex
	restarted := false
	srv.SetRestartFunc(func() {
		mu.Lock()
		restarted = true
		mu.Unlock()
	})

	body := []byte("NEW-VERSION-BINARY")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer ts.Close()

	resp, err := srv.UpgradeWorker(context.Background(), &workerpb.UpgradeWorkerRequest{
		DownloadUrl:   ts.URL,
		Sha256:        sha256Hex(body),
		TargetVersion: "9.9.9",
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatalf("UpgradeWorker 失败: %v", err)
	}
	if !resp.Success {
		t.Fatalf("应升级成功，实得 error=%s", resp.Error)
	}
	if resp.FromVersion != version.Version {
		t.Fatalf("fromVersion 应为当前版本 %s，实得 %s", version.Version, resp.FromVersion)
	}
	// 目标二进制应被替换为新内容。
	got, _ := os.ReadFile(fakeExe)
	if string(got) != string(body) {
		t.Fatalf("替换后目标应为新二进制，实得 %q", got)
	}
	// 重启异步延迟触发（restartDelay 800ms），等待其发生。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := restarted
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !restarted {
		t.Fatal("替换成功后应触发重启")
	}
}
