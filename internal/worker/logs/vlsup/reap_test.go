package vlsup

// 红/绿双证（确定性 ✓）：
//   绿：造一个"两道都过"的孤儿（可执行文件落在本 data 根下 ＋ cmdline 含该根 ✓）⇒ 必须被回收 ✓；
//   红（负向断言 ✓）：造一个 cmdline/exe **都不含**本根的同形进程 ⇒ **必须不动** ✗✓（防误杀）。

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMatchesOwnOrphanJudgement 判据本体表驱动（纯函数 ⇒ 稳定 ✓）。
func TestMatchesOwnOrphanJudgement(t *testing.T) {
	root := "/data/worker-A"
	cases := []struct {
		name    string
		cmdline string
		exe     string
		want    bool
	}{
		{"两道都过 ✓", "/data/worker-A/opt/vlsup/bin/vlsup --storageDataPath /data/worker-A/var/vl/hot", "/data/worker-A/opt/vlsup/bin/vlsup", true},
		{"cmdline 不含根 ✗", "vlsup --storageDataPath /tmp/other", "/data/worker-A/opt/vlsup/bin/vlsup", false},
		{"exe 不在根下 ✗", "/data/worker-A/opt/vlsup/bin/vlsup --x /data/worker-A", "/usr/bin/vlsup", false},
		{"前缀伪装 ✗", "/data/worker-A/opt/vlsup/bin/vlsup --x /data/worker-A", "/data/worker-A-evil/vlsup", false},
		{"空根 ✗", "vlsup --x /data/worker-A", "/data/worker-A/vlsup", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rootArg := root
			if c.name == "空根 ✗" {
				rootArg = ""
			}
			require.Equal(t, c.want, matchesOwnOrphan(c.cmdline, c.exe, rootArg))
		})
	}
}

// TestReapOrphansGreenAndBystanderIntact 绿（回收本根孤儿 ✓）＋ 双负向（旁观进程不动 ✗✓）。
func TestReapOrphansGreenAndBystanderIntact(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("无 /proc ⇒ 本平台不适用 ✓")
	}
	root := t.TempDir()
	binDir := filepath.Join(root, "opt", "vlsup", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	// 把 sleep 复制到本根下当"假 vlsup"（两道都过的构造 ✓）
	self, err := exec.Command("sh", "-c", "command -v sleep").Output()
	require.NoError(t, err)
	sleepBin := filepath.Join(binDir, "vlsup")
	data, err := os.ReadFile(stringsTrim(string(self)))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sleepBin, data, 0o755))

	spawn := func(bin string, args ...string) *exec.Cmd {
		cmd := exec.Command(bin, args...)
		require.NoError(t, cmd.Start())
		return cmd
	}
	// 孤儿候选 ✓：exe 在根下 ＋ cmdline 含根
	// **不加多余参数** ✗：sleep 会拒参并瞬时退出；argv[0] 已是"根下路径" ⇒ cmdline 含根 ✓、exe 在根下 ✓
	orphan := spawn(sleepBin, "300")
	// 旁观者 ✗：cmdline 与 exe 都**不含**本根（真实 sleep ✓）
	bystander := spawn(stringsTrim(string(self)), "300")
	defer func() { _ = bystander.Process.Kill() }()

	time.Sleep(150 * time.Millisecond) // 让 /proc 可读 ✓
	reaped, err := ReapOrphans(root, "test", nil)
	require.NoError(t, err)
	require.Contains(t, reaped, orphan.Process.Pid, "绿：本根孤儿必须被回收 ✓")
	require.NotContains(t, reaped, bystander.Process.Pid, "红：旁观进程必须不动 ✗✓")

	// 且旁观者确实仍活着 ✓（进程级核验，不只信返回值 ✓）
	require.NoError(t, bystander.Process.Signal(os.Signal(syscallSignalZero())), "旁观进程应仍存活 ✓")
	_ = orphan.Wait() // 已回收 ⇒ Wait 应尽快返回 ✓
}

func stringsTrim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
func syscallSignalZero() os.Signal { return syscall.Signal(0) }
