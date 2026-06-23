package grpc

import (
	"context"
	"fmt"

	"github.com/wcpe/JianManager/internal/worker/search"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// SearchFiles 对实例工作目录做全文搜索或文件名快速打开（FR-074，见 ADR-017）。
//
// 每实例一份本地持久倒排索引（落数据根 var/index/，不进 CP DB）。每次查询前先增量更新
// 索引（扫描 + 指纹比对：新增/变化重索引、删除移除），再查询返回命中文件+行+片段。
// mode=filename 时走文件名子串匹配（行号为 0）。
func (s *Server) SearchFiles(ctx context.Context, req *workerpb.SearchFilesRequest) (*workerpb.SearchFilesResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	if inst.WorkDir == "" {
		return nil, fmt.Errorf("实例未设置工作目录")
	}
	if s.root == nil {
		return nil, fmt.Errorf("数据根未初始化，无法建立搜索索引")
	}

	ix := s.searchIndexFor(req.InstanceUuid)

	// 查询前增量更新索引（FR-074：文件变更增量更新）。扫描失败不阻断查询（用既有落盘索引兜底）。
	if _, err := ix.Update(inst.WorkDir); err != nil {
		// 仅记账级失败（如目录被并发删改）：继续用现有索引查询，避免整次搜索失败。
		_ = err
	}

	var res search.Result
	switch req.Mode {
	case "filename":
		res = ix.SearchFilename(req.Query, int(req.MaxResults))
	default: // content（含空串，默认全文）
		var err error
		res, err = ix.SearchContent(inst.WorkDir, req.Query, int(req.MaxResults))
		if err != nil {
			return nil, fmt.Errorf("全文搜索失败: %w", err)
		}
	}

	hits := make([]*workerpb.SearchHit, 0, len(res.Hits))
	for _, h := range res.Hits {
		hits = append(hits, &workerpb.SearchHit{
			Path:    h.Path,
			Line:    int32(h.Line),
			Snippet: h.Snippet,
		})
	}
	return &workerpb.SearchFilesResponse{Hits: hits, Truncated: res.Truncated}, nil
}

// searchIndexFor 取（或懒创建）某实例的搜索索引对象。索引对象持有自己的锁，跨查询复用。
func (s *Server) searchIndexFor(instanceUUID string) *search.Index {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	if ix, ok := s.searchIndexes[instanceUUID]; ok {
		return ix
	}
	ix := search.NewIndex(s.root.IndexDir(), instanceUUID, s.searchIgnore)
	s.searchIndexes[instanceUUID] = ix
	return ix
}
