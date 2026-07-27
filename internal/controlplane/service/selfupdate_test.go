package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/version"
	"github.com/wcpe/JianManager/proto/workerpb"
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
		// 预发布后缀剥离后同核视为「相同」（0.18.0-dev ≡ 0.18.0）
		{"0.18.0-dev", "0.18.0", false},
		{"0.18.0-dev", "v0.18.0", false},
	}
	for _, c := range cases {
		if got := versionDiffers(c.cur, c.latest); got != c.want {
			t.Fatalf("versionDiffers(%q,%q)=%v 期望 %v", c.cur, c.latest, got, c.want)
		}
	}
}

// TestVersionIsUpgrade 可升级判定：仅 latest 严格高于 current 才为 true（防 F1 降级误报）。
func TestVersionIsUpgrade(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"0.17.0", "0.18.0", true},
		{"0.17.0-dev", "v0.18.0", true},
		{"0.18.0", "0.18.0", false},
		{"0.18.0-dev", "0.18.0", false}, // 同核：dev 不算「可升」
		// 真机 F1：当前 0.18.0（或缓存里的 0.17.0-dev 被刷成 0.18.0 后）对 latest=v0.16.0 绝不可升
		{"0.18.0", "v0.16.0", false},
		{"0.17.0-dev", "v0.16.0", false},
		{"0.16.0", "v0.16.0", false},
		{"0.15.0", "v0.16.0", true},
		{"1.2", "1.2.0", false},
		{"1.2", "1.2.1", true},
		{"", "0.1.0", true}, // 空当前 + 有 latest：可升
		{"0.1.0", "", false},
	}
	for _, c := range cases {
		if got := versionIsUpgrade(c.cur, c.latest); got != c.want {
			t.Fatalf("versionIsUpgrade(%q,%q)=%v 期望 %v", c.cur, c.latest, got, c.want)
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
	// 注入 feed 桩：声明一个严格更高的版本 + CP 本平台制品。
	// 注意：不可用 version+"-next"（normalize 后同核，versionIsUpgrade=false）。
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{
			Version: "99.0.0",
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

// TestCheckUpdate_WithFeed_DoesNotOfferDowngrade feed 低于当前时 UpdateAvailable 必须为 false（F1）。
func TestCheckUpdate_WithFeed_DoesNotOfferDowngrade(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: "https://x"}, nil)
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{
			Version: "0.1.0",
			Artifacts: []FeedArtifact{
				{Component: ComponentControlPlane, OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "u", SHA256: "s"},
			},
		}, nil
	}
	res, err := svc.CheckUpdate(context.Background())
	require.NoError(t, err)
	require.True(t, res.ControlPlane.ArtifactAvailable)
	require.False(t, res.ControlPlane.UpdateAvailable,
		"current=%s latest=0.1.0 不得标可升级", version.Version)
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

// TestUpgradeControlPlane_RejectsDowngrade feed 低于当前时拒绝执行升级（F1 门闸）。
func TestUpgradeControlPlane_RejectsDowngrade(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: "https://x"}, nil)
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{
			Version: "0.1.0",
			Artifacts: []FeedArtifact{
				{Component: ComponentControlPlane, OS: runtime.GOOS, Arch: runtime.GOARCH, URL: "u", SHA256: "s"},
			},
		}, nil
	}
	// 若误走下载桩，测试应失败——降级必须在门闸处拦截。
	svc.cpUpgradeFn = func(_ *FeedArtifact) error {
		t.Fatal("降级路径不得调用 cpUpgradeFn")
		return nil
	}
	_, _, err := svc.UpgradeControlPlane(context.Background(), "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "拒绝降级")
}

