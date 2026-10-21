package ingest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// 本文件覆盖 validateInstanceRelativePath 的**第三道判据**（物理落点 / 链接跟随，2026-10-22）。
//
// 为什么需要这一组用例：前两道都是字符串判据，只能证明「字面路径」落在工作目录里。而采集侧
// 打开文件是裸 os.Open（acquire/tailer.go 的 os.Open(t.path)、archive.go 的 os.Open(path)），
// 会跟随符号链接；工作目录里一个 `logs/latest.log -> <目录外文件>` 就能让字符串判据全绿而
// 物理读到宿主机任意可读文件并写入 VL。故必须对「解析后的落点」再判一次 containment。

// makeLinkOrSkip 建符号链接；无权限时跳过（Windows 上建符号链接需 SeCreateSymbolicLinkPrivilege，
// 普通用户/未提权的 CI 会失败——那是环境的限制，不是判据的问题）。
func makeLinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("本机无法创建符号链接（Windows 需 SeCreateSymbolicLinkPrivilege）：%v", err)
		}
		t.Fatalf("创建符号链接失败：%v", err)
	}
}

// TestValidateInstanceRelativePathRejectsSymlinkEscapingWorkDir 覆盖题目要求的
// 「符号链接指向目录外 → 拒绝」：链接本身在工作目录内，但落点在目录外。
func TestValidateInstanceRelativePathRejectsSymlinkEscapingWorkDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // 独立的工作目录之外位置

	// 目录外放一份「机密文件」，并让工作目录里的 logs 指向它所在目录。
	require.NoError(t, os.WriteFile(filepath.Join(outside, "latest.log"), []byte("secret"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "logs"), 0o755))
	require.NoError(t, os.RemoveAll(filepath.Join(root, "logs")))
	makeLinkOrSkip(t, outside, filepath.Join(root, "logs"))

	// 字面路径 `logs/latest.log` 完全正常，字符判据必然通过——这正是缺口所在。
	err := validateInstanceRelativePath(root, "logs/latest.log")
	require.Error(t, err, "落点在工作目录之外，必须拒绝")
	require.Contains(t, err.Error(), "逃出实例工作目录")
}

// TestValidateInstanceRelativePathAllowsSymlinkInsideWorkDir 覆盖
// 「符号链接指向目录内 → 放行」：链接的真实落点仍在工作目录内，属于合法运维布局
// （例如把 logs 软链到一个同盘的实体目录），不得误伤。
func TestValidateInstanceRelativePathAllowsSymlinkInsideWorkDir(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real-logs")
	require.NoError(t, os.MkdirAll(real, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(real, "latest.log"), []byte("ok"), 0o600))
	makeLinkOrSkip(t, real, filepath.Join(root, "logs"))

	require.NoError(t, validateInstanceRelativePath(root, "logs/latest.log"),
		"落点仍在工作目录内，应放行")
}

// TestValidateInstanceRelativePathAllowsMissingPathWithAncestorInside 覆盖
// 「路径不存在但祖先在目录内 → 放行」：日志文件通常在登记时尚未创建。
// 这条同时守住实现的关键点——不能直接对整条路径 EvalSymlinks（会因 ENOENT 报错而误拒），
// 必须先退到**最近的已存在祖先**再解析。
func TestValidateInstanceRelativePathAllowsMissingPathWithAncestorInside(t *testing.T) {
	root := t.TempDir()
	// 一个真实存在的子目录 + 其下尚不存在的文件
	require.NoError(t, os.MkdirAll(filepath.Join(root, "custom"), 0o755))

	require.NoError(t, validateInstanceRelativePath(root, "custom/not-created-yet.log"),
		"文件尚未创建、祖先在工作目录内，应放行")
	require.NoError(t, validateInstanceRelativePath(root, "a/b/c/deep.log"),
		"整串都不存在但祖先在工作目录内，应放行")
}

// TestValidateInstanceRelativePathRejectsMissingPathUnderEscapingLink 覆盖
// 「路径不存在且祖先也是逃逸链接 → 拒绝」：目标文件不存在不是免死金牌——只要能顺着
// 已存在的逃逸链接落进目录外，一旦文件被创建就会被读到。
func TestValidateInstanceRelativePathRejectsMissingPathUnderEscapingLink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	makeLinkOrSkip(t, outside, filepath.Join(root, "link"))

	err := validateInstanceRelativePath(root, "link/absent/deep.log")
	require.Error(t, err, "祖先是指向目录外的链接，即便叶子不存在也必须拒绝")
	require.Contains(t, err.Error(), "逃出实例工作目录")
}

// TestValidateInstanceRelativePathStringGatesUnaffectedBySymlinkLayer 守「只增不减」：
// 引入物理层判据后，原有的字符串层判据（绝对路径 / `..` 逃逸 / Windows 卷名）必须逐字仍然生效。
func TestValidateInstanceRelativePathStringGatesUnaffectedBySymlinkLayer(t *testing.T) {
	root := t.TempDir()

	rejected := []struct {
		name string
		rel  string
	}{
		{"POSIX 绝对路径", "/etc/shadow"},
		{"POSIX 逃逸", "../../etc/passwd"},
		{"POSIX 中段越界", "logs/../../../etc/shadow"},
		{"Windows 绝对路径", `C:\logs\a.log`},
		{"反斜杠逃逸", `..\..\etc\passwd`},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, validateInstanceRelativePath(root, tc.rel))
		})
	}

	// 合法相对路径且工作目录内无链接：不得因新判据被误拒。
	require.NoError(t, validateInstanceRelativePath(root, "logs/latest.log"))
	require.NoError(t, validateInstanceRelativePath(root, ""))
}

// TestValidateInstanceRelativePathSkipsPhysicalGateWithoutWorkDir 固化「workDir 为空」的契约：
// 物理层没有比对基准，此时只做字符串层判定（跨方言用例依赖该契约）。
func TestValidateInstanceRelativePathSkipsPhysicalGateWithoutWorkDir(t *testing.T) {
	require.NoError(t, validateInstanceRelativePath("", "logs/latest.log"),
		"无基准时不做物理判定，合法相对路径应放行")
	require.Error(t, validateInstanceRelativePath("", "/etc/shadow"),
		"字符串层判据不受影响，绝对路径仍须拒绝")
}
