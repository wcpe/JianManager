package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

var (
	ErrNodeNotOnline    = errors.New("节点不在线")
	ErrNodeNotConnected = errors.New("节点未连接")
	ErrWorkDirNotSet    = errors.New("实例未设置工作目录")
)

// FileService 文件管理服务（Control Plane 侧，通过 gRPC 委托给 Worker）。
type FileService struct {
	db     *gorm.DB
	pool   *grpc.ClientPool
}

// NewFileService 创建文件服务。
func NewFileService(db *gorm.DB, pool *grpc.ClientPool) *FileService {
	return &FileService{db: db, pool: pool}
}

// FileInfo 文件信息。
type FileInfo struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
}

// ListFiles 列出实例工作目录下的文件。
func (s *FileService) ListFiles(instanceID uint, path string) ([]FileInfo, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}

	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return nil, err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeNotConnected
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.ListFiles(ctx, &workerpb.ListFilesRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
	})
	if err != nil {
		return nil, fmt.Errorf("列出文件失败: %w", err)
	}

	files := make([]FileInfo, len(resp.Files))
	for i, f := range resp.Files {
		files[i] = FileInfo{
			Name:    f.Name,
			IsDir:   f.IsDir,
			Size:    f.Size,
			ModTime: f.ModTime,
		}
	}
	return files, nil
}

// ReadFile 读取文件内容。
func (s *FileService) ReadFile(instanceID uint, path string) ([]byte, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}

	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return nil, err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeNotConnected
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.ReadFile(ctx, &workerpb.ReadFileRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
	})
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}

	return resp.Content, nil
}

// WriteFile 写入文件内容。
func (s *FileService) WriteFile(instanceID uint, path string, content []byte) error {
	if err := validatePath(path); err != nil {
		return err
	}

	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ErrNodeNotConnected
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.WriteFile(ctx, &workerpb.WriteFileRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
		Content:      content,
	})
	if err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("写入文件失败: %s", resp.Error)
	}

	return nil
}

// DeleteFile 删除文件。
func (s *FileService) DeleteFile(instanceID uint, path string) error {
	if err := validatePath(path); err != nil {
		return err
	}

	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ErrNodeNotConnected
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.DeleteFile(ctx, &workerpb.DeleteFileRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
	})
	if err != nil {
		return fmt.Errorf("删除文件失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("删除文件失败: %s", resp.Error)
	}

	return nil
}

// RenameFile 重命名文件或目录。
func (s *FileService) RenameFile(instanceID uint, oldPath, newPath string) error {
	if err := validatePath(oldPath); err != nil {
		return err
	}
	if err := validatePath(newPath); err != nil {
		return err
	}

	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ErrNodeNotConnected
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Worker.RenameFile(ctx, &workerpb.RenameFileRequest{
		InstanceUuid: instance.UUID,
		OldPath:      oldPath,
		NewPath:      newPath,
	})
	if err != nil {
		return fmt.Errorf("重命名文件失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("重命名文件失败: %s", resp.Error)
	}

	return nil
}

// validatePath 校验文件路径，防止路径遍历攻击。
func validatePath(path string) error {
	if strings.Contains(path, "..") {
		return fmt.Errorf("路径不允许包含 ..")
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("路径不允许以 / 开头")
	}
	return nil
}

// getInstanceAndNode 获取实例及其节点信息。
func (s *FileService) getInstanceAndNode(instanceID uint) (*model.Instance, *model.Node, error) {
	var instance model.Instance
	if err := s.db.Preload("Node").First(&instance, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrInstanceNotFound
		}
		return nil, nil, fmt.Errorf("查询实例失败: %w", err)
	}
	if instance.WorkDir == "" {
		return nil, nil, ErrWorkDirNotSet
	}
	if instance.Node.Status != model.NodeStatusOnline {
		return nil, nil, ErrNodeNotOnline
	}
	return &instance, &instance.Node, nil
}