func TestUpgradeControlPlane_NoArtifact(t *testing.T) {
	svc := NewSelfUpdateService(newSelfUpdateTestDB(t), cpgrpc.NewClientPool(), SelfUpdateConfig{FeedURL: "https://x"}, nil)
	svc.feedFetcher = func(_ context.Context) (*Feed, error) {
		return &Feed{Version: "99.0.0", Artifacts: nil}, nil // 无任何制品
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
	pool.SetWorkerClientForTest(n1.UUID, workerpb.NewWorkerServiceClient(nil))
	pool.SetWorkerClientForTest(n2.UUID, workerpb.NewWorkerServiceClient(nil))

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

// waitRolloutDone 轮询等待 rollout 收敛为 completed（超时 fail）。
func waitRolloutDone(t *testing.T, svc *SelfUpdateService) *Rollout {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if svc.RolloutSnapshot().State == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := svc.RolloutSnapshot()
	if snap.State != "completed" {
		t.Fatalf("rollout 应完成，实得 state=%s phase=%s", snap.State, snap.Phase)
	}
	return snap
}

// seedRolloutNodes 建 n 个在线节点（入库 + 登记 pool），按传入名称，返回节点切片。
func seedRolloutNodes(t *testing.T, db *gorm.DB, pool *cpgrpc.ClientPool, names ...string) []*model.Node {
	t.Helper()
	out := make([]*model.Node, 0, len(names))
	for i, name := range names {
		n := &model.Node{Name: name, Host: "127.0.0.1", GRPCPort: 1, WSPort: 2, Secret: string(rune('a' + i)), OS: runtime.GOOS, Arch: runtime.GOARCH, Status: model.NodeStatusOnline}
		require.NoError(t, db.Create(n).Error)
		pool.SetWorkerClientForTest(n.UUID, workerpb.NewWorkerServiceClient(nil))
		out = append(out, n)
	}
	return out
}

// TestRollout_CanarySucceedsThenRolling 验证金丝雀成功后进入滚动阶段、全部升级、Phase 递进至 completed（FR-155）。
func TestRollout_CanarySucceedsThenRolling(t *testing.T) {
	db := newSelfUpdateTestDB(t)
	pool := cpgrpc.NewClientPool()
	ns := seedRolloutNodes(t, db, pool, "n1", "n2", "n3", "n4")

	svc := NewSelfUpdateService(db, pool, SelfUpdateConfig{FeedURL: "https://x"}, nil)
	var upgraded []uint
	var mu sync.Mutex
	svc.nodeUpgradeFn = func(nodeID uint, _ string) (string, string, error) {
		mu.Lock()
		upgraded = append(upgraded, nodeID)
		mu.Unlock()
		return "0.7.0", "0.8.0", nil
	}

	ro, err := svc.StartRolloutWithOptions(context.Background(), nil, "0.8.0", "", RolloutOptions{CanarySize: 1, AbortOnCanaryFailure: true})
	require.NoError(t, err)
	// 启动即返回快照应带金丝雀阶段与配置回显。
	if ro.Phase != "canary" || ro.CanarySize != 1 {
		t.Fatalf("启动快照应 phase=canary canarySize=1，实得 phase=%s canarySize=%d", ro.Phase, ro.CanarySize)
	}

	snap := waitRolloutDone(t, svc)
	if snap.Phase != "completed" {
		t.Fatalf("金丝雀成功应最终 phase=completed，实得 %s", snap.Phase)
	}
	if snap.Total != 4 || snap.Succeeded != 4 || snap.Failed != 0 {
		t.Fatalf("应 4 全成功，实得 total=%d succeeded=%d failed=%d", snap.Total, snap.Succeeded, snap.Failed)
	}
	mu.Lock()
	got := len(upgraded)
	mu.Unlock()
	if got != 4 {
		t.Fatalf("金丝雀成功后应升级全部 4 个节点，实得 %d", got)
	}
	_ = ns
}

// TestRollout_CanaryFailsAborts 验证金丝雀失败 + abortOnCanaryFailure 时剩余节点不升级、Phase=aborted（FR-155）。
func TestRollout_CanaryFailsAborts(t *testing.T) {
	db := newSelfUpdateTestDB(t)
	pool := cpgrpc.NewClientPool()
	ns := seedRolloutNodes(t, db, pool, "n1", "n2", "n3", "n4")
	canaryID := ns[0].ID

	svc := NewSelfUpdateService(db, pool, SelfUpdateConfig{FeedURL: "https://x"}, nil)
	var upgraded []uint
	var mu sync.Mutex
	svc.nodeUpgradeFn = func(nodeID uint, _ string) (string, string, error) {
		mu.Lock()
		upgraded = append(upgraded, nodeID)
		mu.Unlock()
		if nodeID == canaryID {
			return "0.7.0", "0.7.0", errors.New("checksum mismatch") // 金丝雀失败
		}
		return "0.7.0", "0.8.0", nil
	}

	_, err := svc.StartRolloutWithOptions(context.Background(), nil, "0.8.0", "", RolloutOptions{CanarySize: 1, AbortOnCanaryFailure: true})
	require.NoError(t, err)

	snap := waitRolloutDone(t, svc)
	if snap.Phase != "aborted" {
		t.Fatalf("金丝雀失败 + 中止应 phase=aborted，实得 %s", snap.Phase)
	}
	if snap.Failed != 1 {
		t.Fatalf("应恰 1 个失败（金丝雀），实得 failed=%d", snap.Failed)
	}
	// 剩余 3 个节点应标 skipped 且从未被升级。
	skipped := 0
	for _, n := range snap.Nodes {
		if n.State == "skipped" {
			skipped++
		}
	}
	if skipped != 3 {
		t.Fatalf("剩余 3 节点应 skipped，实得 %d", skipped)
	}
	mu.Lock()
	got := len(upgraded)
	mu.Unlock()
	if got != 1 {
		t.Fatalf("中止后仅金丝雀被升级，实得升级 %d 个", got)
	}
}

// TestRollout_CanaryFailsNoAbortContinues 验证不设 abortOnCanaryFailure 时金丝雀失败仍继续滚动升级剩余（FR-155）。
func TestRollout_CanaryFailsNoAbortContinues(t *testing.T) {
	db := newSelfUpdateTestDB(t)
	pool := cpgrpc.NewClientPool()
	ns := seedRolloutNodes(t, db, pool, "n1", "n2", "n3")
	canaryID := ns[0].ID

	svc := NewSelfUpdateService(db, pool, SelfUpdateConfig{FeedURL: "https://x"}, nil)
	svc.nodeUpgradeFn = func(nodeID uint, _ string) (string, string, error) {
		if nodeID == canaryID {
			return "0.7.0", "0.7.0", errors.New("boom")
		}
		return "0.7.0", "0.8.0", nil
	}

	_, err := svc.StartRolloutWithOptions(context.Background(), nil, "0.8.0", "", RolloutOptions{CanarySize: 1, AbortOnCanaryFailure: false})
	require.NoError(t, err)

	snap := waitRolloutDone(t, svc)
	if snap.Phase != "completed" {
		t.Fatalf("不中止应最终 phase=completed，实得 %s", snap.Phase)
	}
	if snap.Succeeded != 2 || snap.Failed != 1 {
		t.Fatalf("金丝雀失败但继续：应 2 成功 1 失败，实得 succeeded=%d failed=%d", snap.Succeeded, snap.Failed)
	}
	for _, n := range snap.Nodes {
		if n.State == "skipped" {
			t.Fatal("不中止时不应有 skipped 节点")
		}
	}
}

// TestRollout_BatchSizeChunksRemaining 验证 batchSize 正确分批剩余节点、全部升级且 Phase 收敛（FR-155）。
func TestRollout_BatchSizeChunksRemaining(t *testing.T) {
	db := newSelfUpdateTestDB(t)
	pool := cpgrpc.NewClientPool()
	// 5 个节点：金丝雀 1 + 剩余 4，batchSize=2 → 剩余分 2 批（各 2 个）。
	seedRolloutNodes(t, db, pool, "n1", "n2", "n3", "n4", "n5")

	svc := NewSelfUpdateService(db, pool, SelfUpdateConfig{FeedURL: "https://x"}, nil)
	var upgraded []uint
	var mu sync.Mutex
	svc.nodeUpgradeFn = func(nodeID uint, _ string) (string, string, error) {
		mu.Lock()
		upgraded = append(upgraded, nodeID)
		mu.Unlock()
		return "0.7.0", "0.8.0", nil
	}

	ro, err := svc.StartRolloutWithOptions(context.Background(), nil, "0.8.0", "", RolloutOptions{CanarySize: 1, BatchSize: 2})
	require.NoError(t, err)
	if ro.BatchSize != 2 {
		t.Fatalf("启动快照应回显 batchSize=2，实得 %d", ro.BatchSize)
	}

	snap := waitRolloutDone(t, svc)
	if snap.Phase != "completed" {
		t.Fatalf("应最终 phase=completed，实得 %s", snap.Phase)
	}
	if snap.Succeeded != 5 {
		t.Fatalf("5 节点应全部升级成功，实得 succeeded=%d", snap.Succeeded)
	}
	// 金丝雀=batch1、剩余两批=batch2/3 → 结束时 CurrentBatch 应为 3。
	if snap.CurrentBatch != 3 {
		t.Fatalf("金丝雀1 + 剩余分2批，末批号应为 3，实得 currentBatch=%d", snap.CurrentBatch)
	}
	mu.Lock()
	got := len(upgraded)
	mu.Unlock()
	if got != 5 {
		t.Fatalf("应升级全部 5 个节点，实得 %d", got)
	}
}

// TestRollout_NoCanaryNoBatchBackCompat 验证零值 options（无金丝雀/分批）退化为原「串行全部」行为（FR-155 向后兼容）。
func TestRollout_NoCanaryNoBatchBackCompat(t *testing.T) {
	db := newSelfUpdateTestDB(t)
	pool := cpgrpc.NewClientPool()
	seedRolloutNodes(t, db, pool, "n1", "n2", "n3")

	svc := NewSelfUpdateService(db, pool, SelfUpdateConfig{FeedURL: "https://x"}, nil)
	svc.nodeUpgradeFn = func(_ uint, _ string) (string, string, error) { return "0.7.0", "0.8.0", nil }

	// 走原 StartRollout（无 options）路径。
	_, err := svc.StartRollout(context.Background(), nil, "0.8.0")
	require.NoError(t, err)

	snap := waitRolloutDone(t, svc)
	// 无金丝雀 → 直接滚动，剩余全部一批（CurrentBatch=2）。
	if snap.Phase != "completed" || snap.CanarySize != 0 {
		t.Fatalf("无金丝雀应 phase=completed canarySize=0，实得 phase=%s canarySize=%d", snap.Phase, snap.CanarySize)
	}
	if snap.Succeeded != 3 || snap.Failed != 0 {
		t.Fatalf("应 3 全成功，实得 succeeded=%d failed=%d", snap.Succeeded, snap.Failed)
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
