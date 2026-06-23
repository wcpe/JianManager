package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/version"
)

func newSelfUpdateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Node{}))
	return db
}

func TestSelectArtifact(t *testing.T) {
	feed := &Feed{
		Version: "1.0.0",
		Artifacts: []FeedArtifact{
			{Component: "control-plane", OS: "linux", Arch: "amd64", URL: "u1", SHA256: "s1"},
			{Component: "worker", OS: "windows", Arch: "amd64", URL: "u2", SHA256: "s2"},
		},
	}
	if a, ok := SelectArtifact(feed, "worker", "windows", "amd64"); !ok || a.URL != "u2" {
		t.Fatalf("应匹配到 worker/windows/amd64，实得 ok=%v", ok)
	}
	if _, ok := SelectArtifact(feed, "worker", "linux", "amd64"); ok {
		t.Fatal("无匹配制品应返回 false")
	}
	if _, ok := SelectArtifact(nil, "worker", "linux", "amd64"); ok {
		t.Fatal("nil feed 应返回 false")
	}
}

func TestVersionDiffers(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"0.7.0", "0.8.0", true},
		{"0.8.0", "0.8.0", false},
		{"v0.8.0", "0.8.0", false}, // v 前缀归一
		{"0.7.0", "", false},       // 空最新版本不视为有更新
	}
	for _, c := range cases {
		if got := versionDiffers(c.cur, c.latest); got != c.want {
			t.Fatalf("versionDiffers(%q,%q)=%v 期望 %v", c.cur, c.latest, got, c.want)
		}
	}
}

func TestFetchFeed_NotConfigured(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	if svc.Configured() {
		t.Fatal("空 feed_url 应为未配置")
	}
	_, err := svc.FetchFeed(context.Background())
	if !errors.Is(err, ErrUpdateNotConfigured) {
		t.Fatalf("未配源应返回 ErrUpdateNotConfigured，实得 %v", err)
	}
}

func TestFetchFeed_ParsesJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2.0.0","notes":"hi","artifacts":[{"component":"worker","os":"linux","arch":"amd64","url":"u","sha256":"s"}]}`))
	}))
	defer ts.Close()
	// httptest 是 http://，须 allow_insecure。
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: ts.URL, AllowInsecure: true}, nil)
	feed, err := svc.FetchFeed(context.Background())
	require.NoError(t, err)
	if feed.Version != "2.0.0" || len(feed.Artifacts) != 1 {
		t.Fatalf("feed 解析不符: %+v", feed)
	}
}

func TestFetchFeed_RejectsInsecure(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: "http://x/feed.json"}, nil)
	_, err := svc.FetchFeed(context.Background())
	if err == nil {
		t.Fatal("非 https feed 且未允许应拒绝")
	}
}

func TestCheckUpdate_NotConfigured(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	res, err := svc.CheckUpdate(context.Background())
	require.NoError(t, err)
	if res.Configured {
		t.Fatal("未配源 configured 应为 false")
	}
	if res.ControlPlane.CurrentVersion != version.Version {
		t.Fatalf("CP 当前版本应为 %s", version.Version)
	}
	if res.ControlPlane.UpdateAvailable {
		t.Fatal("未配源 CP 不应提示有更新")
	}
}

func TestCheckUpdate_WithFeed_CPUpdateAvailable(t *testing.T) {
	db := newSelfUpdateTestDB(t)
	svc := NewSelfUpdateService(db, cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: "https://x"}, nil)
	// 注入 feed 桩：声明一个更高版本 + CP 本平台制品。
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{
			Version: version.Version + "-next",
			Artifacts: []FeedArtifact{
				{Component: ComponentControlPlane, OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "u", SHA256: "s"},
			},
		}, nil
	}
	res, err := svc.CheckUpdate(context.Background())
	require.NoError(t, err)
	if !res.Configured {
		t.Fatal("configured 应为 true")
	}
	if !res.ControlPlane.ArtifactAvailable {
		t.Fatal("应有 CP 本平台制品")
	}
	if !res.ControlPlane.UpdateAvailable {
		t.Fatal("更高版本 + 有制品应提示 CP 有更新")
	}
}

