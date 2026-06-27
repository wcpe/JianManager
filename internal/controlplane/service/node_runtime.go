package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// NodeRuntimeService 提供节点级运行时管理（FR-178）：制品缓存管理、JDK 版本目录（foojay）、
// 目录浏览。三者都经 gRPC 委托给 Worker（守架构不变量：CP 不直接读节点 FS），
// 仅平台管理员可达（路由层 + 审计在 handler 强制）。
type NodeRuntimeService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
}

// NewNodeRuntimeService 创建节点运行时服务。
func NewNodeRuntimeService(db *gorm.DB, pool *cpgrpc.ClientPool) *NodeRuntimeService {
	return &NodeRuntimeService{db: db, pool: pool}
}

// ArtifactCacheItem 一条节点制品缓存项（CP 透出，name/version 缺失时用 asset 表按 sha256 补全）。
type ArtifactCacheItem struct {
	SHA256     string `json:"sha256"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Version    string `json:"version"`
	Size       int64  `json:"size"`
	CachedAt   int64  `json:"cachedAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
}

// ArtifactCacheView 缓存列表 + 总占用 + 当前上限。
type ArtifactCacheView struct {
	Items      []ArtifactCacheItem `json:"items"`
	TotalBytes int64               `json:"totalBytes"`
	CapBytes   int64               `json:"capBytes"`
}

// JDKCatalogPackage 一条可选 JDK 构建（喂前端版本选择器）。
type JDKCatalogPackage struct {
	Distribution string `json:"distribution"`
	MajorVersion int    `json:"majorVersion"`
	JavaVersion  string `json:"javaVersion"`
	ArchiveType  string `json:"archiveType"`
	Latest       bool   `json:"latest"`
}

// BrowseDirEntry 一个子目录项。
type BrowseDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// BrowseDirView 目录浏览结果。
type BrowseDirView struct {
	Path   string           `json:"path"`
	Parent string           `json:"parent"`
	Dirs   []BrowseDirEntry `json:"dirs"`
}

// nodeClient 解析 nodeID → 在线 Worker 客户端。
func (s *NodeRuntimeService) nodeClient(nodeID uint) (*cpgrpc.Client, error) {
	var n model.Node
	if err := s.db.First(&n, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	client, ok := s.pool.Get(n.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}
	return client, nil
}

// ListArtifactCache 列出节点制品缓存（name/version 缺失时用 CP asset 表按 sha256 补全）。
func (s *NodeRuntimeService) ListArtifactCache(nodeID uint) (*ArtifactCacheView, error) {
	client, err := s.nodeClient(nodeID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.ListArtifactCache(ctx, &workerpb.ListArtifactCacheRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListArtifactCache RPC 失败: %w", err)
	}
	view := &ArtifactCacheView{TotalBytes: resp.TotalBytes, CapBytes: resp.CapBytes}
	for _, it := range resp.Items {
		item := ArtifactCacheItem{
			SHA256:     it.Sha256,
			Name:       it.Name,
			Type:       it.Type,
			Version:    it.Version,
			Size:       it.Size,
			CachedAt:   it.CachedAt,
			LastUsedAt: it.LastUsedAt,
		}
		// name/version 缺失：用 CP 全局制品库（asset 表）按 sha256 补全（ADR-011）。
		if item.Name == "" || item.Version == "" {
			s.enrichFromAsset(&item)
		}
		view.Items = append(view.Items, item)
	}
	return view, nil
}

// enrichFromAsset 用 asset 表按 sha256 补全缓存项的 name/version。
func (s *NodeRuntimeService) enrichFromAsset(item *ArtifactCacheItem) {
	var a model.Asset
	if err := s.db.Where("sha256 = ?", item.SHA256).First(&a).Error; err != nil {
		return
	}
	if item.Name == "" {
		item.Name = a.Name
	}
	if item.Version == "" {
		item.Version = a.Version
	}
}

// EvictArtifactCache 逐项清除指定 sha256 的缓存。
func (s *NodeRuntimeService) EvictArtifactCache(nodeID uint, sha256 string) error {
	client, err := s.nodeClient(nodeID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.EvictArtifactCache(ctx, &workerpb.EvictArtifactCacheRequest{Sha256: sha256})
	if err != nil {
		return fmt.Errorf("EvictArtifactCache RPC 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}

// ClearArtifactCache 清空节点全部制品缓存，返回被清除项数。
func (s *NodeRuntimeService) ClearArtifactCache(nodeID uint) (int, error) {
	client, err := s.nodeClient(nodeID)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.ClearArtifactCache(ctx, &workerpb.ClearArtifactCacheRequest{})
	if err != nil {
		return 0, fmt.Errorf("ClearArtifactCache RPC 失败: %w", err)
	}
	if !resp.Success {
		return 0, fmt.Errorf("%s", resp.Error)
	}
	return int(resp.Removed), nil
}

// SetArtifactCacheCap 设置容量上限（字节，0=不限），返回设定后的上限与总占用。
func (s *NodeRuntimeService) SetArtifactCacheCap(nodeID uint, capBytes int64) (*ArtifactCacheView, error) {
	client, err := s.nodeClient(nodeID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.SetArtifactCacheCap(ctx, &workerpb.SetArtifactCacheCapRequest{CapBytes: capBytes})
	if err != nil {
		return nil, fmt.Errorf("SetArtifactCacheCap RPC 失败: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return &ArtifactCacheView{CapBytes: resp.CapBytes, TotalBytes: resp.TotalBytes}, nil
}

// JDKCatalog 经 Worker（foojay）查询某发行版可选具体版本，喂前端选择器。
func (s *NodeRuntimeService) JDKCatalog(nodeID uint, vendor string, major int, arch string) ([]JDKCatalogPackage, error) {
	client, err := s.nodeClient(nodeID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.JDKCatalog(ctx, &workerpb.JDKCatalogRequest{
		Vendor:       vendor,
		MajorVersion: int32(major),
		Arch:         arch,
	})
	if err != nil {
		return nil, fmt.Errorf("JDKCatalog RPC 失败: %w", err)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	out := make([]JDKCatalogPackage, 0, len(resp.Packages))
	for _, p := range resp.Packages {
		out = append(out, JDKCatalogPackage{
			Distribution: p.Distribution,
			MajorVersion: int(p.MajorVersion),
			JavaVersion:  p.JavaVersion,
			ArchiveType:  p.ArchiveType,
			Latest:       p.Latest,
		})
	}
	return out, nil
}

// BrowseDir 经 Worker 只读列出节点某绝对路径下的子目录（JDK 路径登记目录选择器）。
func (s *NodeRuntimeService) BrowseDir(nodeID uint, path string) (*BrowseDirView, error) {
	client, err := s.nodeClient(nodeID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.Worker.BrowseDir(ctx, &workerpb.BrowseDirRequest{Path: path})
	if err != nil {
		return nil, fmt.Errorf("BrowseDir RPC 失败: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	view := &BrowseDirView{Path: resp.Path, Parent: resp.Parent}
	for _, d := range resp.Dirs {
		view.Dirs = append(view.Dirs, BrowseDirEntry{Name: d.Name, Path: d.Path})
	}
	return view, nil
}
