package service

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
)

var (
	ErrNodeNotOnline = errors.New("节点不在线")
)

// FileService 文件管理服务（Control Plane 侧，通过 gRPC 委托给 Worker）。
type FileService struct {
	db *gorm.DB
}

// NewFileService 创建文件服务。
func NewFileService(db *gorm.DB) *FileService {
	return &FileService{db: db}
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
	instance, err := s.getInstanceWithNode(instanceID)
	if err != nil {
		return nil, err
	}
	if instance.Node.Status != model.NodeStatusOnline {
		return nil, ErrNodeNotOnline
	}

	// TODO: 通过 gRPC 调用 Worker Node ListFiles
	_ = path
	return []FileInfo{}, nil
}

// ReadFile 读取文件内容。
func (s *FileService) ReadFile(instanceID uint, path string) ([]byte, error) {
	instance, err := s.getInstanceWithNode(instanceID)
	if err != nil {
		return nil, err
	}
	if instance.Node.Status != model.NodeStatusOnline {
		return nil, ErrNodeNotOnline
	}

	// TODO: 通过 gRPC 调用 Worker Node ReadFile
	_ = path
	return nil, fmt.Errorf("待实现: 需要 gRPC 连接 Worker Node")
}

// WriteFile 写入文件内容。
func (s *FileService) WriteFile(instanceID uint, path string, content []byte) error {
	instance, err := s.getInstanceWithNode(instanceID)
	if err != nil {
		return err
	}
	if instance.Node.Status != model.NodeStatusOnline {
		return ErrNodeNotOnline
	}

	// TODO: 通过 gRPC 调用 Worker Node WriteFile
	_ = path
	_ = content
	return fmt.Errorf("待实现: 需要 gRPC 连接 Worker Node")
}

// DeleteFile 删除文件。
func (s *FileService) DeleteFile(instanceID uint, path string) error {
	instance, err := s.getInstanceWithNode(instanceID)
	if err != nil {
		return err
	}
	if instance.Node.Status != model.NodeStatusOnline {
		return ErrNodeNotOnline
	}

	// TODO: 通过 gRPC 调用 Worker Node DeleteFile
	_ = path
	return fmt.Errorf("待实现: 需要 gRPC 连接 Worker Node")
}

// getInstanceWithNode 获取实例及其节点信息。
func (s *FileService) getInstanceWithNode(instanceID uint) (*model.Instance, error) {
	var instance model.Instance
	if err := s.db.Preload("Node").First(&instance, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}
	return &instance, nil
}