func TestUpgradeControlPlane_AlreadyLatest(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: "https://x"}, nil)
	// feed 版本与当前一致 → 已最新。
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{
			Version: version.Version,
			Artifacts: []FeedArtifact{
				{Component: ComponentControlPlane, OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "u", SHA256: "s"},
			},
		}, nil
	}
	_, _, err := svc.UpgradeControlPlane(context.Background(), "")
	if !errors.Is(err, ErrUpdateAlreadyLatest) {
		t.Fatalf("应返回 ErrUpdateAlreadyLatest，实得 %v", err)
	}
}

func TestUpgradeControlPlane_NoArtifact(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: "https://x"}, nil)
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{Version: version.Version + "-next", Artifacts: nil}, nil // 无任何制品
	}
	_, _, err := svc.UpgradeControlPlane(context.Background(), "")
	if !errors.Is(err, ErrUpdateNoArtifact) {
		t.Fatalf("无本平台制品应返回 ErrUpdateNoArtifact，实得 %v", err)
	}
}

// TestRollout_MixedResults 验证全网逐节点编排：成功/失败隔离 + 聚合计数 + 完成态。
func TestRollout_MixedResults(t *testing.T) {
	db := newSelfUpdateTestDB(t)
	pool := cpgrpc.NewClientPool()
	// 三个节点入库；前两个登记到 pool（视为在线），第三个不在 pool（离线，不应被选中）。
	n1 := &model.Node{Name: "n1", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "a", OS: runtime.GOOS, Arch: runtime.GOARCH, Status: model.NodeStatusOnline}
	n2 := &model.Node{Name: "n2", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "b", OS: runtime.GOOS, Arch: runtime.GOARCH, Status: model.NodeStatusOnline}
	n3 := &model.Node{Name: "n3", Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: "c", OS: runtime.GOOS, Arch: runtime.GOARCH, Status: model.NodeStatusOnline}
	require.NoError(t, db.Create(n1).Error)
	require.NoError(t, db.Create(n2).Error)
	require.NoError(t, db.Create(n3).Error)
	// grpc.NewClient 是惰性连接（首次 RPC 才拨号），故 Connect 仅登记不实际连。
	require.NoError(t, pool.Connect(n1.UUID, "127.0.0.1:1"))
	require.NoError(t, pool.Connect(n2.UUID, "127.0.0.1:1"))

	svc := NewSelfUpdateService(db, pool, SelfUpdateConfig{FeedURL: "https://x"}, nil)
	// 注入单节点升级桩：n1 成功，n2 失败。
	svc.nodeUpgradeFn = func(nodeID uint, _ string) (string, string, error) {
		if nodeID == n2.ID {
			return "0.7.0", "0.8.0", errors.New("checksum mismatch")
		}
		return "0.7.0", "0.8.0", nil
	}

	_, err := svc.StartRollout(context.Background(), nil, "0.8.0")
	require.NoError(t, err)

	// 等待 rollout 完成。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.RolloutSnapshot().State == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap := svc.RolloutSnapshot()
	if snap.State != "completed" {
		t.Fatalf("rollout 应完成，实得 state=%s", snap.State)
	}
	if snap.Total != 2 {
		t.Fatalf("应只选中 2 个在线节点，实得 total=%d", snap.Total)
	}
	if snap.Succeeded != 1 || snap.Failed != 1 {
		t.Fatalf("应 1 成功 1 失败，实得 succeeded=%d failed=%d", snap.Succeeded, snap.Failed)
	}
	// 失败节点保留 error，不影响成功节点。
	var sawFailErr bool
	for _, n := range snap.Nodes {
		if n.NodeID == n2.ID {
			if n.State != "failed" || n.Error == "" {
				t.Fatalf("n2 应失败且有 error，实得 state=%s error=%q", n.State, n.Error)
			}
			sawFailErr = true
		}
		if n.NodeID == n1.ID && n.State != "succeeded" {
			t.Fatalf("n1 应成功，实得 %s", n.State)
		}
	}
	if !sawFailErr {
		t.Fatal("未见到 n2 失败状态")
	}
}

func TestRollout_NotConfigured(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	_, err := svc.StartRollout(context.Background(), nil, "")
	if !errors.Is(err, ErrUpdateNotConfigured) {
		t.Fatalf("未配源 rollout 应返回 ErrUpdateNotConfigured，实得 %v", err)
	}
}

func TestRolloutSnapshot_Idle(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{}, nil)
	if svc.RolloutSnapshot().State != "idle" {
		t.Fatal("从未发起 rollout 应为 idle")
	}
}
