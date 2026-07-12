package grpc

import (
	"context"

	"github.com/wcpe/JianManager/internal/worker/pkgmgr"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// SetPkgManager 注入包管理器（FR-306）。由 main 装配；不注入则 PM 配置 RPC 返回明确错误。
func (s *Server) SetPkgManager(m *pkgmgr.Manager) {
	s.pkgMgr = m
}

// GetPMConfig 读取节点包管理器与 registry 配置（FR-306）。
func (s *Server) GetPMConfig(ctx context.Context, req *workerpb.GetPMConfigRequest) (*workerpb.GetPMConfigResponse, error) {
	if s.pkgMgr == nil {
		return &workerpb.GetPMConfigResponse{Pm: "npm"}, nil
	}
	cfg := s.pkgMgr.Get(req.GetPm())
	return &workerpb.GetPMConfigResponse{
		Pm:                cfg.PM,
		CorepackAvailable: cfg.CorepackAvailable,
		PmVersion:         cfg.PMVersion,
		NodeBin:           cfg.NodeBin,
		Registries:        toProtoRegistries(cfg.Registries),
	}, nil
}

// SetPMConfig 设置 PM 偏好（corepack 激活 pnpm/yarn）并写托管 .npmrc（FR-306）。
func (s *Server) SetPMConfig(ctx context.Context, req *workerpb.SetPMConfigRequest) (*workerpb.SetPMConfigResponse, error) {
	if s.pkgMgr == nil {
		return &workerpb.SetPMConfigResponse{Success: false, Error: "节点未启用包管理器"}, nil
	}
	ver, err := s.pkgMgr.Set(ctx, req.GetPm(), fromProtoRegistries(req.GetRegistries()))
	if err != nil {
		return &workerpb.SetPMConfigResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.SetPMConfigResponse{Success: true, PmVersion: ver}, nil
}

func toProtoRegistries(regs []pkgmgr.Registry) []*workerpb.PMRegistry {
	out := make([]*workerpb.PMRegistry, 0, len(regs))
	for _, r := range regs {
		out = append(out, &workerpb.PMRegistry{Name: r.Name, Url: r.URL, Scope: r.Scope}) // token 不回传
	}
	return out
}

func fromProtoRegistries(regs []*workerpb.PMRegistry) []pkgmgr.Registry {
	out := make([]pkgmgr.Registry, 0, len(regs))
	for _, r := range regs {
		out = append(out, pkgmgr.Registry{Name: r.GetName(), URL: r.GetUrl(), Scope: r.GetScope(), Token: r.GetToken()})
	}
	return out
}
