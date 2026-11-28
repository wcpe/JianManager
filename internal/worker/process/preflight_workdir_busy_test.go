package process

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPreflight_WorkDirBusy_IgnoresUnrelatedCmdlinePathMention 回归 issue #101：
// 无关进程的命令行里**出现工作目录路径字符串**，不得被判为「工作目录下已有活进程」。
//
// 缺陷现场（真机 2026-11-27）：运维向实例工作目录拷贝 server.jar 的那条 shell 命令本身被预检
// 判成活进程，启动被拒：
//
//	{"error":"PREFLIGHT_FAILED","message":"工作目录下已有活进程（pid=…：bash -c WD=…cp /tmp/jmchain/pa…"}
//
// 命令退出后重试即成功——误报而非真占用。链路是两道判据同时被穿透：
//   - procInWorkDir 的「命令行含工作目录路径」子串匹配命中（命令里当然会出现目标路径）；
//   - looksLikeRuntimeCmdline 命中 `paper`/`.jar`（拷贝的正是 paper.jar）。
//
// 修复后预检以 cwd 为准：该命令的 cwd 在别处（/tmp），故不判占用。
func TestPreflight_WorkDirBusy_IgnoresUnrelatedCmdlinePathMention(t *testing.T) {
	orig := preflightListProcesses
	defer func() { preflightListProcesses = orig }()

	workDir := filepath.Join(t.TempDir(), "pa")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	jarPath := filepath.Join(workDir, "paper.jar")

	preflightListProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{
			PID:     4242,
			Cmdline: fmt.Sprintf("bash -c WD=%s; cp /tmp/jmchain/paper.jar %s", workDir, jarPath),
			Cwd:     t.TempDir(), // cwd 在别处：这条命令并不在实例目录里运行
		}}, nil
	}
	assert.NoError(t, checkWorkDirBusy(workDir, 0),
		"命令行里出现工作目录路径只是「提到了它」，不代表进程在该目录运行")

	// 同类：cwd 在别处的 tar/grep 等运维动作提到 jar 路径，同样不得判占用。
	preflightListProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{
			PID:     4243,
			Cmdline: fmt.Sprintf("tar -czf /tmp/backup.tgz %s", workDir),
			Cwd:     "/tmp",
		}}, nil
	}
	assert.NoError(t, checkWorkDirBusy(workDir, 0))
}

// TestPreflight_WorkDirBusy_DetectsRuntimeByCwd 正例：真正的游戏服进程即使命令行里**不含**
// 工作目录路径（相对 jar 名），只要 cwd 落在工作目录之下且可执行体是 java，就必须判占用。
//
// 这是本次收紧后必须保住的召回：多数受管实例的启动命令是 `java -jar server.jar nogui`，
// 命令行里没有绝对路径，唯一可靠的占用证据就是 cwd。
func TestPreflight_WorkDirBusy_DetectsRuntimeByCwd(t *testing.T) {
	orig := preflightListProcesses
	defer func() { preflightListProcesses = orig }()

	workDir := filepath.Join(t.TempDir(), "game-1")
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "world"), 0o755))

	// cwd 落在工作目录的**子目录**下（游戏服写 world 后仍可能改 cwd）也算占用。
	preflightListProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{
			PID:     5151,
			Cmdline: "java -Xmx4G -jar server.jar nogui",
			Cwd:     filepath.Join(workDir, "world"),
		}}, nil
	}
	err := checkWorkDirBusy(workDir, 0)
	require.Error(t, err, "cwd 在工作目录之下的 java 必须判占用")
	assert.Contains(t, err.Error(), "5151")

	// 基岩版可执行体同样算运行时。
	preflightListProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{{PID: 5152, Cmdline: "./bedrock_server", Cwd: workDir}}, nil
	}
	assert.Error(t, checkWorkDirBusy(workDir, 0), "基岩版服务端同样判占用")
}

