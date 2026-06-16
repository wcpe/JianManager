package grpc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wxys233/JianManager/proto/workerpb"
)

// ListFiles 列出实例工作目录下的文件。
func (s *Server) ListFiles(ctx context.Context, req *workerpb.ListFilesRequest) (*workerpb.ListFilesResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}

	dir := filepath.Join(inst.WorkDir, req.Path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败: %w", err)
	}

	files := make([]*workerpb.FileInfo, len(entries))
	for i, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files[i] = &workerpb.FileInfo{
			Name:    entry.Name(),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		}
	}

	return &workerpb.ListFilesResponse{Files: files}, nil
}

// ReadFile 读取文件内容。
func (s *Server) ReadFile(ctx context.Context, req *workerpb.ReadFileRequest) (*workerpb.ReadFileResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}

	path := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, path); err != nil {
		return nil, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}

	const maxSize = 10 * 1024 * 1024
	if len(content) > maxSize {
		content = content[:maxSize]
	}

	return &workerpb.ReadFileResponse{Content: content}, nil
}

// WriteFile 写入文件内容。
func (s *Server) WriteFile(ctx context.Context, req *workerpb.WriteFileRequest) (*workerpb.WriteFileResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}

	path := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, path); err != nil {
		return nil, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &workerpb.WriteFileResponse{Success: false, Error: fmt.Sprintf("创建目录失败: %v", err)}, nil
	}

	if err := os.WriteFile(path, req.Content, 0644); err != nil {
		return &workerpb.WriteFileResponse{Success: false, Error: fmt.Sprintf("写入文件失败: %v", err)}, nil
	}

	return &workerpb.WriteFileResponse{Success: true}, nil
}

// DeleteFile 删除文件。
func (s *Server) DeleteFile(ctx context.Context, req *workerpb.DeleteFileRequest) (*workerpb.DeleteFileResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}

	path := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, path); err != nil {
		return nil, err
	}

	if err := os.RemoveAll(path); err != nil {
		return &workerpb.DeleteFileResponse{Success: false, Error: fmt.Sprintf("删除失败: %v", err)}, nil
	}

	return &workerpb.DeleteFileResponse{Success: true}, nil
}

// RenameFile 重命名文件或目录。
func (s *Server) RenameFile(ctx context.Context, req *workerpb.RenameFileRequest) (*workerpb.RenameFileResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}

	oldPath := filepath.Join(inst.WorkDir, req.OldPath)
	newPath := filepath.Join(inst.WorkDir, req.NewPath)
	if err := validatePath(inst.WorkDir, oldPath); err != nil {
		return nil, err
	}
	if err := validatePath(inst.WorkDir, newPath); err != nil {
		return nil, err
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return &workerpb.RenameFileResponse{Success: false, Error: fmt.Sprintf("重命名失败: %v", err)}, nil
	}

	return &workerpb.RenameFileResponse{Success: true}, nil
}

// validatePath 校验路径安全（防止路径遍历攻击）。
func validatePath(workDir, targetPath string) error {
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("解析工作目录失败: %w", err)
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("解析目标路径失败: %w", err)
	}

	if absTarget != absWork && !filepath.HasPrefix(absTarget, absWork+string(filepath.Separator)) {
		return fmt.Errorf("路径越界: %s 不在工作目录 %s 下", targetPath, workDir)
	}

	return nil
}
