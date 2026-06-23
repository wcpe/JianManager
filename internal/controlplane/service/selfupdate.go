package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/internal/platform/selfupdate"
	"github.com/wcpe/JianManager/internal/version"
	"github.com/wcpe/JianManager/proto/workerpb"
)

var (
	// ErrUpdateNotConfigured 未配置更新源（feed_url 为空），无法检查/执行更新。
	ErrUpdateNotConfigured = errors.New("未配置更新源")
	// ErrUpdateNoArtifact feed 中无匹配目标平台（component+os+arch）的制品。
	ErrUpdateNoArtifact = errors.New("更新源无匹配本平台的制品")
	// ErrUpdateAlreadyLatest 目标已是最新版本（无更高版本可升）。
	ErrUpdateAlreadyLatest = errors.New("已是最新版本")
)

// 自更新组件标识（feed artifact.component 取值）。
const (
	ComponentControlPlane = "control-plane"
	ComponentWorker       = "worker"
)

// Feed 是 release feed 的 JSON 结构（FR-081，见 spec api.md 契约）。
type Feed struct {
	Version   string         `json:"version"`
	Notes     string         `json:"notes"`
	Artifacts []FeedArtifact `json:"artifacts"`
}

// FeedArtifact 是 feed 中单个平台制品的下载信息。
type FeedArtifact struct {
	Component string `json:"component"` // control-plane | worker
	OS        string `json:"os"`        // runtime.GOOS
	Arch      string `json:"arch"`      // runtime.GOARCH
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
}

// SelfUpdateConfig 是注入自更新服务的更新源配置（对齐 config.UpdateConfig）。
type SelfUpdateConfig struct {
	FeedURL       string
	BinaryBaseURL string
	AllowInsecure bool
}

// SelfUpdateService 面板自更新编排（FR-081，见 ADR-020 §4）。
// CP 统一编排：检查更新（CP + 各节点版本对比）、CP 自升级、经 gRPC 令 Worker 升级、全网逐节点编排。
type SelfUpdateService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
	cfg  SelfUpdateConfig
	root *dataroot.Root

	// rolloutMu 保护 rollout（全网编排为单例：同一时刻只跑一次全网升级）。
	rolloutMu sync.Mutex
	rollout   *Rollout

	// 以下为可注入测试桩；生产为 nil 时走真实现。
	feedFetcher   func(ctx context.Context) (*Feed, error)         // 覆盖 FetchFeed
	cpUpgradeFn   func(art *FeedArtifact) error                    // 覆盖 CP 自身下载替换重启
	restartCPFn   func()                                           // 覆盖 CP 重启动作
	nodeUpgradeFn func(nodeID uint, wantVersion string) (string, string, error) // 覆盖 rollout 内单节点升级
}

// NewSelfUpdateService 创建自更新服务。root 用于 CP 自身下载落 cache/，可为 nil（回退临时目录）。
func NewSelfUpdateService(db *gorm.DB, pool *cpgrpc.ClientPool, cfg SelfUpdateConfig, root *dataroot.Root) *SelfUpdateService {
	return &SelfUpdateService{db: db, pool: pool, cfg: cfg, root: root}
}

// Configured 报告是否已配置更新源（feed_url 非空）。
func (s *SelfUpdateService) Configured() bool {
	return strings.TrimSpace(s.cfg.FeedURL) != ""
}

// FetchFeed 拉取并解析 release feed。未配 feed_url 返回 ErrUpdateNotConfigured。
func (s *SelfUpdateService) FetchFeed(ctx context.Context) (*Feed, error) {
	if s.feedFetcher != nil {
		return s.feedFetcher(ctx)
	}
	if !s.Configured() {
		return nil, ErrUpdateNotConfigured
	}
	if !s.cfg.AllowInsecure && !strings.HasPrefix(strings.ToLower(s.cfg.FeedURL), "https://") {
		return nil, selfupdate.ErrInsecureURL
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.FeedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造 feed 请求失败: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取更新源失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取更新源失败: HTTP %d", resp.StatusCode)
	}
	var feed Feed
	if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
		return nil, fmt.Errorf("解析更新源失败: %w", err)
	}
	return &feed, nil
}

