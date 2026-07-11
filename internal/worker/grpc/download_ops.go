package grpc

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// downloadChunkSize 是 DownloadFile 流式分片大小：边读边发，不把整个文件读进内存。
const downloadChunkSize = 64 * 1024

// DownloadFile 把单个文件原样分块流式返回。
// ReadFile 的 10MiB 上限是在线编辑器护栏；下载走本 RPC，任意大小不截断。
func (s *Server) DownloadFile(req *workerpb.DownloadFileRequest, stream workerpb.WorkerService_DownloadFileServer) error {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}
	return streamFileDownload(inst.WorkDir, req.Path, func(content []byte, totalSize int64) error {
		return stream.Send(&workerpb.DownloadFileChunk{Content: content, TotalSize: totalSize})
	})
}

// streamFileDownload 打开 workDir 下 rel 指定的文件，按分片经 send 回调发出。
// 首帧携带文件总字节数（空文件也发一帧空内容），后续帧 totalSize 为 0；
// CP 依赖「成功必有首帧」先行判错再写 HTTP 响应头。
// 抽成纯函数（不依赖 gRPC）便于单测。
func streamFileDownload(workDir, rel string, send func(content []byte, totalSize int64) error) error {
	abs := filepath.Join(workDir, rel)
	if err := validatePath(workDir, abs); err != nil {
		return err
	}
	f, err := os.Open(abs)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("读取文件信息失败: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s 是目录，请使用打包下载", rel)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是常规文件", rel)
	}

	buf := make([]byte, downloadChunkSize)
	first := true
	for {
		n, rerr := f.Read(buf)
		if n > 0 || first {
			var total int64
			if first {
				total = info.Size()
				first = false
			}
			// 拷贝一份再发：gRPC 异步序列化，复用底层 buffer 会数据竞争。
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if serr := send(chunk, total); serr != nil {
				return serr
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("读取文件失败: %w", rerr)
		}
	}
}
