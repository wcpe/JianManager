package grpc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/wcpe/JianManager/internal/worker/jdk"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// ListArtifactCache 列出节点本地制品缓存项 + 总占用 + 当前容量上限（FR-178）。
// 缓存未启用时返回空列表（total/cap 为 0），CP 侧据此提示「未启用」。
func (s *Server) ListArtifactCache(_ context.Context, _ *workerpb.ListArtifactCacheRequest) (*workerpb.ListArtifactCacheResponse, error) {
	if s.cache == nil {
		return &workerpb.ListArtifactCacheResponse{}, nil
	}
	items, err := s.cache.List()
	if err != nil {
		return nil, fmt.Errorf("列出制品缓存失败: %w", err)
	}
	total, _ := s.cache.TotalBytes()
	out := make([]*workerpb.ArtifactCacheItem, 0, len(items))
	for _, it := range items {
		out = append(out, &workerpb.ArtifactCacheItem{
			Sha256:     it.SHA256,
			Name:       it.Name,
			Type:       it.Type,
			Version:    it.Version,
			Size:       it.Size,
			CachedAt:   it.CachedAt.Unix(),
			LastUsedAt: it.LastUsedAt.Unix(),
		})
	}
	return &workerpb.ListArtifactCacheResponse{Items: out, TotalBytes: total, CapBytes: s.cache.Cap()}, nil
}

// EvictArtifactCache 逐项清除指定 sha256 的缓存（幂等）。
func (s *Server) EvictArtifactCache(_ context.Context, req *workerpb.EvictArtifactCacheRequest) (*workerpb.EvictArtifactCacheResponse, error) {
	if s.cache == nil {
		return &workerpb.EvictArtifactCacheResponse{Success: false, Error: "节点制品缓存未启用"}, nil
	}
	if err := s.cache.Evict(req.Sha256); err != nil {
		return &workerpb.EvictArtifactCacheResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.EvictArtifactCacheResponse{Success: true}, nil
}

// ClearArtifactCache 清空节点全部制品缓存，返回被清除的项数。
func (s *Server) ClearArtifactCache(_ context.Context, _ *workerpb.ClearArtifactCacheRequest) (*workerpb.ClearArtifactCacheResponse, error) {
	if s.cache == nil {
		return &workerpb.ClearArtifactCacheResponse{Success: false, Error: "节点制品缓存未启用"}, nil
	}
	n, err := s.cache.Clear()
	if err != nil {
		return &workerpb.ClearArtifactCacheResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.ClearArtifactCacheResponse{Success: true, Removed: int32(n)}, nil
}

// SetArtifactCacheCap 设置容量上限（字节，0=不限）；设定后即触发一次 LRU 淘汰使总占用回落。
func (s *Server) SetArtifactCacheCap(_ context.Context, req *workerpb.SetArtifactCacheCapRequest) (*workerpb.SetArtifactCacheCapResponse, error) {
	if s.cache == nil {
		return &workerpb.SetArtifactCacheCapResponse{Success: false, Error: "节点制品缓存未启用"}, nil
	}
	s.cache.SetCap(req.CapBytes)
	// 设上限后主动收敛一次（SetCap 不淘汰，靠下次 Put；这里立即按新上限淘汰一次）。
	if err := s.cache.EnforceCap(); err != nil {
		return &workerpb.SetArtifactCacheCapResponse{Success: false, Error: err.Error()}, nil
	}
	total, _ := s.cache.TotalBytes()
	return &workerpb.SetArtifactCacheCapResponse{Success: true, CapBytes: s.cache.Cap(), TotalBytes: total}, nil
}

// JDKCatalog 经 foojay disco 查询某发行版可选的具体 JDK 版本，喂前端版本选择器（FR-178）。
// 经进程级出站 client（FR-174）。失败时返回 error 字段、packages 为空（CP 透传给前端提示）。
func (s *Server) JDKCatalog(_ context.Context, req *workerpb.JDKCatalogRequest) (*workerpb.JDKCatalogResponse, error) {
	pkgs, err := jdk.FoojayCatalog(s.outboundClient(), "", req.Vendor, int(req.MajorVersion), req.Arch)
	if err != nil {
		return &workerpb.JDKCatalogResponse{Error: err.Error()}, nil
	}
	out := make([]*workerpb.JDKCatalogPackage, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, &workerpb.JDKCatalogPackage{
			Distribution: p.Distribution,
			MajorVersion: int32(p.MajorVersion),
			JavaVersion:  p.JavaVersion,
			ArchiveType:  p.ArchiveType,
			Latest:       p.Latest,
		})
	}
	return &workerpb.JDKCatalogResponse{Packages: out}, nil
}

// BrowseDir 只读列出节点上某绝对路径下的子目录（FR-178 目录选择器）。
// 空路径返回可选起点（Windows 盘符 / Unix 根）；只列目录、不列文件；
// path 经 filepath.Clean 规范化，CP 仅平台管理员可达（守架构不变量：CP 经 gRPC 委托 Worker）。
func (s *Server) BrowseDir(_ context.Context, req *workerpb.BrowseDirRequest) (*workerpb.BrowseDirResponse, error) {
	path := req.Path
	if path == "" {
		return &workerpb.BrowseDirResponse{Success: true, Path: "", Dirs: browseRoots()}, nil
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return &workerpb.BrowseDirResponse{Success: false, Error: "必须是绝对路径"}, nil
	}
	st, err := os.Stat(clean)
	if err != nil {
		return &workerpb.BrowseDirResponse{Success: false, Error: fmt.Sprintf("无法访问: %v", err)}, nil
	}
	if !st.IsDir() {
		return &workerpb.BrowseDirResponse{Success: false, Error: "不是目录"}, nil
	}
	entries, err := os.ReadDir(clean)
	if err != nil {
		return &workerpb.BrowseDirResponse{Success: false, Error: fmt.Sprintf("读取目录失败: %v", err)}, nil
	}
	dirs := make([]*workerpb.BrowseDirEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dirs = append(dirs, &workerpb.BrowseDirEntry{Name: e.Name(), Path: filepath.Join(clean, e.Name())})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })

	parent := filepath.Dir(clean)
	if parent == clean {
		parent = "" // 已在根/盘符顶
	}
	return &workerpb.BrowseDirResponse{Success: true, Path: clean, Parent: parent, Dirs: dirs}, nil
}

// browseRoots 返回目录浏览的可选起点：Windows 探测存在的盘符根，Unix 返回 /。
func browseRoots() []*workerpb.BrowseDirEntry {
	if runtime.GOOS != "windows" {
		return []*workerpb.BrowseDirEntry{{Name: "/", Path: "/"}}
	}
	var out []*workerpb.BrowseDirEntry
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + ":\\"
		if _, err := os.Stat(root); err == nil {
			out = append(out, &workerpb.BrowseDirEntry{Name: string(c) + ":", Path: root})
		}
	}
	return out
}
