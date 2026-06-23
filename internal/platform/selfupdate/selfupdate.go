// Package selfupdate 提供 Control Plane 与 Worker Node 共用的二进制在线自替换能力（FR-081，见 ADR-020 §4）。
//
// 职责三件：流式下载并 SHA-256 校验、原子替换当前可执行文件、重启自身。
// 完整性校验靠 SHA-256（同构制品库 ADR-011 的内容寻址校验思路）；
// 替换跨平台处理（Unix rename 直接换 inode，Windows 先移走运行中的旧 exe）。
package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/net/context"
)

// ErrChecksumMismatch 下载产物的 SHA-256 与期望值不符（完整性校验失败，绝不替换）。
var ErrChecksumMismatch = errors.New("二进制 sha256 校验不符")

// ErrInsecureURL 下载源为非 https 且未显式允许（默认仅允许 https，避免中间人篡改二进制）。
var ErrInsecureURL = errors.New("下载源非 https 且未允许不安全下载")

// downloadTimeout 单次二进制下载的整体超时（二进制通常数十 MB，给足余量）。
const downloadTimeout = 10 * time.Minute

// Download 流式下载 url 到 destPath，边下边算 SHA-256，校验通过才保留。
//
// expectedSHA256 为期望的十六进制 sha256（大小写不敏感）；为空表示跳过校验（不推荐，仅内部测试）。
// allowInsecure=false 时拒绝非 https 源（ErrInsecureURL）。校验不符删除已下载文件并返回 ErrChecksumMismatch。
// destPath 的父目录须已存在（调用方通常用数据根 cache/ 目录）。
func Download(ctx context.Context, url, expectedSHA256, destPath string, allowInsecure bool) error {
	if !allowInsecure && !strings.HasPrefix(strings.ToLower(url), "https://") {
		return ErrInsecureURL
	}

	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("构造下载请求失败: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("下载二进制失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载二进制失败: HTTP %d", resp.StatusCode)
	}

	tmp := destPath + ".download"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("创建下载临时文件失败: %w", err)
	}

	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, hasher), resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("写入下载内容失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("关闭下载文件失败: %w", closeErr)
	}

	if expectedSHA256 != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, strings.TrimSpace(expectedSHA256)) {
			_ = os.Remove(tmp)
			return fmt.Errorf("%w: 期望 %s 实得 %s", ErrChecksumMismatch, expectedSHA256, got)
		}
	}

	if err := os.Rename(tmp, destPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("落地下载文件失败: %w", err)
	}
	return nil
}

// FileSHA256 计算文件的十六进制 SHA-256（小写）。供测试与校验复用。
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ReplaceExecutable 用 newPath 处的新二进制原子替换 target 处的可执行文件。
//
// target 通常是 os.Executable() 返回的当前进程可执行文件。
// Unix：直接 rename（替换 inode；正在运行的进程持有旧 inode 不受影响）。
// Windows：运行中的 exe 不能被覆盖/删除，先把 target 改名为 target.old，再把 newPath 落到 target。
// 替换在同目录内进行以保证 rename 为原子操作（跨卷 rename 会失败）。
func ReplaceExecutable(target, newPath string) error {
	// 收敛新二进制权限为可执行（下载临时文件已 0755，这里再保险）。
	_ = os.Chmod(newPath, 0o755)

	if runtime.GOOS == "windows" {
		old := target + ".old"
		_ = os.Remove(old) // 清理上一轮残留（旧进程可能仍持有则忽略）
		if err := os.Rename(target, old); err != nil {
			return fmt.Errorf("移走运行中的旧二进制失败: %w", err)
		}
		if err := os.Rename(newPath, target); err != nil {
			// 回滚：把旧二进制移回原位，避免目标缺失
			_ = os.Rename(old, target)
			return fmt.Errorf("替换二进制失败: %w", err)
		}
		return nil
	}

	if err := os.Rename(newPath, target); err != nil {
		return fmt.Errorf("替换二进制失败: %w", err)
	}
	return nil
}

// Restart 以原 argv/env re-exec 自身，父进程随后退出（由调用方控制退出时机）。
//
// 供裸跑（无系统服务托管）场景；被 systemd/Windows 服务托管时，进程退出即由服务拉起，
// 无需自 re-exec（调用方可直接 os.Exit）。返回新进程已成功启动后才可安全退出父进程。
func Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位自身可执行文件失败: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("解析可执行文件路径失败: %w", err)
	}
	procAttr := &os.ProcAttr{
		Dir:   "",
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}
	proc, err := os.StartProcess(exe, os.Args, procAttr)
	if err != nil {
		return fmt.Errorf("拉起新进程失败: %w", err)
	}
	// 不等待新进程；放手让其独立运行。
	return proc.Release()
}
