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

// ErrDockerUnavailable 表示目标节点未安装/未运行 Docker 守护进程。
var ErrDockerUnavailable = errors.New("目标节点 Docker 不可用")

// DockerImageService 经 gRPC 委托目标节点 Worker 管理本机 Docker 镜像（FR-078，见 ADR-019）。
// CP 不直连 Docker，所有镜像操作（列出/拉取/删除）经 Worker 中转，守架构边界。
type DockerImageService struct {
	db   *gorm.DB
	pool *cpgrpc.ClientPool
}

// NewDockerImageService 创建 Docker 镜像服务。
func NewDockerImageService(db *gorm.DB, pool *cpgrpc.ClientPool) *DockerImageService {
	return &DockerImageService{db: db, pool: pool}
}

// DockerImageInfo 是镜像列表的对外返回（FR-078）。
type DockerImageInfo struct {
	ID        string   `json:"id"`
	Tags      []string `json:"tags"`
	SizeBytes int64    `json:"sizeBytes"`
	Created   int64    `json:"created"`
}

// nodeByID 查询节点并返回连接句柄；节点不存在/未连接时返回相应错误。
func (s *DockerImageService) workerClient(nodeID uint) (*cpgrpc.Client, error) {
	var node model.Node
	if err := s.db.First(&node, nodeID).Error; err != nil {
		return nil, fmt.Errorf("节点不存在: %w", err)
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeOffline
	}
	return client, nil
}

// List 列出目标节点本机 Docker 镜像。
// Docker 不可用时返回 ErrDockerUnavailable（由 router 映射为可读提示）。
func (s *DockerImageService) List(nodeID uint) ([]DockerImageInfo, error) {
	client, err := s.workerClient(nodeID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resp, err := client.Worker.ListImages(ctx, &workerpb.ListImagesRequest{})
	if err != nil {
		return nil, fmt.Errorf("Worker ListImages RPC 失败: %w", err)
	}
	if !resp.DockerAvailable {
		return nil, ErrDockerUnavailable
	}
	out := make([]DockerImageInfo, 0, len(resp.Images))
	for _, img := range resp.Images {
		out = append(out, DockerImageInfo{
			ID:        img.Id,
			Tags:      img.Tags,
			SizeBytes: img.SizeBytes,
			Created:   img.Created,
		})
	}
	return out, nil
}

// Pull 在目标节点拉取镜像。拉取可能耗时较长，给较长超时。
func (s *DockerImageService) Pull(nodeID uint, image string) error {
	client, err := s.workerClient(nodeID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	resp, err := client.Worker.PullImage(ctx, &workerpb.PullImageRequest{Image: image})
	if err != nil {
		return fmt.Errorf("Worker PullImage RPC 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("拉取镜像失败: %s", resp.Error)
	}
	return nil
}

// Remove 在目标节点删除镜像。
func (s *DockerImageService) Remove(nodeID uint, image string, force bool) error {
	client, err := s.workerClient(nodeID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Worker.RemoveImage(ctx, &workerpb.RemoveImageRequest{Image: image, Force: force})
	if err != nil {
		return fmt.Errorf("Worker RemoveImage RPC 失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("删除镜像失败: %s", resp.Error)
	}
	return nil
}
