package vlsup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/wcpe/JianManager/internal/platform/logasset"
)

// FR-475 依赖资产基线（v1.52.0）。正式发行分发与许可清单仍属 FR-475 验收；
// 其他版本必须重跑受影响能力验证（worker-log-platform-contract §2.2 / §10）。
const (
	// AssetTag 审批 tag。
	AssetTag = logasset.Tag
	// AssetBuildID 上游构建标识（Linux smoke 返回的 version 串）。
	AssetBuildID = logasset.BuildID
	// AssetReleaseCommit 审批材料登记的 release commit。
	AssetReleaseCommit = logasset.ReleaseCommit
	// AssetLicense 上游许可证声明。
	AssetLicense = logasset.License
)

// 审批材料中的解包可执行文件 SHA-256（.tmp/vl-asset-approval-2026-09-20.md）。
const (
	AssetExeSHA256LinuxAMD64   = logasset.LinuxExecutableSHA256
	AssetExeSHA256WindowsAMD64 = logasset.WindowsExecutableSHA256
	// 下载包 SHA-256（分发校验用，exec 前校验的是解包可执行文件）。
	AssetPkgSHA256LinuxAMD64   = logasset.LinuxPackageSHA256
	AssetPkgSHA256WindowsAMD64 = logasset.WindowsPackageSHA256
)

// ApprovedExeSHA256 返回当前 GOOS/GOARCH 的审批可执行文件哈希；未登记平台返回空串。
func ApprovedExeSHA256(goos, goarch string) string {
	switch strings.ToLower(goos + "/" + goarch) {
	case "linux/amd64":
		return AssetExeSHA256LinuxAMD64
	case "windows/amd64":
		return AssetExeSHA256WindowsAMD64
	default:
		return ""
	}
}

// CurrentApprovedExeSHA256 返回当前运行平台的审批可执行文件哈希。
func CurrentApprovedExeSHA256() string {
	return ApprovedExeSHA256(runtime.GOOS, runtime.GOARCH)
}

// VerifyAsset 在 exec 前校验资产路径与期望 SHA-256。
// 任一校验不匹配（缺失路径、空期望、读失败、哈希不一致）均不得安装/启动
// （worker-victorialogs-runtime §3.1）。
func VerifyAsset(path, expectedSHA256 string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("vlsup: asset path is empty")
	}
	want := strings.TrimSpace(expectedSHA256)
	if want == "" {
		return fmt.Errorf("vlsup: expected asset sha256 is empty")
	}
	got, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("vlsup: asset sha256 mismatch for %s: expected %s got %s", path, want, got)
	}
	return nil
}

// FileSHA256 计算文件内容的 SHA-256（小写 hex）。
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("vlsup: open asset: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("vlsup: hash asset: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