// TestPreflight_WorkDirBusy_CwdInDirButNotRuntime cwd 恰在工作目录内的**非运行时**进程不判占用。
//
// 这条与上面那条正例是一对：cwd 命中只是必要条件，还要求可执行体是游戏服运行时。理由是
// 真机见过的现象——运维 tmux 会话里的 `-bash`、`tee` 管道、以及在目录里手工跑 `cp paper.jar …`
// 的 shell 都占着这个目录，却不占端口、不持锁、不影响新进程启动；把它们当占用会让「已停的服」
// 永远启动不了（面板只会回 PREFLIGHT_FAILED，运维无从绕过）。
func TestPreflight_WorkDirBusy_CwdInDirButNotRuntime(t *testing.T) {
	orig := preflightListProcesses
	defer func() { preflightListProcesses = orig }()

	workDir := t.TempDir()
	preflightListProcesses = func() ([]ScannedProcess, error) {
		return []ScannedProcess{
			{PID: 6161, Cmdline: "-bash", Cwd: workDir},                           // tmux 空壳
			{PID: 6162, Cmdline: "tee -a console.log", Cwd: workDir},              // 管道
			{PID: 6163, Cmdline: "cp paper.jar /tmp/paper.jar.bak", Cwd: workDir}, // 目录内手工拷贝
			{PID: 6164, Cmdline: "vim server.properties", Cwd: workDir},           // 编辑配置
		}, nil
	}
	assert.NoError(t, checkWorkDirBusy(workDir, 0),
		"cwd 在目录内但不像运行时的进程不得判占用（否则已停的服永远起不来）")
}

// TestPreflight_WorkDirBusy_EmptyCwdFallback cwd 取不到时（Windows 常见、/proc 权限受限）的
// 回退行为：仍要求**可执行体像运行时**，而不是「命令行任意位置含 .jar」。
//
// 明确写死这条回退语义，避免日后「cwd 为空就跳过」与「cwd 为空按老口径匹配」两种极端：
//   - 空 cwd + java 命令行含工作目录路径 → 判占用（真机双开的主要形态就是 java）；
//   - 空 cwd + bash/cp 命令行含工作目录路径 → 不判占用（issue #101 的误报在空 cwd 下也不得复活）。
func TestPreflight_WorkDirBusy_EmptyCwdFallback(t *testing.T) {
	orig := preflightListProcesses
	defer func() { preflightListProcesses = orig }()

	workDir := filepath.Join(t.TempDir(), "pa")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"空 cwd + java 绝对路径 jar", "java -jar " + filepath.Join(workDir, "server.jar") + " nogui", true},
		{"空 cwd + 带引号的 Windows java 路径", `"C:\Program Files\Java\jdk-21\bin\java.exe" -jar ` + filepath.Join(workDir, "server.jar"), true},
		{"空 cwd + 基岩版", "./bedrock_server --dir " + workDir, true},
		{"空 cwd + 拷贝命令（issue #101 形态）", "bash -c cp /tmp/jmchain/paper.jar " + filepath.Join(workDir, "paper.jar"), false},
		{"空 cwd + 打包命令", "tar -czf /tmp/b.tgz " + workDir, false},
		{"空 cwd + java 但不含工作目录路径", "java -jar /other/place/server.jar", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			preflightListProcesses = func() ([]ScannedProcess, error) {
				return []ScannedProcess{{PID: 7171, Cmdline: c.cmdline, Cwd: ""}}, nil
			}
			err := checkWorkDirBusy(workDir, 0)
			if c.want {
				require.Error(t, err, "cwd 取不到时应靠「可执行体像运行时 + 命令行含路径」判占用")
				assert.Contains(t, err.Error(), "7171")
				return
			}
			assert.NoError(t, err, "cwd 取不到时也不得把无关命令判成占用")
		})
	}
}

// TestProcInWorkDir_OrphanScanKeepsCmdlineMatch 守护两处判据的**刻意差异**：
// 孤儿扫描用的 procInWorkDir 必须继续接受「cwd 为空、仅命令行含路径」的命中。
//
// 若有人日后为了「统一口径」把 procInWorkDir 收紧成预检那样，会直接削弱扫描召回：
// wrapper 死了但 Java 还活着时，Java 的 cwd 常常取不到（/proc 权限、进程刚 exec），
// 只剩 cmdline 里的 jar 路径可认；漏报的后果是残留进程长期占着 session.lock 无人处置。
// 本用例是那条召回的护栏，与预检的收紧互不影响。
func TestProcInWorkDir_OrphanScanKeepsCmdlineMatch(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "pa")
	jarPath := filepath.Join(workDir, "server.jar")

	assert.True(t, procInWorkDir(ScannedProcess{Cmdline: "java -jar " + jarPath, Cwd: ""}, workDir),
		"孤儿扫描必须保留「cwd 取不到、命令行含路径」的召回")
	assert.True(t, procInWorkDir(ScannedProcess{Cmdline: "sh -c java -jar " + jarPath, Cwd: "/"}, workDir),
		"孤儿扫描的宽口径不得被预检的收紧波及")
	assert.False(t, procInWorkDir(ScannedProcess{Cmdline: "java -jar /other/server.jar", Cwd: "/"}, workDir),
		"既不提路径也不在该目录的进程不算命中")
}
