package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/platform/selfupdate"
	"github.com/wcpe/JianManager/internal/version"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// waitRestarted 轮询等待重启回调发生（升级/回滚均异步延迟重启）。
func waitRestarted(t *testing.T, mu *sync.Mutex, flag *bool) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := *flag
		mu.Unlock()
		if done {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestUpgradeWorker_BacksUpBeforeReplace 升级成功后，升级前的二进制已被备份（供回滚）。
func TestUpgradeWorker_BacksUpBeforeReplace(t *testing.T) {
	srv := newSelfUpdateTestServer(t)
	fakeExe := filepath.Join(t.TempDir(), "worker-bin")
	if err := os.WriteFile(fakeExe, []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.SetExecutablePath(fakeExe)
	srv.SetRestartFunc(func() {})

	body := []byte("NEW-BINARY")
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
	if err != nil || !resp.Success {
		t.Fatalf("升级应成功: err=%v resp=%+v", err, resp)
	}
	// 升级前内容（OLD-BINARY，版本=当前 version.Version）应已备份。
	meta, ok := selfupdate.BackupInfo(selfupdate.ComponentWorker, srv.root)
	if !ok {
		t.Fatal("升级后应存在升级前备份")
	}
	if meta.Version != version.Version {
		t.Fatalf("备份版本应为升级前版本 %s，实得 %q", version.Version, meta.Version)
	}
	if meta.SHA256 != sha256Hex([]byte("OLD-BINARY")) {
		t.Fatalf("备份应为升级前二进制内容")
	}
}

// TestUpgradeWorker_Rollback_RestoresBackup action=rollback 把二进制换回本地备份并重启。
func TestUpgradeWorker_Rollback_RestoresBackup(t *testing.T) {
	srv := newSelfUpdateTestServer(t)
	fakeExe := filepath.Join(t.TempDir(), "worker-bin")
	if err := os.WriteFile(fakeExe, []byte("OLD-GOOD"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.SetExecutablePath(fakeExe)

	// 先造一份备份（模拟升级前），再把当前二进制改为「坏新版」。
	if err := selfupdate.BackupCurrentFrom(selfupdate.ComponentWorker, "0.8.0", fakeExe, srv.root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fakeExe, []byte("NEW-BROKEN"), 0o755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	restarted := false
	srv.SetRestartFunc(func() { mu.Lock(); restarted = true; mu.Unlock() })

	resp, err := srv.UpgradeWorker(context.Background(), &workerpb.UpgradeWorkerRequest{Action: "rollback"})
	if err != nil {
		t.Fatalf("回滚不应返回 gRPC error: %v", err)
	}
	if !resp.Success {
		t.Fatalf("回滚应成功，实得 error=%s", resp.Error)
	}
	got, _ := os.ReadFile(fakeExe)
	if string(got) != "OLD-GOOD" {
		t.Fatalf("回滚后应换回备份的旧二进制，实得 %q", got)
	}
	if !waitRestarted(t, &mu, &restarted) {
		t.Fatal("回滚成功后应触发重启")
	}
}

// TestUpgradeWorker_Rollback_NoBackup 无备份时回滚 success=false 且不重启。
func TestUpgradeWorker_Rollback_NoBackup(t *testing.T) {
	srv := newSelfUpdateTestServer(t)
	fakeExe := filepath.Join(t.TempDir(), "worker-bin")
	if err := os.WriteFile(fakeExe, []byte("CURRENT"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv.SetExecutablePath(fakeExe)
	restarted := false
	srv.SetRestartFunc(func() { restarted = true })

	resp, err := srv.UpgradeWorker(context.Background(), &workerpb.UpgradeWorkerRequest{Action: "rollback"})
	if err != nil {
		t.Fatalf("回滚不应返回 gRPC error: %v", err)
	}
	if resp.Success {
		t.Fatal("无备份回滚应 success=false")
	}
	if resp.Error == "" {
		t.Fatal("无备份回滚应有 error 文案")
	}
	// 目标二进制不应被改动。
	got, _ := os.ReadFile(fakeExe)
	if string(got) != "CURRENT" {
		t.Fatalf("无备份回滚不应改动目标，实得 %q", got)
	}
	if restarted {
		t.Fatal("无备份回滚不应触发重启")
	}
}

// TestGetVersion_ReportsBackupVersion GetVersion 透出本地备份版本（有备份）。
func TestGetVersion_ReportsBackupVersion(t *testing.T) {
	srv := newSelfUpdateTestServer(t)
	// 无备份时 backup_version 为空。
	resp, err := srv.GetVersion(context.Background(), &workerpb.GetVersionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.BackupVersion != "" {
		t.Fatalf("无备份时 backup_version 应为空，实得 %q", resp.BackupVersion)
	}

	// 造一份备份后应透出其版本。
	fakeExe := filepath.Join(t.TempDir(), "worker-bin")
	if err := os.WriteFile(fakeExe, []byte("X"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := selfupdate.BackupCurrentFrom(selfupdate.ComponentWorker, "0.7.7", fakeExe, srv.root); err != nil {
		t.Fatal(err)
	}
	resp, err = srv.GetVersion(context.Background(), &workerpb.GetVersionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.BackupVersion != "0.7.7" {
		t.Fatalf("backup_version 应为 0.7.7，实得 %q", resp.BackupVersion)
	}
}
