package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

var (
	ErrNodeNotOnline    = errors.New("节点不在线")
	ErrNodeNotConnected = errors.New("节点未连接")
	ErrWorkDirNotSet    = errors.New("实例未设置工作目录")
)

// FileService 文件管理服务（Control Plane 侧，通过 gRPC 委托给 Worker）。
type FileService struct {
	db   *gorm.DB
	pool *grpc.ClientPool
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

// DownloadArchive 把选中的若干文件/目录即时打包为 zip 流式返回（FR-070 批量下载）。
// 返回 Worker gRPC 服务端流，由调用方逐帧 Recv 并写到 HTTP 响应；Worker 边打包边发，CP 不缓冲整包。
// 注意：流式打包需贯穿整个 HTTP 响应，故由调用方传入请求级 ctx（不在此设固定超时）。
func (s *FileService) DownloadArchive(ctx context.Context, instanceID uint, paths []string) (workerpb.WorkerService_DownloadArchiveClient, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("未指定要打包的路径")
	}
	for _, p := range paths {
		if err := validatePath(p); err != nil {
			return nil, err
		}
	}

	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return nil, err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeNotConnected
	}

	stream, err := client.Worker.DownloadArchive(ctx, &workerpb.DownloadArchiveRequest{
		InstanceUuid: instance.UUID,
		Paths:        paths,
	})
	if err != nil {
		return nil, fmt.Errorf("批量下载失败: %w", err)
	}
	return stream, nil
}

// SearchHit 一条搜索命中（与 Worker SearchHit 对应，FR-074）。
type SearchHit struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}

// SearchResult 一次搜索的结果（FR-074）。
type SearchResult struct {
	Hits      []SearchHit `json:"hits"`
	Truncated bool        `json:"truncated"`
}

// SearchFiles 对实例工作目录做全文搜索或文件名快速打开（FR-074，见 ADR-017）。
// CP 仅经 gRPC 把查询转发到目标节点 Worker（索引是 Worker 本地资产，CP 不持有）。
// mode 为 content（默认全文）或 filename（文件名快速打开）；maxResults<=0 时由 Worker 取默认。
func (s *FileService) SearchFiles(instanceID uint, query, mode string, maxResults int) (*SearchResult, error) {
	if mode != "filename" {
		mode = "content"
	}
	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return nil, err
	}

	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeNotConnected
	}

	// 索引增量 + 大目录扫描可能略耗时，给较宽超时（仍受请求级取消约束）。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Worker.SearchFiles(ctx, &workerpb.SearchFilesRequest{
		InstanceUuid: instance.UUID,
		Query:        query,
		Mode:         mode,
		MaxResults:   int32(maxResults),
	})
	if err != nil {
		return nil, fmt.Errorf("搜索失败: %w", err)
	}

	hits := make([]SearchHit, len(resp.Hits))
	for i, h := range resp.Hits {
		hits[i] = SearchHit{Path: h.Path, Line: int(h.Line), Snippet: h.Snippet}
	}
	return &SearchResult{Hits: hits, Truncated: resp.Truncated}, nil
}

// ArchiveEntry 归档（jar/zip）内的单个条目（FR-075）。
type ArchiveEntry struct {
	Name           string `json:"name"`
	IsDir          bool   `json:"isDir"`
	Size           int64  `json:"size"`
	CompressedSize int64  `json:"compressedSize"`
	Modified       int64  `json:"modified"`
	CRC32          uint32 `json:"crc32"`
}

// ArchiveEntries 是列举归档条目的结果（FR-075）。
type ArchiveEntries struct {
	Entries   []ArchiveEntry `json:"entries"`
	Truncated bool           `json:"truncated"`
}

// ListArchiveEntries 列出归档（jar/zip）内全部条目（FR-075，委托 Worker archive/zip）。
func (s *FileService) ListArchiveEntries(instanceID uint, path string) (*ArchiveEntries, error) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resp, err := client.Worker.ListArchiveEntries(ctx, &workerpb.ListArchiveEntriesRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
	})
	if err != nil {
		return nil, fmt.Errorf("列出归档条目失败: %w", err)
	}
	out := &ArchiveEntries{Truncated: resp.Truncated, Entries: make([]ArchiveEntry, len(resp.Entries))}
	for i, e := range resp.Entries {
		out.Entries[i] = ArchiveEntry{
			Name:           e.Name,
			IsDir:          e.IsDir,
			Size:           e.Size,
			CompressedSize: e.CompressedSize,
			Modified:       e.Modified,
			CRC32:          e.Crc32,
		}
	}
	return out, nil
}

// ArchiveEntryContent 是读取归档内某条目内容的结果（FR-075）。
type ArchiveEntryContent struct {
	Content   []byte
	Truncated bool
	Binary    bool
}

// ReadArchiveEntry 读取归档内某条目内容（FR-075，委托 Worker）。
func (s *FileService) ReadArchiveEntry(instanceID uint, path, entry string) (*ArchiveEntryContent, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}
	if entry == "" {
		return nil, fmt.Errorf("缺少归档条目名")
	}
	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return nil, err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return nil, ErrNodeNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	resp, err := client.Worker.ReadArchiveEntry(ctx, &workerpb.ReadArchiveEntryRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
		Entry:        entry,
	})
	if err != nil {
		return nil, fmt.Errorf("读取归档条目失败: %w", err)
	}
	return &ArchiveEntryContent{Content: resp.Content, Truncated: resp.Truncated, Binary: resp.Binary}, nil
}

// DecompileResult 是反编译结果（FR-075）。
type DecompileResult struct {
	Success    bool   `json:"success"`
	Error      string `json:"error,omitempty"`
	Source     string `json:"source"`
	Truncated  bool   `json:"truncated"`
	Decompiler string `json:"decompiler,omitempty"`
}

// DecompileClass 反编译工作目录内 class/jar（或归档内某 class）为 Java 源码（FR-075，委托 Worker CFR）。
// 反编译可能耗时（跑 CFR 子进程），故超时给得比普通文件操作宽（含 Worker 侧 30s 反编译 + 余量）。
func (s *FileService) DecompileClass(instanceID uint, path, entry string) (*DecompileResult, error) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	resp, err := client.Worker.DecompileClass(ctx, &workerpb.DecompileClassRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
		Entry:        entry,
	})
	if err != nil {
		return nil, fmt.Errorf("反编译失败: %w", err)
	}
	return &DecompileResult{
		Success:    resp.Success,
		Error:      resp.Error,
		Source:     resp.Source,
		Truncated:  resp.Truncated,
		Decompiler: resp.Decompiler,
	}, nil
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
