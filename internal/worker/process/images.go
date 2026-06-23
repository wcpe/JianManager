package process

import (
	"context"
	"fmt"
	"io"

	imagetypes "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// ImageSummary 是 Worker 本机一个 Docker 镜像的精简描述（FR-078，ADR-019）。
type ImageSummary struct {
	ID        string
	Tags      []string
	SizeBytes int64
	Created   int64
}

// dockerImageClient 抽象镜像管理所需的 Docker API 子集，便于测试注入 fake。
type dockerImageClient interface {
	ImageList(ctx context.Context, options imagetypes.ListOptions) ([]imagetypes.Summary, error)
	ImagePull(ctx context.Context, ref string, options imagetypes.PullOptions) (io.ReadCloser, error)
	ImageRemove(ctx context.Context, image string, options imagetypes.RemoveOptions) ([]imagetypes.DeleteResponse, error)
	Close() error
}

// newImageClient 构造镜像管理用的 Docker 客户端；默认 FromEnv，测试可覆盖。
var newImageClient = func() (dockerImageClient, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("连接本机 Docker 守护进程失败（请确认已安装并运行 Docker）: %w", err)
	}
	return cli, nil
}

// ListDockerImages 列出 Worker 本机 Docker 镜像。
// Docker 不可用时返回错误，调用方据此回报 docker_available=false（FR-078）。
func ListDockerImages(ctx context.Context) ([]ImageSummary, error) {
	cli, err := newImageClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()

	summaries, err := cli.ImageList(ctx, imagetypes.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("列出镜像失败: %w", err)
	}
	out := make([]ImageSummary, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, ImageSummary{
			ID:        s.ID,
			Tags:      s.RepoTags,
			SizeBytes: s.Size,
			Created:   s.Created,
		})
	}
	return out, nil
}

// PullDockerImage 从 registry 拉取镜像到 Worker 本机。
// 拉取流必须读尽，否则 Docker 守护进程可能中断拉取。
func PullDockerImage(ctx context.Context, ref string) error {
	cli, err := newImageClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	rc, err := cli.ImagePull(ctx, ref, imagetypes.PullOptions{})
	if err != nil {
		return fmt.Errorf("拉取镜像失败: %w", err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("读取拉取进度失败: %w", err)
	}
	return nil
}

// RemoveDockerImage 删除 Worker 本机 Docker 镜像。
func RemoveDockerImage(ctx context.Context, ref string, force bool) error {
	cli, err := newImageClient()
	if err != nil {
		return err
	}
	defer cli.Close()

	if _, err := cli.ImageRemove(ctx, ref, imagetypes.RemoveOptions{Force: force}); err != nil {
		return fmt.Errorf("删除镜像失败: %w", err)
	}
	return nil
}
