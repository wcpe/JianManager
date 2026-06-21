package grpc

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// archiveChunkSize 是 DownloadArchive 流式分片大小：边打包边发，不在内存缓冲整个 zip。
const archiveChunkSize = 32 * 1024

// archiveStreamWriter 把写入转成 gRPC 流分片，供 zip.Writer 边写边发。
// 实现 io.Writer：每次 Write 即作为一帧 DownloadArchiveChunk 下发（archive/zip 内部已按缓冲块调用 Write）。
type archiveStreamWriter struct {
	stream workerpb.WorkerService_DownloadArchiveServer
}

func (w *archiveStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	// 按 archiveChunkSize 切片下发，避免单帧过大。
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > archiveChunkSize {
			n = archiveChunkSize
		}
		// 拷贝一份再发：gRPC 异步序列化，复用底层 buffer 会数据竞争。
		buf := make([]byte, n)
		copy(buf, p[:n])
		if err := w.stream.Send(&workerpb.DownloadArchiveChunk{Content: buf}); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

// DownloadArchive 把选中的文件/目录（目录递归）即时打包为 zip 并分块流式返回（FR-070 批量下载）。
// 边遍历边打包边发送，Worker 不在内存缓冲整个归档。每个条目都经 validatePath 防越界。
func (s *Server) DownloadArchive(req *workerpb.DownloadArchiveRequest, stream workerpb.WorkerService_DownloadArchiveServer) error {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	return writeZipArchive(&archiveStreamWriter{stream: stream}, inst.WorkDir, req.Paths)
}

// writeZipArchive 把 workDir 下 paths 指定的文件/目录（目录递归）打包为 zip 写入 w。
// 每个条目经 validatePath 校验，越界即整体失败；仅打包常规文件（跳过符号链接/设备等）。
// 抽成纯函数（不依赖 gRPC）便于单测：传 *bytes.Buffer 即可读回校验。
func writeZipArchive(w io.Writer, workDir string, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("未指定要打包的路径")
	}

	zw := zip.NewWriter(w)

	for _, sel := range paths {
		selAbs := filepath.Join(workDir, sel)
		if err := validatePath(workDir, selAbs); err != nil {
			_ = zw.Close()
			return err
		}
		info, err := os.Stat(selAbs)
		if err != nil {
			_ = zw.Close()
			return fmt.Errorf("读取 %s 失败: %w", sel, err)
		}

		if info.IsDir() {
			walkErr := filepath.Walk(selAbs, func(p string, fi os.FileInfo, werr error) error {
				if werr != nil {
					return werr
				}
				if !fi.Mode().IsRegular() {
					return nil // 目录由文件路径隐式重建；跳过符号链接/设备等
				}
				rel, rerr := filepath.Rel(workDir, p)
				if rerr != nil {
					return rerr
				}
				return addZipFile(zw, p, zipEntryName(sel, rel))
			})
			if walkErr != nil {
				_ = zw.Close()
				return fmt.Errorf("打包 %s 失败: %w", sel, walkErr)
			}
			continue
		}

		if !info.Mode().IsRegular() {
			continue
		}
		if err := addZipFile(zw, selAbs, zipEntryName(sel, sel)); err != nil {
			_ = zw.Close()
			return fmt.Errorf("打包 %s 失败: %w", sel, err)
		}
	}

	return zw.Close()
}

// zipEntryName 计算某文件在 zip 内的条目名。
// sel 是用户选中的条目（文件或目录），rel 是该文件相对 workDir 的路径。
// 以 sel 的父目录为基准取相对路径，使 zip 内以「所选条目」为根（选 plugins 则含 plugins/...；选单文件则为其 basename）。
func zipEntryName(sel, rel string) string {
	selSlash := filepath.ToSlash(sel)
	relSlash := filepath.ToSlash(rel)
	parent := path.Dir(selSlash) // sel 的父目录；顶层条目为 "."
	if parent == "." || parent == "/" || parent == "" {
		return relSlash
	}
	trimmed := strings.TrimPrefix(relSlash, parent+"/")
	return trimmed
}

// addZipFile 把单个磁盘文件写入 zip，条目名为 name。
func addZipFile(zw *zip.Writer, diskPath, name string) error {
	f, err := os.Open(diskPath)
	if err != nil {
		return err
	}
	defer f.Close()
	// 用 Deflate 压缩；条目名统一正斜杠（zip 规范）。
	hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
	wtr, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(wtr, f)
	return err
}
