//go:build e2e

// 真机/联网冒烟测试：验证 CoreService 能从真实 PaperMC API 解析版本与构建。
// 默认构建与 `go test ./...` 不包含本文件（受 e2e 构建标签保护），需显式：
//
//	go test -tags e2e -run Live -v ./internal/controlplane/service/
package service

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestCoreService_LivePaperResolve 打通「列版本 → 解析最新构建 → 下载地址可达」整条核心解析链。
func TestCoreService_LivePaperResolve(t *testing.T) {
	svc := NewCoreService()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	versions, err := svc.ListVersions(ctx, "paper")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) == 0 {
		t.Fatal("未取到任何 Paper 版本")
	}
	// ListVersions 已反转为新→旧，故首元素即最新版本。
	latest := versions[0]
	t.Logf("Paper 版本数=%d，最新=%s，最旧=%s", len(versions), latest, versions[len(versions)-1])

	info, err := svc.ResolveBuild(ctx, "paper", latest, 0)
	if err != nil {
		t.Fatalf("ResolveBuild(%s, latest): %v", latest, err)
	}
	if info.DownloadURL == "" || info.SHA256 == "" || info.Filename == "" {
		t.Fatalf("解析结果不完整: %+v", info)
	}
	t.Logf("解析成功: build=%d file=%s sha256=%s", info.Build, info.Filename, info.SHA256)
	t.Logf("下载地址: %s", info.DownloadURL)

	// 用 HEAD 确认下载地址真实可达（不拉取整包）。
	req, _ := http.NewRequestWithContext(ctx, http.MethodHead, info.DownloadURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD 下载地址失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("下载地址不可达, 状态码=%d", resp.StatusCode)
	}
	t.Logf("下载地址可达 (200)，Content-Length=%d", resp.ContentLength)
}