// SelectArtifact 从 feed 精确匹配 component+os+arch 的制品。无匹配返回 (nil,false)。
func SelectArtifact(feed *Feed, component, goos, goarch string) (*FeedArtifact, bool) {
	if feed == nil {
		return nil, false
	}
	for i := range feed.Artifacts {
		a := &feed.Artifacts[i]
		if strings.EqualFold(a.Component, component) &&
			strings.EqualFold(a.OS, goos) &&
			strings.EqualFold(a.Arch, goarch) {
			return a, true
		}
	}
	return nil, false
}

// versionDiffers 报告 current 与 latest 是否不同（视为「有更新」）。
// 采用「字符串不等即有更新」的保守语义：版本号格式多样（含/不含 v 前缀、预发布后缀），
// 简单不等判定足够驱动「是否提示升级」，真正是否升级由运营手动确认（不自动放量）。
func versionDiffers(current, latest string) bool {
	c := strings.TrimSpace(strings.TrimPrefix(current, "v"))
	l := strings.TrimSpace(strings.TrimPrefix(latest, "v"))
	return l != "" && c != l
}

// ComponentStatus 是检查更新里单个组件（CP 或某节点）的版本对比结果。
type ComponentStatus struct {
	NodeID            uint   `json:"nodeId,omitempty"`
	NodeUUID          string `json:"nodeUuid,omitempty"`
	Name              string `json:"name,omitempty"`
	Online            bool   `json:"online"`
	CurrentVersion    string `json:"currentVersion"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	UpdateAvailable   bool   `json:"updateAvailable"`
	ArtifactAvailable bool   `json:"artifactAvailable"`
}

// CheckResult 是 GET /self-update/check 的返回。
type CheckResult struct {
	Configured    bool              `json:"configured"`
	LatestVersion string            `json:"latestVersion"`
	Notes         string            `json:"notes"`
	ControlPlane  ComponentStatus   `json:"controlPlane"`
	Nodes         []ComponentStatus `json:"nodes"`
}

// CheckUpdate 组装 CP 自身 + 各节点的版本对比。未配源时返回 configured=false 的结果（不报错）。
func (s *SelfUpdateService) CheckUpdate(ctx context.Context) (*CheckResult, error) {
	res := &CheckResult{
		Configured: s.Configured(),
		ControlPlane: ComponentStatus{
			CurrentVersion: version.Version,
			OS:             runtime.GOOS,
			Arch:           runtime.GOARCH,
			Online:         true,
		},
	}

	var feed *Feed
	if s.Configured() {
		f, err := s.FetchFeed(ctx)
		if err != nil {
			return nil, err
		}
		feed = f
		res.LatestVersion = feed.Version
		res.Notes = feed.Notes
		// CP 自身对比。
		if art, ok := SelectArtifact(feed, ComponentControlPlane, runtime.GOOS, runtime.GOARCH); ok {
			res.ControlPlane.ArtifactAvailable = true
			_ = art
		}
		res.ControlPlane.UpdateAvailable = res.ControlPlane.ArtifactAvailable &&
			versionDiffers(version.Version, feed.Version)
	}

	// 各节点对比。
	var nodes []model.Node
	if err := s.db.Order("id ASC").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	for _, n := range nodes {
		st := ComponentStatus{
			NodeID:   n.ID,
			NodeUUID: n.UUID,
			Name:     n.Name,
			OS:       n.OS,
			Arch:     n.Arch,
		}
		// 在线节点实时拉取版本。
		if client, ok := s.pool.Get(n.UUID); ok {
			st.Online = true
			vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if vr, err := client.Worker.GetVersion(vctx, &workerpb.GetVersionRequest{}); err == nil {
				st.CurrentVersion = vr.Version
				if vr.Os != "" {
					st.OS = vr.Os
				}
				if vr.Arch != "" {
					st.Arch = vr.Arch
				}
			}
			cancel()
		}
		if feed != nil {
			if _, ok := SelectArtifact(feed, ComponentWorker, st.OS, st.Arch); ok {
				st.ArtifactAvailable = true
			}
			st.UpdateAvailable = st.Online && st.ArtifactAvailable &&
				st.CurrentVersion != "" && versionDiffers(st.CurrentVersion, feed.Version)
		}
		res.Nodes = append(res.Nodes, st)
	}
	return res, nil
}

// resolveArtifact 取 feed（按目标版本或最新）中目标 component+平台的制品。
// version 留空取 feed 最新；非空时校验 feed.Version 与之一致（本 FR feed 为 latest-only，
// 指定版本仅作确认，不做多版本检索）。
func (s *SelfUpdateService) resolveArtifact(ctx context.Context, component, goos, goarch, wantVersion string) (*Feed, *FeedArtifact, error) {
	feed, err := s.FetchFeed(ctx)
	if err != nil {
		return nil, nil, err
	}
	if wantVersion != "" && versionDiffers(wantVersion, feed.Version) {
		// 指定版本与 feed 当前版本不一致：本 FR 不支持任意历史版本回拉。
		return nil, nil, fmt.Errorf("更新源当前版本为 %s，无法升级到指定的 %s", feed.Version, wantVersion)
	}
	art, ok := SelectArtifact(feed, component, goos, goarch)
	if !ok {
		return feed, nil, ErrUpdateNoArtifact
	}
	return feed, art, nil
}

// UpgradeControlPlane 升级 CP 自身：选平台制品 → 下载校验替换 → 计划重启。
// 返回 (fromVersion, toVersion, error)。替换成功后异步延迟重启，函数先返回让 HTTP 202 抵达前端。
func (s *SelfUpdateService) UpgradeControlPlane(ctx context.Context, wantVersion string) (string, string, error) {
	from := version.Version
	feed, art, err := s.resolveArtifact(ctx, ComponentControlPlane, runtime.GOOS, runtime.GOARCH, wantVersion)
	if err != nil {
		return from, "", err
	}
	if !versionDiffers(from, feed.Version) {
		return from, feed.Version, ErrUpdateAlreadyLatest
	}

	if s.cpUpgradeFn != nil {
		if err := s.cpUpgradeFn(art); err != nil {
			return from, feed.Version, err
		}
		return from, feed.Version, nil
	}

	cacheDir := os.TempDir()
	if s.root != nil {
		cacheDir = s.root.CacheDir()
		_ = os.MkdirAll(cacheDir, 0o755)
	}
	dest := filepath.Join(cacheDir, fmt.Sprintf("control-plane-upgrade-%d", time.Now().UnixNano()))
	if err := selfupdate.Download(ctx, art.URL, art.SHA256, dest, s.cfg.AllowInsecure); err != nil {
		return from, feed.Version, err
	}
	target, err := os.Executable()
	if err != nil {
		_ = os.Remove(dest)
		return from, feed.Version, fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	if err := selfupdate.ReplaceExecutable(target, dest); err != nil {
		_ = os.Remove(dest)
		return from, feed.Version, err
	}
	// 异步延迟重启，先让 HTTP 202 返回。
	go func() {
		time.Sleep(time.Second)
		if s.restartCPFn != nil {
			s.restartCPFn()
			return
		}
		if err := selfupdate.Restart(); err == nil {
			os.Exit(0)
		}
	}()
	return from, feed.Version, nil
}

// UpgradeNode 经 gRPC 令目标节点升级：选节点平台制品 → 下发 UpgradeWorker。
// 返回 (fromVersion, toVersion, error)。
func (s *SelfUpdateService) UpgradeNode(ctx context.Context, nodeID uint, wantVersion string) (string, string, error) {
	var node model.Node
	if err := s.db.First(&node, nodeID).Error; err != nil {
		return "", "", fmt.Errorf("节点不存在: %w", err)
	}
	// 先校验更新源已配置——未配源对任何节点都无法升级，应先于节点在线状态报错。
	if _, err := s.FetchFeed(ctx); err != nil {
		return "", "", err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return "", "", ErrNodeOffline
	}

	goos, goarch := node.OS, node.Arch
	// 节点平台未知（极少数）时实时问一次。
	if goos == "" || goarch == "" {
		vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if vr, err := client.Worker.GetVersion(vctx, &workerpb.GetVersionRequest{}); err == nil {
			goos, goarch = vr.Os, vr.Arch
		}
		cancel()
	}

	feed, art, err := s.resolveArtifact(ctx, ComponentWorker, goos, goarch, wantVersion)
	if err != nil {
		return "", "", err
	}

	upctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	resp, err := client.Worker.UpgradeWorker(upctx, &workerpb.UpgradeWorkerRequest{
		DownloadUrl:   art.URL,
		Sha256:        art.SHA256,
		TargetVersion: feed.Version,
		AllowInsecure: s.cfg.AllowInsecure,
	})
	if err != nil {
		return "", feed.Version, fmt.Errorf("Worker UpgradeWorker RPC 失败: %w", err)
	}
	if !resp.Success {
		return resp.FromVersion, feed.Version, fmt.Errorf("节点升级失败: %s", resp.Error)
	}
	return resp.FromVersion, feed.Version, nil
}

// === 全网逐节点升级编排（FR-081） ===

// RolloutNodeState 是 rollout 中单个节点的升级状态。
type RolloutNodeState struct {
	NodeID      uint   `json:"nodeId"`
	Name        string `json:"name"`
	State       string `json:"state"` // pending | upgrading | succeeded | failed
	FromVersion string `json:"fromVersion"`
	ToVersion   string `json:"toVersion"`
	Error       string `json:"error"`
	Attempts    int    `json:"attempts"`
}

// Rollout 是一次全网逐节点升级编排的进度快照。
type Rollout struct {
	RolloutID     string             `json:"rolloutId"`
	TargetVersion string             `json:"targetVersion"`
	State         string             `json:"state"` // idle | running | completed
	StartedAt     time.Time          `json:"startedAt"`
	FinishedAt    *time.Time         `json:"finishedAt"`
	Total         int                `json:"total"`
	Succeeded     int                `json:"succeeded"`
	Failed        int                `json:"failed"`
	Pending       int                `json:"pending"`
	Nodes         []RolloutNodeState `json:"nodes"`
}

// StartRollout 对给定节点（nodeIDs 空=全部在线节点）发起逐节点串行升级，异步执行。
// 同一时刻只允许一个 rollout 运行中（再次发起返回错误）。返回当前 rollout 快照。
func (s *SelfUpdateService) StartRollout(ctx context.Context, nodeIDs []uint, wantVersion string) (*Rollout, error) {
	if !s.Configured() {
		return nil, ErrUpdateNotConfigured
	}

	s.rolloutMu.Lock()
	if s.rollout != nil && s.rollout.State == "running" {
		s.rolloutMu.Unlock()
		return nil, errors.New("已有全网升级正在进行")
	}

	// 选目标节点：指定则按 id，否则所有在线节点。
	targets, err := s.selectRolloutTargets(nodeIDs)
	if err != nil {
		s.rolloutMu.Unlock()
		return nil, err
	}

	ro := &Rollout{
		RolloutID:     uuid.New().String(),
		TargetVersion: wantVersion,
		State:         "running",
		StartedAt:     time.Now(),
		Total:         len(targets),
		Pending:       len(targets),
	}
	for _, n := range targets {
		ro.Nodes = append(ro.Nodes, RolloutNodeState{NodeID: n.ID, Name: n.Name, State: "pending"})
	}
	s.rollout = ro
	s.rolloutMu.Unlock()

	go s.runRollout(targets, wantVersion)
	return s.RolloutSnapshot(), nil
}

// selectRolloutTargets 解析 rollout 目标节点列表（须持 rolloutMu 调用）。
func (s *SelfUpdateService) selectRolloutTargets(nodeIDs []uint) ([]model.Node, error) {
	var nodes []model.Node
	q := s.db.Order("id ASC")
	if len(nodeIDs) > 0 {
		q = q.Where("id IN ?", nodeIDs)
	}
	if err := q.Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	// 仅保留当前在线（有反向连接）的节点。
	online := make([]model.Node, 0, len(nodes))
	for _, n := range nodes {
		if _, ok := s.pool.Get(n.UUID); ok {
			online = append(online, n)
		}
	}
	return online, nil
}

// runRollout 逐节点串行升级；单节点失败不阻断后续，记 failed + error。
func (s *SelfUpdateService) runRollout(targets []model.Node, wantVersion string) {
	for _, n := range targets {
		s.updateRolloutNode(n.ID, func(ns *RolloutNodeState) {
			ns.State = "upgrading"
			ns.Attempts++
		})
		from, to, err := s.upgradeNodeForRollout(n.ID, wantVersion)
		s.updateRolloutNode(n.ID, func(ns *RolloutNodeState) {
			ns.FromVersion = from
			ns.ToVersion = to
			if err != nil {
				ns.State = "failed"
				ns.Error = err.Error()
			} else {
				ns.State = "succeeded"
				ns.Error = ""
			}
		})
	}
	s.rolloutMu.Lock()
	if s.rollout != nil {
		now := time.Now()
		s.rollout.State = "completed"
		s.rollout.FinishedAt = &now
	}
	s.rolloutMu.Unlock()
}

// upgradeNodeForRollout 是 rollout 内对单节点的升级调用；测试可经 nodeUpgradeFn 覆盖。
func (s *SelfUpdateService) upgradeNodeForRollout(nodeID uint, wantVersion string) (string, string, error) {
	if s.nodeUpgradeFn != nil {
		return s.nodeUpgradeFn(nodeID, wantVersion)
	}
	return s.UpgradeNode(context.Background(), nodeID, wantVersion)
}

// updateRolloutNode 在锁内更新指定节点的 rollout 状态并重算聚合计数。
func (s *SelfUpdateService) updateRolloutNode(nodeID uint, fn func(*RolloutNodeState)) {
	s.rolloutMu.Lock()
	defer s.rolloutMu.Unlock()
	if s.rollout == nil {
		return
	}
	for i := range s.rollout.Nodes {
		if s.rollout.Nodes[i].NodeID == nodeID {
			fn(&s.rollout.Nodes[i])
			break
		}
	}
	s.recountRolloutLocked()
}

// recountRolloutLocked 重算 succeeded/failed/pending（须持 rolloutMu）。
func (s *SelfUpdateService) recountRolloutLocked() {
	var ok, fail, pend int
	for _, n := range s.rollout.Nodes {
		switch n.State {
		case "succeeded":
			ok++
		case "failed":
			fail++
		default:
			pend++
		}
	}
	s.rollout.Succeeded, s.rollout.Failed, s.rollout.Pending = ok, fail, pend
}

// RolloutSnapshot 返回当前/最近一次 rollout 的深拷贝快照；从未发起过返回 idle 空快照。
func (s *SelfUpdateService) RolloutSnapshot() *Rollout {
	s.rolloutMu.Lock()
	defer s.rolloutMu.Unlock()
	if s.rollout == nil {
		return &Rollout{State: "idle"}
	}
	cp := *s.rollout
	cp.Nodes = append([]RolloutNodeState{}, s.rollout.Nodes...)
	if s.rollout.FinishedAt != nil {
		f := *s.rollout.FinishedAt
		cp.FinishedAt = &f
	}
	return &cp
}
