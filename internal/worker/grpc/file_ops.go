package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// ListFiles 列出实例工作目录下的文件（FR-373：附权限元数据）。
func (s *Server) ListFiles(ctx context.Context, req *workerpb.ListFilesRequest) (*workerpb.ListFilesResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}

	dir := filepath.Join(inst.WorkDir, req.Path)
	if err := validatePath(inst.WorkDir, dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%s", formatPermError("读取目录", err))
	}

	files := make([]*workerpb.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fi := &workerpb.FileInfo{
			Name:    entry.Name(),
			IsDir:   entry.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		}
		fillFileInfoPerm(fi, filepath.Join(dir, entry.Name()))
		files = append(files, fi)
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
	if err := validateNonRootPath(inst.WorkDir, path); err != nil {
		return nil, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s", formatPermError("读取", err))
	}

	const maxSize = 10 * 1024 * 1024
	if len(content) > maxSize {
		content = content[:maxSize]
	}

	return &workerpb.ReadFileResponse{Content: content}, nil
}

// hashFileHardMaxBytes HashFile 的**服务端硬上限**（M-3）。
//
// 为什么服务端必须自己兜底：`HashFileRequest.MaxBytes` 是 CP→Worker 的可变参数，
// 语义写在 proto 注释里而无服务端下限保护；未来任何调用方传 0（= 不限）或超大值，
// Worker 都会无界读盘。ReadFile 有写死的 10MiB 截断兜底，本 RPC 原先没有对应硬上限。
//
// 取 512MiB：与 CP 侧 `binaryDiskDigestMaxBytes`（service/binary_version.go）同量级，
// 即「正常调用方的上限就是它能接受的最大值」，再大的一律按超限处理而不去读盘。
const hashFileHardMaxBytes = 512 * 1024 * 1024

// HashFile 计算实例工作目录内某文件的 SHA-256（FR-468 §2.5 版本漂移检测）。
//
// 独立于 ReadFile 的原因：ReadFile 有 10MiB 截断，对二进制做摘要会得到「前 10MiB 的
// 摘要」这种看似成功实则错误的结论；且为校验把整个文件搬回 CP 纯属浪费带宽。
// 本 RPC 在 Worker 本地流式读盘算哈希，返回的是完整文件的真实摘要。
//
// max_bytes>0 时超限直接返回 too_large（不计算）：调用方（版本视图巡检）用它避免
// 每次查询都去读一个数 GB 的文件。**服务端另有硬上限**（hashFileHardMaxBytes）：
// 调用方传 0 或超大值时按硬上限处理，不信任调用方上限（M-3）。
// 读盘错误不返回 gRPC error，而是落在 error 字段，
// 让 CP 能把「文件不在/没权限」当成漂移信息呈现，而不是一次失败查询。
func (s *Server) HashFile(ctx context.Context, req *workerpb.HashFileRequest) (*workerpb.HashFileResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}

	path := filepath.Join(inst.WorkDir, req.Path)
	if err := validateNonRootPath(inst.WorkDir, path); err != nil {
		return nil, err
	}

	// Lstat 而非 Stat（M-3）：不跟随符号链接，避免实例工作目录内的 `link -> /etc/shadow`
	// 这类把戏让「读工作目录内文件」越出工作目录（只读，危害有限，但属防御纵深缺口）。
	// 与同包 binary_ops.go 的「节点本地文件必须是常规文件」判定同口径。
	// 注意：文件不存在时 Lstat 也返回 err，与原先 Stat 的错误分支一致。
	info, err := os.Lstat(path)
	if err != nil {
		return &workerpb.HashFileResponse{Error: formatPermError("读取", err)}, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// 显式拒绝符号链接：本 RPC 只对工作目录内的**常规文件**做摘要。
		return &workerpb.HashFileResponse{Error: "目标是符号链接，不接受（避免读到工作目录外的文件）"}, nil
	}
	if info.IsDir() {
		return &workerpb.HashFileResponse{Error: "目标是目录，不是文件"}, nil
	}

	// 生效上限 = min(调用方上限, 服务端硬上限)：调用方传 0/超大值时不放宽。
	limit := int64(req.MaxBytes)
	if limit <= 0 || limit > hashFileHardMaxBytes {
		limit = hashFileHardMaxBytes
	}
	if info.Size() > limit {
		return &workerpb.HashFileResponse{TooLarge: true, SizeBytes: info.Size()}, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return &workerpb.HashFileResponse{Error: formatPermError("读取", err)}, nil
	}
	// 只读文件句柄：Close 失败无补救动作（fd 由 runtime 兜底回收），显式忽略以满足 errcheck。
	defer func() { _ = file.Close() }()

	// O_NOFOLLOW 无法经 os.Open 表达，且路径已拒绝符号链接；此处再校验一次句柄指向的
	// 仍是常规文件（覆盖「Lstat 之后、Open 之前被换成符号链接」的 TOCTOU 窗口）。
	if fh, serr := file.Stat(); serr != nil || !fh.Mode().IsRegular() {
		return &workerpb.HashFileResponse{Error: "目标不是常规文件（可能被并发替换）"}, nil
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return &workerpb.HashFileResponse{Error: formatPermError("读取", err)}, nil
	}
	return &workerpb.HashFileResponse{
		Sha256:    hex.EncodeToString(hasher.Sum(nil)),
		SizeBytes: info.Size(),
	}, nil
}

// WriteFile 写入文件内容。
func (s *Server) WriteFile(ctx context.Context, req *workerpb.WriteFileRequest) (*workerpb.WriteFileResponse, error) {
	inst, exists := s.manager.GetInstance(req.InstanceUuid)
	if !exists {
		return nil, fmt.Errorf("实例 %s 不存在", req.InstanceUuid)
	}

	path := filepath.Join(inst.WorkDir, req.Path)
	if err := validateNonRootPath(inst.WorkDir, path); err != nil {
		return nil, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &workerpb.WriteFileResponse{Success: false, Error: fmt.Sprintf("创建目录失败: %v", err)}, nil
	}

	if err := os.WriteFile(path, req.Content, 0644); err != nil {
		return &workerpb.WriteFileResponse{Success: false, Error: formatPermError("写文件", err)}, nil
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
	if err := validateNonRootPath(inst.WorkDir, path); err != nil {
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
	if err := validateNonRootPath(inst.WorkDir, oldPath); err != nil {
		return nil, err
	}
	if err := validateNonRootPath(inst.WorkDir, newPath); err != nil {
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

	rel, err := filepath.Rel(absWork, absTarget)
	if err != nil {
		return fmt.Errorf("计算目标相对路径失败: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("路径越界: %s 不在工作目录 %s 下", targetPath, workDir)
	}

	return nil
}

// validateNonRootPath 拒绝把实例工作目录根当作具体文件操作目标，防止删除等操作扩大到整个实例。
func validateNonRootPath(workDir, targetPath string) error {
	if err := validatePath(workDir, targetPath); err != nil {
		return err
	}
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("解析工作目录失败: %w", err)
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("解析目标路径失败: %w", err)
	}
	if absTarget == absWork {
		return fmt.Errorf("路径不得为工作目录根")
	}
	return nil
}
