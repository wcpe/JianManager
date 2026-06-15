package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/grpc"
	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/proto/workerpb"
)

var (
	ErrNodeNotOnline  = errors.New("节点不在线")
	ErrNodeNotConnected = errors.New("节点未连接")
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
	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return nil, err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeNotConnected
	}
	if client.Worker == nil {
		return nil, fmt.Errorf("gRPC 客户端未就绪")
	}

	resp, err := client.Worker.ListFiles(nil, &workerpb.ListFilesRequest{
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
	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return nil, err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeNotConnected
	}
	if client.Worker == nil {
		return nil, fmt.Errorf("gRPC 客户端未就绪")
	}

	resp, err := client.Worker.ReadFile(nil, &workerpb.ReadFileRequest{
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
	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ErrNodeNotConnected
	}
	if client.Worker == nil {
		return fmt.Errorf("gRPC 客户端未就绪")
	}

	resp, err := client.Worker.WriteFile(nil, &workerpb.WriteFileRequest{
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
	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return ErrNodeNotConnected
	}
	if client.Worker == nil {
		return fmt.Errorf("gRPC 客户端未就绪")
	}

	resp, err := client.Worker.DeleteFile(nil, &workerpb.DeleteFileRequest{
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

// getInstanceAndNode 获取实例及其节点信息。
func (s *FileService) getInstanceAndNode(instanceID uint) (*model.Instance, *model.Node, error) {
	var instance model.Instance
	if err := s.db.Preload("Node").First(&instance, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrInstanceNotFound
		}
		return nil, nil, fmt.Errorf("查询实例失败: %w", err)
	}
	if instance.Node.Status != model.NodeStatusOnline {
		return nil, nil, ErrNodeNotOnline
	}
	return &instance, &instance.Node, nil
}
