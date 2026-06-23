package grpc

import (
	"context"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// Docker 镜像管理 RPC（FR-078，见 ADR-019）。
// Worker 经本机 Docker Engine API 管镜像；CP 不直连 Docker，所有镜像操作经此委托。
// Docker 守护进程不可达时返回明确错误（不影响 direct/daemon 模式）。

// ListImages 列出 Worker 本机 Docker 镜像。
func (s *Server) ListImages(ctx context.Context, req *workerpb.ListImagesRequest) (*workerpb.ListImagesResponse, error) {
	images, err := process.ListDockerImages(ctx)
	if err != nil {
		// Docker 不可用：回报 docker_available=false，由 CP 提示用户安装 Docker。
		return &workerpb.ListImagesResponse{DockerAvailable: false, Error: err.Error()}, nil
	}
	out := make([]*workerpb.ImageInfo, 0, len(images))
	for _, img := range images {
		out = append(out, &workerpb.ImageInfo{
			Id:        img.ID,
			Tags:      img.Tags,
			SizeBytes: img.SizeBytes,
			Created:   img.Created,
		})
	}
	return &workerpb.ListImagesResponse{Images: out, DockerAvailable: true}, nil
}

// PullImage 从 registry 拉取镜像到 Worker 本机。
func (s *Server) PullImage(ctx context.Context, req *workerpb.PullImageRequest) (*workerpb.PullImageResponse, error) {
	if strings.TrimSpace(req.Image) == "" {
		return &workerpb.PullImageResponse{Success: false, Error: "镜像名不能为空"}, nil
	}
	if err := process.PullDockerImage(ctx, req.Image); err != nil {
		return &workerpb.PullImageResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.PullImageResponse{Success: true}, nil
}

// RemoveImage 删除 Worker 本机 Docker 镜像。
func (s *Server) RemoveImage(ctx context.Context, req *workerpb.RemoveImageRequest) (*workerpb.RemoveImageResponse, error) {
	if strings.TrimSpace(req.Image) == "" {
		return &workerpb.RemoveImageResponse{Success: false, Error: "镜像名不能为空"}, nil
	}
	if err := process.RemoveDockerImage(ctx, req.Image, req.Force); err != nil {
		return &workerpb.RemoveImageResponse{Success: false, Error: err.Error()}, nil
	}
	return &workerpb.RemoveImageResponse{Success: true}, nil
}

// portMappingsFromProto 把 proto 端口映射转为 process 层端口映射（docker 模式）。
func portMappingsFromProto(in []*workerpb.PortMapping) []process.PortMapping {
	if len(in) == 0 {
		return nil
	}
	out := make([]process.PortMapping, 0, len(in))
	for _, pm := range in {
		out = append(out, process.PortMapping{
			ContainerPort: int(pm.ContainerPort),
			HostPort:      int(pm.HostPort),
			Protocol:      pm.Protocol,
		})
	}
	return out
}
