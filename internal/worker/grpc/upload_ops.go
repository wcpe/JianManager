package grpc

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// uploadChunkSize 是 UploadFile 流式分片大小（CP 侧切分参照值），与 downloadChunkSize 对称。
const uploadChunkSize = 64 * 1024

// UploadFile 单文件分块流式上传（FR-304，与 DownloadFile 对称）。
// WriteFile unary 受直拨 64MiB 单消息上限约束、反向隧道下又无上限整块缓冲，大文件上传走本流式 RPC。
// 零帧即关流按约定返回业务级失败且不触盘——CP 借此探测老 Worker（老版本返回 Unimplemented）。
func (s *Server) UploadFile(stream workerpb.WorkerService_UploadFileServer) error {
	first, err := stream.Recv()
	if err == io.EOF {
		// 能力探测约定：无副作用的业务级失败（见 proto 注释）。
		return stream.SendAndClose(&workerpb.UploadFileResponse{Success: false, Error: "缺少首帧"})
	}
	if err != nil {
		return err
	}

	inst, exists := s.manager.GetInstance(first.InstanceUuid)
	if !exists {
		return fmt.Errorf("实例 %s 不存在", first.InstanceUuid)
	}

	written, err := receiveFileUpload(inst.WorkDir, first, stream.Recv)
	if err != nil {
		return err
	}
	return stream.SendAndClose(&workerpb.UploadFileResponse{Success: true, BytesWritten: written})
}

// receiveFileUpload 把首帧与后续分片写入 workDir 下的目标文件：同目录临时文件接收，
// 收完原子改名覆盖目标（同卷保证原子性，Windows 走 MoveFileEx REPLACE_EXISTING 语义）。
// recv 返回 io.EOF 表示流结束；任何失败删除临时文件、既有目标文件保持原状。
// 抽成纯函数（不依赖 gRPC）便于单测。
func receiveFileUpload(workDir string, first *workerpb.UploadFileChunk, recv func() (*workerpb.UploadFileChunk, error)) (int64, error) {
	abs := filepath.Join(workDir, first.Path)
	if err := validatePath(workDir, abs); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return 0, fmt.Errorf("创建目录失败: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(abs), ".jm-upload-*.tmp")
	if err != nil {
		return 0, fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	discard := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	var written int64
	chunk := first
	for {
		if len(chunk.Content) > 0 {
			n, werr := tmp.Write(chunk.Content)
			written += int64(n)
			if werr != nil {
				discard()
				return 0, fmt.Errorf("写入临时文件失败: %w", werr)
			}
		}
		next, rerr := recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			discard()
			return 0, fmt.Errorf("接收上传流失败: %w", rerr)
		}
		chunk = next
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, abs); err != nil {
		_ = os.Remove(tmpPath)
		return 0, fmt.Errorf("移入目标文件失败: %w", err)
	}
	return written, nil
}
