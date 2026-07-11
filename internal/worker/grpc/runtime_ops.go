package grpc

import (
	"context"

	"github.com/wcpe/JianManager/internal/worker/runtimescan"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// SetRuntimeScanner 注入运行时扫描器（FR-298 节点运行时库）。
// 由 main 装配；不注入则 ScanRuntimes 返回空候选（向后兼容）。
func (s *Server) SetRuntimeScanner(sc *runtimescan.Scanner) {
	s.runtimeScanner = sc
}

// ScanRuntimes 扫描节点常见安装路径发现运行时候选（jdk/nodejs，FR-298）。
// 路径不存在/探测失败静默跳过；托管根下的候选标 already_registered，
// CP 侧另按 DB 已登记路径补标。
func (s *Server) ScanRuntimes(ctx context.Context, req *workerpb.ScanRuntimesRequest) (*workerpb.ScanRuntimesResponse, error) {
	if s.runtimeScanner == nil {
		return &workerpb.ScanRuntimesResponse{}, nil
	}
	cands := s.runtimeScanner.Scan(req.Types)
	out := make([]*workerpb.RuntimeCandidate, 0, len(cands))
	for _, c := range cands {
		out = append(out, &workerpb.RuntimeCandidate{
			Type:              c.Type,
			Vendor:            c.Vendor,
			Version:           c.Version,
			MajorVersion:      int32(c.Major),
			Arch:              c.Arch,
			Path:              c.Path,
			AlreadyRegistered: c.AlreadyRegistered,
		})
	}
	return &workerpb.ScanRuntimesResponse{Candidates: out}, nil
}
