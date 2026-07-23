package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	gogrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// FileInfo 文件信息（FR-373：含权限元数据，加性字段）。
type FileInfo struct {
	Name       string `json:"name"`
	IsDir      bool   `json:"isDir"`
	Size       int64  `json:"size"`
	ModTime    int64  `json:"modTime"`
	ModeOctal  string `json:"modeOctal,omitempty"`
	ModeString string `json:"modeString,omitempty"`
	Readable   bool   `json:"readable"`
	Writable   bool   `json:"writable"`
	Owner      string `json:"owner,omitempty"`
	Group      string `json:"group,omitempty"`
}

// PathAccess 写前/浏览前权限探测结果（FR-373）。
type PathAccess struct {
	Exists     bool   `json:"exists"`
	IsDir      bool   `json:"isDir"`
	Readable   bool   `json:"readable"`
	Writable   bool   `json:"writable"`
	ModeOctal  string `json:"modeOctal,omitempty"`
	ModeString string `json:"modeString,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Group      string `json:"group,omitempty"`
	Reason     string `json:"reason,omitempty"`
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
			Name:       f.Name,
			IsDir:      f.IsDir,
			Size:       f.Size,
			ModTime:    f.ModTime,
			ModeOctal:  f.ModeOctal,
			ModeString: f.ModeString,
			Readable:   f.Readable,
			Writable:   f.Writable,
			Owner:      f.Owner,
			Group:      f.Group,
		}
	}
	return files, nil
}

// CheckPathAccess 探测实例内路径可读/可写（FR-373）。
func (s *FileService) CheckPathAccess(instanceID uint, path string) (*PathAccess, error) {
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
	resp, err := client.Worker.CheckPathAccess(ctx, &workerpb.CheckPathAccessRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
	})
	if err != nil {
		return nil, fmt.Errorf("权限探测失败: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("%s", resp.Error)
	}
	return &PathAccess{
		Exists:     resp.Exists,
		IsDir:      resp.IsDir,
		Readable:   resp.Readable,
		Writable:   resp.Writable,
		ModeOctal:  resp.ModeOctal,
		ModeString: resp.ModeString,
		Owner:      resp.Owner,
		Group:      resp.Group,
		Reason:     resp.Reason,
	}, nil
}

// ChmodPath 实例内单 path 非递归 chmod（FR-373）。mode 空=保证属主可读写。
func (s *FileService) ChmodPath(instanceID uint, path, mode string) (string, error) {
	if err := validatePath(path); err != nil {
		return "", err
	}
	instance, node, err := s.getInstanceAndNode(instanceID)
	if err != nil {
		return "", err
	}
	client, ok := s.pool.Get(node.UUID)
	if !ok {
		return "", ErrNodeNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := client.Worker.ChmodPath(ctx, &workerpb.ChmodPathRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
		Mode:         mode,
	})
	if err != nil {
		return "", fmt.Errorf("修改权限失败: %w", err)
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Error)
	}
	return resp.ModeOctal, nil
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

// uploadChunkSize 上传流式分片大小，与 Worker DownloadFile 分片对称（FR-304）。
const uploadChunkSize = 64 * 1024

// legacyUploadMaxBytes 老 Worker（无 UploadFile）回退 WriteFile unary 的内容上限：
// Worker 服务端 64MiB 单消息上限扣除 protobuf 编组余量，超限直接引导升级而非盲发注定被拒的请求。
const legacyUploadMaxBytes = 64*1024*1024 - 64*1024

// UploadFile 单文件流式上传（FR-304）：经 Worker UploadFile client-stream 分片转发，
// 任意大小不受单消息上限约束、CP 侧内存占用 O(chunk)。老 Worker 回退 WriteFile unary（≤64MB）。
// 流式需贯穿整个 HTTP 请求，故由调用方传入请求级 ctx（不设固定超时——这正是本 FR 要移除的 10s 硬超时）。
func (s *FileService) UploadFile(ctx context.Context, instanceID uint, filePath string, r io.Reader) error {
	if err := validatePath(filePath); err != nil {
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

	return uploadToWorker(ctx, client.Worker, instance.UUID, filePath, r)
}

// workerFileUploader 上传所需的 Worker 客户端能力子集（UploadFile 流式 + WriteFile 回退）。
// workerpb.WorkerServiceClient 与 plugin 服务的窄接口均天然满足，供文件上传与插件部署共用。
type workerFileUploader interface {
	UploadFile(ctx context.Context, opts ...gogrpc.CallOption) (workerpb.WorkerService_UploadFileClient, error)
	WriteFile(ctx context.Context, in *workerpb.WriteFileRequest, opts ...gogrpc.CallOption) (*workerpb.WriteFileResponse, error)
}

// uploadToWorker 上传统一入口：探测能力后流式 UploadFile，老 Worker 回退 WriteFile（≤64MB）。
// 每次上传都轻量探测（一次零帧流）：隧道模式的 pool.Get 按取即建 Client，
// 无稳定宿主可缓存探测结果；上传是低频用户操作，探测成本可忽略且永远正确。
func uploadToWorker(ctx context.Context, w workerFileUploader, instanceUUID, filePath string, r io.Reader) error {
	supported, err := probeUploadFile(ctx, w)
	if err != nil {
		return fmt.Errorf("上传文件失败: %w", err)
	}
	if !supported {
		return uploadViaLegacyWriteFile(ctx, w, instanceUUID, filePath, r)
	}
	return streamUploadFile(ctx, w, instanceUUID, filePath, r)
}

// probeUploadFile 零帧探测 Worker 是否支持 UploadFile（约定见 proto 注释）：
// 新 Worker 对零帧即关流返回业务级失败（无副作用）→ 支持；老 Worker 返回 Unimplemented → 不支持；
// 其他传输错误如实上抛（此时回退 WriteFile 同样会失败，不掩盖真因）。
func probeUploadFile(ctx context.Context, w workerFileUploader) (bool, error) {
	stream, err := w.UploadFile(ctx)
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return false, nil
		}
		return false, err
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		if status.Code(err) == codes.Unimplemented {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// streamUploadFile 把 r 按分片经 UploadFile 流发往 Worker，并校验落盘字节数与已发送一致。
func streamUploadFile(ctx context.Context, w workerFileUploader, instanceUUID, filePath string, r io.Reader) error {
	// 子 ctx：任何提前返回都中止流（而非 CloseSend 正常结束），Worker 据此清理临时文件、不落半截目标。
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := w.UploadFile(sctx)
	if err != nil {
		return fmt.Errorf("上传文件失败: %w", err)
	}

	buf := make([]byte, uploadChunkSize)
	var sent int64
	first := true
	for {
		n, rerr := r.Read(buf)
		if n > 0 || first {
			// 拷贝一份再发：gRPC 异步序列化，复用底层 buffer 会数据竞争（与 DownloadFile 同理）。
			chunk := &workerpb.UploadFileChunk{Content: append([]byte(nil), buf[:n]...)}
			if first {
				chunk.InstanceUuid = instanceUUID
				chunk.Path = filePath
				first = false
			}
			if serr := stream.Send(chunk); serr != nil {
				if serr == io.EOF {
					break // 流已被服务端终止，真实错误由 CloseAndRecv 给出
				}
				return fmt.Errorf("上传文件失败: %w", serr)
			}
			sent += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			// 上游（浏览器）断流：直接返回并经 defer cancel 中止 gRPC 流——绝不 CloseSend，
			// 否则 Worker 会把残缺内容当完整文件提交。
			return fmt.Errorf("读取上传内容失败: %w", rerr)
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return fmt.Errorf("上传文件失败: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("上传文件失败: %s", resp.Error)
	}
	if resp.BytesWritten != sent {
		return fmt.Errorf("上传完整性校验失败：已发送 %d 字节，Worker 落盘 %d 字节", sent, resp.BytesWritten)
	}
	return nil
}

// uploadViaLegacyWriteFile 老 Worker 回退：整块读入（≤64MB）走既有 WriteFile unary。
// 超时放宽为 5 分钟（替代既有 10s——慢链路大文件在旧超时下必失败）。
func uploadViaLegacyWriteFile(ctx context.Context, w workerFileUploader, instanceUUID, filePath string, r io.Reader) error {
	content, err := io.ReadAll(io.LimitReader(r, legacyUploadMaxBytes+1))
	if err != nil {
		return fmt.Errorf("读取上传内容失败: %w", err)
	}
	if int64(len(content)) > legacyUploadMaxBytes {
		return errors.New("节点 Worker 版本过旧，不支持大文件流式上传，请先升级节点")
	}

	wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	resp, err := w.WriteFile(wctx, &workerpb.WriteFileRequest{
		InstanceUuid: instanceUUID,
		Path:         filePath,
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

// DownloadFile 单文件流式下载：返回 Worker gRPC 服务端流，由调用方逐帧 Recv 并写到 HTTP 响应。
// ReadFile 的 10MiB 上限是在线编辑器护栏，下载必须走本流式 RPC，任意大小不截断。
// 注意：流式需贯穿整个 HTTP 响应，故由调用方传入请求级 ctx（不在此设固定超时）。
func (s *FileService) DownloadFile(ctx context.Context, instanceID uint, path string) (workerpb.WorkerService_DownloadFileClient, error) {
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

	stream, err := client.Worker.DownloadFile(ctx, &workerpb.DownloadFileRequest{
		InstanceUuid: instance.UUID,
		Path:         path,
	})
	if err != nil {
		return nil, fmt.Errorf("下载文件失败: %w", err)
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
	// Indexing 为 true 表示 Worker 索引首建未就绪（FR-113，ADR-024）：本次 Hits 为空，调用方应稍后用同一查询重试。
	Indexing bool `json:"indexing"`
}

// SearchScope 限定跨文件搜索范围；CP 层过滤 Worker 返回的命中，避免新增 gRPC 字段。
type SearchScope struct {
	RootPath   string   `json:"rootPath"`
	Extensions []string `json:"extensions"`
}

// SearchFiles 对实例工作目录做全文搜索或文件名快速打开（FR-074，见 ADR-017）。
// CP 仅经 gRPC 把查询转发到目标节点 Worker（索引是 Worker 本地资产，CP 不持有）。
// mode 为 content（默认全文）或 filename（文件名快速打开）；maxResults<=0 时由 Worker 取默认。
func (s *FileService) SearchFiles(instanceID uint, query, mode string, maxResults int, scope SearchScope) (*SearchResult, error) {
	if mode != "filename" {
		mode = "content"
	}
	scope = normalizeSearchScope(scope)
	if scope.RootPath != "" {
		if err := validatePath(scope.RootPath); err != nil {
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

	workerMaxResults := maxResults
	if hasSearchScope(scope) && maxResults > 0 && maxResults < 1000 {
		workerMaxResults = maxResults * 5
		if workerMaxResults > 1000 {
			workerMaxResults = 1000
		}
	}
	// 索引增量 + 大目录扫描可能略耗时，给较宽超时（仍受请求级取消约束）。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.Worker.SearchFiles(ctx, &workerpb.SearchFilesRequest{
		InstanceUuid: instance.UUID,
		Query:        query,
		Mode:         mode,
		MaxResults:   int32(workerMaxResults),
	})
	if err != nil {
		return nil, fmt.Errorf("搜索失败: %w", err)
	}

	hits := make([]SearchHit, len(resp.Hits))
	for i, h := range resp.Hits {
		hits[i] = SearchHit{Path: h.Path, Line: int(h.Line), Snippet: h.Snippet}
	}
	if hasSearchScope(scope) {
		hits, resp.Truncated = filterSearchHits(hits, maxResults, scope, resp.Truncated)
	}
	return &SearchResult{Hits: hits, Truncated: resp.Truncated, Indexing: resp.Indexing}, nil
}

func normalizeSearchScope(scope SearchScope) SearchScope {
	scope.RootPath = strings.Trim(strings.ReplaceAll(scope.RootPath, "\\", "/"), "/ ")
	out := make([]string, 0, len(scope.Extensions))
	seen := map[string]bool{}
	for _, ext := range scope.Extensions {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		if seen[ext] {
			continue
		}
		seen[ext] = true
		out = append(out, ext)
	}
	scope.Extensions = out
	return scope
}

func hasSearchScope(scope SearchScope) bool {
	return scope.RootPath != "" || len(scope.Extensions) > 0
}

func filterSearchHits(hits []SearchHit, maxResults int, scope SearchScope, truncated bool) ([]SearchHit, bool) {
	exts := map[string]bool{}
	for _, ext := range scope.Extensions {
		exts[ext] = true
	}
	filtered := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		if scope.RootPath != "" && hit.Path != scope.RootPath && !strings.HasPrefix(hit.Path, scope.RootPath+"/") {
			continue
		}
		if len(exts) > 0 && !exts[strings.ToLower(path.Ext(hit.Path))] {
			continue
		}
		filtered = append(filtered, hit)
	}
	if maxResults > 0 && len(filtered) > maxResults {
		return filtered[:maxResults], true
	}
	return filtered, truncated
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
