package service

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/version"
)

// newCacheTestDB 建带 Node + SelfUpdateCheckCache 表的内存库。
func newCacheTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newSelfUpdateTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.SelfUpdateCheckCache{}))
	return db
}

// TestCachedCheck_EmptyReturnsNotCached 无缓存行时 GET 缓存返回 cached=false 的空结果（让前端触发刷新）。
func TestCachedCheck_EmptyReturnsNotCached(t *testing.T) {
	svc := NewSelfUpdateService(newCacheTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{GitHubRepo: "owner/repo"}, nil)
	res, err := svc.CachedCheck(context.Background())
	require.NoError(t, err)
	require.NotNil(t, res)
	if res.Cached {
		t.Fatal("无缓存应 cached=false")
	}
	if res.CheckedAt != nil {
		t.Fatal("无缓存 checkedAt 应为 nil")
	}
	// 即便没缓存，也带上当前是否已配置（前端据此提示）。
	if !res.Configured {
		t.Fatal("已配 github_repo，configured 应为 true")
	}
}

// TestRefreshCheck_PersistsCache refresh 成功后写缓存；随后 GET 缓存返回同一结果且 cached=true、不触发 live。
func TestRefreshCheck_PersistsCache(t *testing.T) {
	db := newCacheTestDB(t)
	svc := NewSelfUpdateService(db, cpgrpc.NewClientPool(), SelfUpdateConfig{GitHubRepo: "owner/repo"}, nil)
	var liveCalls int
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		liveCalls++
		return &Feed{
			Version: version.Version + "-next",
			Notes:   "# 标题\n- 项目",
			Artifacts: []FeedArtifact{
				{Component: ComponentControlPlane, OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "u", SHA256: "s"},
			},
		}, nil
	}

	got, err := svc.RefreshCheck(context.Background())
	require.NoError(t, err)
	if !got.Cached {
		t.Fatal("refresh 成功的返回应标 cached=true")
	}
	if got.CheckedAt == nil {
		t.Fatal("refresh 成功应带 checkedAt")
	}
	if liveCalls != 1 {
		t.Fatalf("refresh 应触发恰好一次 live，实得 %d", liveCalls)
	}

	// 缓存行已写。
	var cnt int64
	require.NoError(t, db.Model(&model.SelfUpdateCheckCache{}).Count(&cnt).Error)
	if cnt != 1 {
		t.Fatalf("应写入 1 行缓存，实得 %d", cnt)
	}

	// GET 缓存：返回同结果、cached=true，且不再触发 live。
	cached, err := svc.CachedCheck(context.Background())
	require.NoError(t, err)
	if !cached.Cached || cached.CheckedAt == nil {
		t.Fatal("有缓存应 cached=true 且带 checkedAt")
	}
	if cached.LatestVersion != got.LatestVersion || cached.Notes != got.Notes {
		t.Fatalf("缓存内容应与 refresh 一致: %+v vs %+v", cached, got)
	}
	if liveCalls != 1 {
		t.Fatalf("GET 缓存不应触发 live，liveCalls 仍应为 1，实得 %d", liveCalls)
	}
}

// TestRefreshCheck_FailureKeepsCache refresh 失败不清已有缓存（断网/限流后仍能回显上次结果）。
func TestRefreshCheck_FailureKeepsCache(t *testing.T) {
	db := newCacheTestDB(t)
	svc := NewSelfUpdateService(db, cpgrpc.NewClientPool(), SelfUpdateConfig{GitHubRepo: "owner/repo"}, nil)

	// 先成功写一次缓存。
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{Version: "9.9.9", Notes: "old notes"}, nil
	}
	first, err := svc.RefreshCheck(context.Background())
	require.NoError(t, err)
	require.Equal(t, "9.9.9", first.LatestVersion)

	// 再次 refresh 失败（模拟限流）。
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return nil, ErrUpdateRateLimited
	}
	_, err = svc.RefreshCheck(context.Background())
	if !errors.Is(err, ErrUpdateRateLimited) {
		t.Fatalf("应透出 ErrUpdateRateLimited，实得 %v", err)
	}

	// 缓存仍是上次成功结果，未被清。
	cached, err := svc.CachedCheck(context.Background())
	require.NoError(t, err)
	if !cached.Cached || cached.LatestVersion != "9.9.9" || cached.Notes != "old notes" {
		t.Fatalf("失败不应清缓存，应仍为上次成功结果，实得 %+v", cached)
	}
}

// TestRefreshCheck_Unconfigured 未配源 refresh 仍透出 CheckResult（configured=false，不报错），并写缓存。
func TestRefreshCheck_Unconfigured(t *testing.T) {
	svc := NewSelfUpdateService(newCacheTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	got, err := svc.RefreshCheck(context.Background())
	require.NoError(t, err)
	if got.Configured {
		t.Fatal("未配源 configured 应为 false")
	}
	if !got.Cached || got.CheckedAt == nil {
		t.Fatal("未配源的成功检查同样应写缓存并标 cached=true")
	}
}
