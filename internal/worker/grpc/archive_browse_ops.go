package grpc

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// 归档浏览的安全上限（FR-075，见 ADR-018）。只读浏览，绝不成为节点事故源。
const (
	// maxArchiveEntries 是单个归档列举条目数上限，超出截断（防恶意/异常巨包拖垮列举）。
	maxArchiveEntries = 50000
	// maxArchiveEntryBytes 是读取单个归档内条目内容的字节上限，超出截断（文本预览用途）。
	maxArchiveEntryBytes = 4 * 1024 * 1024
)

// archiveExts 是可作为归档打开的扩展名（jar 即 zip）。
var archiveExts = map[string]bool{".jar": true, ".zip": true}

// isArchivePath 判断某路径是否为可打开的归档（按扩展名）。
func isArchivePath(p string) bool {
	return archiveExts[strings.ToLower(filepath.Ext(p))]
}

// ListArchiveEntries 列出归档（jar/zip）内全部条目（Go archive/zip，不起进程；FR-075，见 ADR-018）。
func (s *Server) ListArchiveEntries(ctx context.Context, req *workerpb.ListArchiveEntriesRequest) (*workerpb.ListArchiveEntriesResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	archivePath := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, archivePath); err != nil {
		return nil, err
	}
	if !isArchivePath(req.Path) {
		return nil, fmt.Errorf("%s 不是受支持的归档（仅 .jar/.zip）", req.Path)
	}

	entries, truncated, err := listZipEntries(archivePath)
	if err != nil {
		return nil, err
	}
	return &workerpb.ListArchiveEntriesResponse{Entries: entries, Truncated: truncated}, nil
}

// listZipEntries 打开 zip/jar 并列出条目（扁平），抽成纯函数便于单测。
// 超过 maxArchiveEntries 时截断并返回 truncated=true。
func listZipEntries(archivePath string) ([]*workerpb.ArchiveEntry, bool, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, false, fmt.Errorf("打开归档失败: %w", err)
	}
	defer zr.Close()

	out := make([]*workerpb.ArchiveEntry, 0, len(zr.File))
	truncated := false
	for _, f := range zr.File {
		if len(out) >= maxArchiveEntries {
			truncated = true
			break
		}
		// zip-slip 防护：拒绝逃逸/绝对路径条目（即便不落盘，异常条目一律不列）。
		if !isSafeArchiveEntryName(f.Name) {
			continue
		}
		out = append(out, &workerpb.ArchiveEntry{
			Name:           f.Name,
			IsDir:          f.FileInfo().IsDir(),
			Size:           int64(f.UncompressedSize64),
			CompressedSize: int64(f.CompressedSize64),
			Modified:       f.Modified.Unix(),
			Crc32:          f.CRC32,
		})
	}
	return out, truncated, nil
}

// isSafeArchiveEntryName 校验归档内条目名安全（zip-slip 防护）。
// 拒绝：绝对路径、含 .. 的路径段、Windows 盘符。归一为正斜杠后判定。
func isSafeArchiveEntryName(name string) bool {
	clean := filepath.ToSlash(name)
	if clean == "" {
		return false
	}
	if strings.HasPrefix(clean, "/") {
		return false
	}
	// 盘符（c:\ 等）。
	if len(clean) >= 2 && clean[1] == ':' {
		return false
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// ReadArchiveEntry 读取归档内某条目内容，流式截断到上限（FR-075）。
func (s *Server) ReadArchiveEntry(ctx context.Context, req *workerpb.ReadArchiveEntryRequest) (*workerpb.ReadArchiveEntryResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	archivePath := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, archivePath); err != nil {
		return nil, err
	}
	if !isArchivePath(req.Path) {
		return nil, fmt.Errorf("%s 不是受支持的归档（仅 .jar/.zip）", req.Path)
	}
	if !isSafeArchiveEntryName(req.Entry) {
		return nil, fmt.Errorf("非法的归档条目名: %s", req.Entry)
	}

	content, truncated, err := readZipEntry(archivePath, req.Entry, maxArchiveEntryBytes)
	if err != nil {
		return nil, err
	}
	return &workerpb.ReadArchiveEntryResponse{
		Content:   content,
		Truncated: truncated,
		Binary:    looksBinary(content),
	}, nil
}

// readZipEntry 从 zip/jar 中定位某条目并读取其内容（截断到 limit），抽成纯函数便于单测。
func readZipEntry(archivePath, entry string, limit int) ([]byte, bool, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, false, fmt.Errorf("打开归档失败: %w", err)
	}
	defer zr.Close()

	var target *zip.File
	for _, f := range zr.File {
		if f.Name == entry {
			target = f
			break
		}
	}
	if target == nil {
		return nil, false, fmt.Errorf("归档内不存在条目: %s", entry)
	}
	if target.FileInfo().IsDir() {
		return nil, false, fmt.Errorf("%s 是目录条目，不可读取内容", entry)
	}

	rc, err := target.Open()
	if err != nil {
		return nil, false, fmt.Errorf("读取归档条目失败: %w", err)
	}
	defer rc.Close()

	// 多读 1 字节判定是否被截断（读到 limit+1 表示原始更长）。
	buf, err := io.ReadAll(io.LimitReader(rc, int64(limit)+1))
	if err != nil {
		return nil, false, fmt.Errorf("读取归档条目失败: %w", err)
	}
	truncated := false
	if len(buf) > limit {
		buf = buf[:limit]
		truncated = true
	}
	return buf, truncated, nil
}

// looksBinary 嗅探内容是否为二进制（含 NUL 字节即判二进制）。
// 仅取前 8 KiB 嗅探，足够区分文本配置与编译产物/资源。
func looksBinary(content []byte) bool {
	n := len(content)
	if n > 8192 {
		n = 8192
	}
	return bytes.IndexByte(content[:n], 0) >= 0
}
