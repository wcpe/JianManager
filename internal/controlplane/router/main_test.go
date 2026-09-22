package router

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本包测试临时根的目录名前缀（`TestMain` 创建，`sweepStaleTestRoots` 据此识别遗留）。
const testRootPrefix = "jm-router-tests-"

// TestMain 让整个 router 测试包共用一个进程级临时目录，并在退出时兜底清理。
//
// 背景：`os.TempDir()` 默认指向系统 /tmp，而 CI/开发机上 /tmp 常为 tmpfs（内存盘，
// 容量有限）。本包的 `setupTestRouter*` 会经 `dataroot.Init` 在临时目录下铺一套
// FHS 布局（`var/servers`、`opt/jdks` …），且每个用例各建一份；若不做进程级回收，
// 整包跑下来会在 /tmp 里堆积数百棵目录树（实测单次约 15–20MB）直到系统重启。
//
// 三件事：
//  1. 先清扫**上一次**遗留的根（见 sweepStaleTestRoots）；
//  2. 把 TMPDIR 改指到本进程专属目录，使 `t.TempDir()`、`os.MkdirTemp("")` 与
//     `dataroot` 派生目录全部落在这个可整体删除的根之下（比逐处改签名更彻底，
//     且不动 500+ 个既有调用点）；
//  3. m.Run 返回后移除整棵根；即使个别用例只做了部分清理，也不会留下残渣。
//
// 注意：必须在任何 t.TempDir() 之前设置 TMPDIR —— testing 框架在首次调用
// t.TempDir() 时读取 TMPDIR 并缓存，故在 TestMain 入口处设置是唯一可靠时机。
func TestMain(m *testing.M) {
	// 自愈：若上次运行被外部杀死（`go test -timeout`、CI 取消、SIGKILL），下面的
	// 退出清理不会执行，遗留根会一直占着 tmpfs。这里在启动时先把旧的一并收掉。
	sweepStaleTestRoots()

	root, err := os.MkdirTemp("", testRootPrefix)
	if err != nil {
		// 连临时根都建不出来时不静默：直接失败，避免测试在错误前提下继续。
		// m11：用 stderr + os.Exit(1) 而非 panic——后者会打出整段 goroutine 栈，
		// 把「TMPDIR 不可用」这一条事实淹没在噪音里；两者都让测试进程以非 0 退出，
		// 但一行 stderr 更贴近 Go 测试约定的 TestMain 失败形态。
		fmt.Fprintf(os.Stderr, "router 测试：创建测试临时根失败: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("TMPDIR", root); err != nil {
		fmt.Fprintf(os.Stderr, "router 测试：设置 TMPDIR 失败: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	removeTreeWithRetry(root)
	os.Exit(code)
}

// sweepStaleTestRoots 清扫 `os.TempDir()` 下本包遗留的测试根。
//
// 为什么需要它：`TestMain` 的退出清理只在正常退出时执行；被 `-timeout` 杀掉或
// 收到 SIGKILL 时（实测 `timeout 300 go test ./internal/controlplane/router/` 即如此），
// 清理代码根本不会运行，于是每次被杀都在 tmpfs 上留下 15–20MB。
//
// 安全性（不误删活跃根）：本函数只处理三类同时满足的目录——
//  1. 名字带本包固定前缀 `jm-router-tests-`（不碰同目录下其它进程/其它临时文件）；
//  2. 是**目录**（`ReadDir` 的 DirEntry 按类型位判定，符号链接不算目录，故不会被跟随）；
//  3. 根目录自身 mtime 早于 2 小时前。
//
// 第 3 条是「并发执行（`-count`、并行跑两次、多 worktree 同时跑本包）时不误删活根」的
// 粗筛。它成立的前提是**活跃运行会持续刷新根目录 mtime**：每个用例的 `t.TempDir()`
// 都在根下直接建子目录（`os.MkdirTemp` 落在 TMPDIR 内），子目录删除也回写父目录，
// 故整包运行期间根的 mtime 一直在动。本包实测整包耗时 5~6 分钟（非 race）/≤1 小时（-race），
// 2 小时的窗口留足了余量；只有「同一根下连续 2 小时无任何文件系统变更」的极端停顿
// （如某个用例被 hang 住）才可能被误收，此时该运行本身已由 `-timeout` 判失败。
func sweepStaleTestRoots() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	staleBefore := time.Now().Add(-2 * time.Hour)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), testRootPrefix) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || info.ModTime().After(staleBefore) {
			continue
		}
		removeTreeWithRetry(filepath.Join(os.TempDir(), e.Name()))
	}
}

