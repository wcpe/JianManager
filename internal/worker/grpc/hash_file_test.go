package grpc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/proto/workerpb"
)

// hashFileTestInstanceUUID 与 newBinaryWorker 注册的实例 UUID 保持一致
// （该辅助函数内以字面量注册，故此处同口径）。
const hashFileTestInstanceUUID = "22222222-2222-2222-2222-222222222222"

// TestHashFile_EnforcesServerSideHardLimit M-3：服务端必须自己兜底文件大小上限。
//
// 缺陷现场：`HashFileRequest.MaxBytes` 是 CP→Worker 的可变参数；传 0（= 不限，proto
// 注释如此声明）或超大值时，Worker 会无界读盘。ReadFile 有写死的 10MiB 截断兜底，
// HashFile 原先没有对应硬上限——契约完全依赖调用方自觉。
//
// 本测试用稀疏文件覆盖：声明 size 超过硬上限，实际不占磁盘。断言 `max_bytes=0`
// 与超大 max_bytes 都被判 too_large，且**未计算摘要**（说明是提前拒绝而非读完再判）。
func TestHashFile_EnforcesServerSideHardLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("稀疏文件语义在 Windows 上不稳定")
	}
	srv, _, workDir := newBinaryWorker(t)
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	bigName := "big.bin"
	f, err := os.Create(filepath.Join(workDir, bigName))
	require.NoError(t, err)
	require.NoError(t, f.Truncate(hashFileHardMaxBytes+1))
	require.NoError(t, f.Close())

	// max_bytes=0 在原语义下是「不限」，服务端必须按硬上限处理。
	resp, err := srv.HashFile(context.Background(), &workerpb.HashFileRequest{
		InstanceUuid: hashFileTestInstanceUUID, Path: bigName, MaxBytes: 0,
	})
	require.NoError(t, err)
	require.True(t, resp.TooLarge, "max_bytes=0 不得绕过服务端硬上限")
	require.Empty(t, resp.Sha256, "超限时不得计算摘要")
	require.EqualValues(t, hashFileHardMaxBytes+1, resp.SizeBytes)

	// 调用方传超大值同样按硬上限处理（不放宽）。
	resp, err = srv.HashFile(context.Background(), &workerpb.HashFileRequest{
		InstanceUuid: hashFileTestInstanceUUID, Path: bigName, MaxBytes: hashFileHardMaxBytes * 100,
	})
	require.NoError(t, err)
	require.True(t, resp.TooLarge, "超大 max_bytes 不得放宽服务端硬上限")

	// 正常小文件算出摘要（确认拒绝只针对超限，没有一刀切）。
	smallName := "small.txt"
	require.NoError(t, os.WriteFile(filepath.Join(workDir, smallName), []byte("hello"), 0o644))
	resp, err = srv.HashFile(context.Background(), &workerpb.HashFileRequest{
		InstanceUuid: hashFileTestInstanceUUID, Path: smallName, MaxBytes: 0,
	})
	require.NoError(t, err)
	require.False(t, resp.TooLarge)
	require.NotEmpty(t, resp.Sha256)
}

// TestHashFile_RejectsSymlink M-3：必须用 Lstat 判定目标类型，拒绝符号链接。
//
// 缺陷现场：原实现用 `os.Stat`（**跟随**符号链接），实例工作目录内的
// `link -> /etc/shadow` 这类把戏会让「读工作目录内文件」越出工作目录。
// 同包 binary_ops.go 已对节点本地文件用 Lstat 显式防这一点，本 RPC 属同类缺口。
func TestHashFile_RejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上创建符号链接需要特权")
	}
	srv, _, workDir := newBinaryWorker(t)
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	outside := filepath.Join(t.TempDir(), "outside-secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	linkName := "link-to-outside"
	require.NoError(t, os.Symlink(outside, filepath.Join(workDir, linkName)))

	resp, err := srv.HashFile(context.Background(), &workerpb.HashFileRequest{
		InstanceUuid: hashFileTestInstanceUUID, Path: linkName,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Sha256, "不得对工作目录外的文件算出摘要")
	require.Contains(t, resp.Error, "符号链接", "必须显式说明拒绝原因（而非静默按不存在处理）")
}

// TestHashFile_DirectoryRejected 目录仍被拒绝（重构后行为不变）。
func TestHashFile_DirectoryRejected(t *testing.T) {
	srv, _, workDir := newBinaryWorker(t)
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "subdir"), 0o755))

	resp, err := srv.HashFile(context.Background(), &workerpb.HashFileRequest{
		InstanceUuid: hashFileTestInstanceUUID, Path: "subdir", MaxBytes: 1024,
	})
	require.NoError(t, err)
	require.Contains(t, resp.Error, "目录")
	require.Empty(t, resp.Sha256)
}

// TestHashFile_MissingFileReportsError 不存在的文件仍走 error 字段（不返回 gRPC error）。
func TestHashFile_MissingFileReportsError(t *testing.T) {
	srv, _, _ := newBinaryWorker(t)
	resp, err := srv.HashFile(context.Background(), &workerpb.HashFileRequest{
		InstanceUuid: hashFileTestInstanceUUID, Path: "nope.bin",
	})
	require.NoError(t, err, "读盘问题必须落在 error 字段，让 CP 当作漂移信息呈现")
	require.NotEmpty(t, resp.Error)
	require.Empty(t, resp.Sha256)
}

// TestHashFile_RejectsPathEscape 路径穿越仍被拦下（回归）。
func TestHashFile_RejectsPathEscape(t *testing.T) {
	srv, _, _ := newBinaryWorker(t)
	_, err := srv.HashFile(context.Background(), &workerpb.HashFileRequest{
		InstanceUuid: hashFileTestInstanceUUID, Path: "../../etc/passwd",
	})
	require.Error(t, err, "越界路径必须直接拒绝（gRPC error）")
}