// removeTreeWithRetry 删除目录树，最多尝试若干轮。
//
// 为什么需要重试：`os.RemoveAll` 在遇到**不可读/不可进入**的目录时会 EACCES 半途退出，
// 留下整棵树。本包当前没有创建这类目录的用例（唯一显式 Chmod 是
// `backup_storage_test.go` 的 `0o755`），这段逻辑是为「将来/其它 worktree 上存在的
// 权限类用例」兜底——历史上 worker 侧有 `internal/worker/grpc/perm_ops_rpc_test.go`
// 会造 `0o000` 目录，同族的包级 TMPDIR 机制一旦被复制过去就需要它。
//
// 机制：每轮先给「能被访问到的」目录补 u+rwx，再删一次。`filepath.WalkDir` 对每个目录
// **先**以 `err == nil` 回调一次（此时可 chmod），随后才尝试读它——读失败时会再回调一次
// 并带上 err（本次跳过，但上一轮的 chmod 已生效）。因此父目录权限一旦放开，
// 下一轮就能进入更深的那层继续放开，若干轮内可清干净。
// 清理失败不改变退出码（调用方按兜底语义忽略）。
func removeTreeWithRetry(root string) {
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := os.Stat(root); err != nil {
			return // 已删除
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil // 进不去的目录留到下一轮
			}
			if info, ierr := d.Info(); ierr == nil && info.Mode().Perm()&0o700 != 0o700 {
				_ = os.Chmod(path, info.Mode().Perm()|0o700)
			}
			return nil
		})
		if err := os.RemoveAll(root); err == nil {
			return
		}
		// 目录权限刚被放开，稍等一拍再试（并行用例可能仍在写文件）。
		time.Sleep(100 * time.Millisecond)
	}
}

// TestRemoveTreeWithRetry_CleansUnreadableDir （M-7）给兜底清理补上覆盖：
// 本包既有的 `removeTreeWithRetry` 逻辑此前**没有任何用例打到**（包内无 `0o000` 用例），
// 一旦 `WalkDir` 的「先回调、后读目录」次序假设被改坏（例如把 chmod 挪到 `err != nil` 之后），
// 清理会静默退化成「删不掉」，而退出码不变、无人发现。
//
// 用例在 `t.TempDir()` 下造一层 `0o000` 子目录 + 文件，断言整棵树被清掉。
// 以 root 身份运行时 `0o000` 不构成障碍（root 可无视权限位），此时跳过，
// 以免把「用例如实跑完」误报成通过。
func TestRemoveTreeWithRetry_CleansUnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时权限位不构成障碍，无法验证兜底路径")
	}
	base := t.TempDir()
	root := filepath.Join(base, "cleanup-root")
	locked := filepath.Join(root, "locked")
	require := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	require(os.MkdirAll(locked, 0o755), "建目录树")
	require(os.WriteFile(filepath.Join(locked, "f.txt"), []byte("x"), 0o644), "写文件")
	// 把 locked 自身设为不可进入/不可读：RemoveAll 必须先放开它才能递归删掉里面的文件。
	require(os.Chmod(locked, 0o000), "降权限")
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) }) // 用例失败时不留不可删的残渣

	removeTreeWithRetry(root)

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("兜底清理未删除整棵树：stat(%s)=%v", root, err)
	}
}
