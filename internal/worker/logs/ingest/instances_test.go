package ingest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/worker/logs/acquire"
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

func TestInstanceStdioPersistsRawBeforePollingAndRestoresBindings(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root := t.TempDir()
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	require.NoError(t, m.RegisterInstance("uuid-1", "inst:1", "holder-g1", "STDIO_PRIMARY", t.TempDir()))
	require.NoError(t, m.AppendInstanceOutput("uuid-1", "stdout", []byte("stdout raw\n")))
	require.NoError(t, m.AppendInstanceOutput("uuid-1", "stderr", []byte("stderr raw\n")))
	data, err := os.ReadFile(m.rawInstancePath(m.state.Instances["uuid-1"], "stdout"))
	require.NoError(t, err)
	require.Equal(t, "stdout raw\n", string(data))
	require.Empty(t, cat.Keys(), "no query publication before polling")
	m.pollOnce()
	rec, ok := cat.Get(catalog.PartitionKey{StorageNamespace: "inst:1", UTCDay: runtimeTestUTCDay()})
	require.True(t, ok)
	require.Len(t, rec.PublishedProjection.SourceProjections, 2)
	restarted, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: catalog.New(cat.Journal()), Journal: cat.Journal()})
	require.NoError(t, err)
	require.Len(t, restarted.pipes, 2, "sources restore without CP or config replay")
	require.NoError(t, restarted.AppendInstanceOutput("uuid-1", "stdout", []byte("after restart\n")))
	restarted.pollOnce()
	require.Len(t, durableEvents(t, restarted, "inst:1/stdout/holder-g1"), 2)
	require.Len(t, durableEvents(t, restarted, "inst:1/stderr/holder-g1"), 1)
	require.NoError(t, os.Remove(restarted.rawInstancePath(restarted.state.Instances["uuid-1"], "stdout")))
	require.Error(t, restarted.AppendInstanceOutput("uuid-1", "stdout", []byte("not captured\n")))
	require.False(t, restarted.CutoverReadiness().LedgerReady)
	require.ErrorContains(t, restarted.ResolveCoveredGaps(), "projection alone cannot resolve")
}

func TestInstanceFilePrimaryDoesNotDoubleCollectConsoleOutput(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(work, "logs"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(work, "logs", "latest.log"), []byte("file canonical\n"), 0o600))
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)
	require.NoError(t, m.RegisterInstance("uuid-1", "inst:1", "holder-g1", "FILE_PRIMARY", work))
	require.Equal(t, []string{"unbound"}, m.MissingInstanceBindings([]string{"uuid-1", "unbound"}))
	require.NoError(t, m.RegisterInstance("uuid-1", "inst:1", "holder-g1", "FILE_PRIMARY", work))
	require.NoError(t, m.AppendInstanceOutput("uuid-1", "stdout", []byte("console copy\n")))
	m.pollOnce()
	require.Len(t, m.pipes, 1)
	events := durableEvents(t, m, "inst:1/file/holder-g1")
	require.Len(t, events, 1)
	require.Equal(t, "file canonical", events[0].Message)
}

func TestInstanceOutputSpoolsBeforeBindingAndFlushesInOrder(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)

	err = m.AppendInstanceOutput("uuid-spool", "stdout", []byte("before-1\n"))
	require.ErrorIs(t, err, ErrInstanceBindingPending)
	err = m.AppendInstanceOutput("uuid-spool", "stderr", []byte("error-before\n"))
	require.ErrorIs(t, err, ErrInstanceBindingPending)
	require.FileExists(t, m.pendingInstancePath("uuid-spool", "stdout"))
	require.FileExists(t, m.pendingInstancePath("uuid-spool", "stderr"))
	readiness := m.CutoverReadiness()
	require.False(t, readiness.LedgerReady)
	require.Contains(t, strings.Join(readiness.Reasons, ","), "pending_spool:", "%v", readiness.Reasons)

	require.NoError(t, m.RegisterInstance("uuid-spool", "inst:spool", "holder-g1", "STDIO_PRIMARY", work))
	require.NoError(t, m.AppendInstanceOutput("uuid-spool", "stdout", []byte("after-1\n")))
	binding := m.state.Instances["uuid-spool"]
	stdout, err := os.ReadFile(m.rawInstancePath(binding, "stdout"))
	require.NoError(t, err)
	stderr, err := os.ReadFile(m.rawInstancePath(binding, "stderr"))
	require.NoError(t, err)
	require.Equal(t, "before-1\nafter-1\n", string(stdout))
	require.Equal(t, "error-before\n", string(stderr))
	require.NoFileExists(t, m.pendingInstancePath("uuid-spool", "stdout"))
	require.NoFileExists(t, m.pendingInstancePath("uuid-spool", "stderr"))
}

func TestInstanceOutputPendingSpoolSurvivesRestart(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	first, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)
	require.ErrorIs(t, first.AppendInstanceOutput("uuid-restart", "stdout", []byte("survives restart\n")), ErrInstanceBindingPending)
	require.NoError(t, first.Stop())

	restarted, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)
	require.NoError(t, restarted.RegisterInstance("uuid-restart", "inst:restart", "holder-g1", "STDIO_PRIMARY", work))
	binding := restarted.state.Instances["uuid-restart"]
	data, err := os.ReadFile(restarted.rawInstancePath(binding, "stdout"))
	require.NoError(t, err)
	require.Equal(t, "survives restart\n", string(data))
}

func TestInstanceOutputPendingFlushFailureKeepsGapAndPause(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)
	require.ErrorIs(t, m.AppendInstanceOutput("uuid-fail", "stdout", []byte("must-not-disappear\n")), ErrInstanceBindingPending)
	binding := InstanceBinding{UUID: "uuid-fail", Generation: "holder-g1"}
	rawPath := m.rawInstancePath(binding, "stdout")
	require.NoError(t, os.MkdirAll(filepath.Dir(rawPath), 0o700))
	require.NoError(t, os.Mkdir(rawPath, 0o700))

	err = m.RegisterInstance("uuid-fail", "inst:fail", "holder-g1", "STDIO_PRIMARY", work)
	require.Error(t, err)
	pipe := m.pipes["inst:fail/stdout/holder-g1"]
	require.NotNil(t, pipe)
	entry := pipe.Ledger().Get(pipe.Key())
	require.NotNil(t, entry)
	require.True(t, entry.AcquirePaused)
	require.Len(t, entry.Gaps, 1)
	require.Equal(t, "STDIO_RAW_WRITE_FAILED", entry.Gaps[0].Reason)
	require.False(t, m.CutoverReadiness().LedgerReady)
}

func TestInstanceOutputBindingRacePreservesAllBytes(t *testing.T) {
	vl, _ := newProjectionVL(t)
	root, work := t.TempDir(), t.TempDir()
	m, err := newTestManager(t, Options{Root: root, VL: vl, Catalog: catalog.New(nil)})
	require.NoError(t, err)
	const count = 32
	chunk := []byte("concurrent-output\n")
	start := make(chan struct{})
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		go func() {
			<-start
			errs <- m.AppendInstanceOutput("uuid-race", "stdout", chunk)
		}()
	}
	close(start)
	require.NoError(t, m.RegisterInstance("uuid-race", "inst:race", "holder-g1", "STDIO_PRIMARY", work))
	for i := 0; i < count; i++ {
		err := <-errs
		if err != nil {
			require.ErrorIs(t, err, ErrInstanceBindingPending)
		}
	}
	binding := m.state.Instances["uuid-race"]
	data, err := os.ReadFile(m.rawInstancePath(binding, "stdout"))
	require.NoError(t, err)
	require.Len(t, data, count*len(chunk))
}

// M-6 回归：pending 暂存必须受容量门禁约束。
//
// 缺陷形态：未绑定实例的输出在绑定到达前无 pipeline/ledger 兜底，而暂存追加路径既
// 无单流/总量上限、也不做磁盘阈值检查（托管 Raw 写入有该检查，暂存没有）。持续
// stdout/stderr 因此可耗尽 Worker 数据盘，进而拖垮 ingest.state.json、事件段、WAL
// 与 VL 运行时；且未绑定时没有 ledger，写失败不会进入 gap/pause 语义。
func TestPendingSpoolEnforcesCapacityLimits(t *testing.T) {
	newManager := func(t *testing.T, budget *acquire.CapacityBudget) *Manager {
		t.Helper()
		vl, _ := newProjectionVL(t)
		cat := catalog.New(nil)
		opts := Options{Root: t.TempDir(), VL: vl, Catalog: cat, Journal: cat.Journal()}
		if budget != nil {
			opts.CapacityProvider = func() (acquire.CapacityBudget, error) { return *budget, nil }
		}
		m, err := newTestManager(t, opts)
		require.NoError(t, err)
		return m
	}

	t.Run("per_stream_limit", func(t *testing.T) {
		m := newManager(t, nil)
		m.SetPendingSpoolLimits(16, 1<<20)
		// 未注册绑定 → 输出落 pending 暂存；成功写入也返回 Pending 哨兵（既有契约）。
		require.ErrorIs(t, m.AppendInstanceOutput("uuid-limit", "stdout", []byte("12345678")), ErrInstanceBindingPending)
		require.ErrorIs(t, m.AppendInstanceOutput("uuid-limit", "stdout", []byte("12345678")), ErrInstanceBindingPending)
		err := m.AppendInstanceOutput("uuid-limit", "stdout", []byte("x"))
		require.Error(t, err, "超过单流上限必须拒绝写入")
		require.NotErrorIs(t, err, ErrInstanceBindingPending, "应为额度错误而非 Pending 哨兵")
		require.Contains(t, err.Error(), "per-stream limit")
		// 另一条流有独立额度，不应被前一条流的上限牵连。
		require.ErrorIs(t, m.AppendInstanceOutput("uuid-limit", "stderr", []byte("ok")), ErrInstanceBindingPending)
	})

	t.Run("total_limit", func(t *testing.T) {
		m := newManager(t, nil)
		m.SetPendingSpoolLimits(1<<20, 20)
		require.ErrorIs(t, m.AppendInstanceOutput("uuid-a", "stdout", []byte("12345678")), ErrInstanceBindingPending)
		require.ErrorIs(t, m.AppendInstanceOutput("uuid-b", "stdout", []byte("12345678")), ErrInstanceBindingPending)
		err := m.AppendInstanceOutput("uuid-c", "stdout", []byte("12345678"))
		require.Error(t, err, "超过总量上限必须拒绝写入")
		require.NotErrorIs(t, err, ErrInstanceBindingPending)
		require.Contains(t, err.Error(), "total limit")
	})

	t.Run("disk_pause_threshold", func(t *testing.T) {
		budget := acquire.DefaultCapacityBudget()
		budget.DiskUsagePercent = 95
		m := newManager(t, &budget)
		err := m.AppendInstanceOutput("uuid-disk", "stdout", []byte("data"))
		require.Error(t, err, "达到磁盘暂停阈值时暂存也不得继续写")
		require.Contains(t, err.Error(), "pause threshold")
		require.NoFileExists(t, m.pendingInstancePath("uuid-disk", "stdout"), "被拒绝的写入不得落盘")
	})

	t.Run("under_limit_succeeds", func(t *testing.T) {
		m := newManager(t, nil)
		m.SetPendingSpoolLimits(1<<20, 1<<20)
		require.ErrorIs(t, m.AppendInstanceOutput("uuid-ok", "stdout", []byte("within limits")), ErrInstanceBindingPending)
		require.FileExists(t, m.pendingInstancePath("uuid-ok", "stdout"))
	})
}

// createDirLink 创建一个指向 target 的目录链接（链接级）。
//
// 为什么不用单纯的 os.Symlink：Windows 上创建符号链接需要 SeCreateSymbolicLinkPrivilege
// （普通账户报 "A required privilege is not held by the client"），而目录 junction
// （mklink /J）无需任何特权且同样是「打开动作会跟随的重解析点」——对被验判据完全等价。
// 故先试符号链接（Unix 与已开启开发者模式的 Windows），失败则回退 junction；两者都不可用时
// 才跳过（环境无法构造该场景，不伪造成通过）。
func createDirLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Fatalf("创建符号链接失败: %v", err)
	}
	out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Skipf("环境无法创建符号链接与 junction（普通账户且未开启开发者模式）: %v, output=%s", err, out)
	}
}

// M-7 回归：拒绝符号链接工作目录，但不误伤合法路径。
//
// 缺陷形态：RegisterInstance 只对 workDir 做 filepath.Clean，既不校验符号链接也不
// 收敛受管根。FILE_PRIMARY 会按该路径拼 logs/latest.log 与 *.gz 并直接打开，而打开
// 动作跟随符号链接——于是指向别处的链接会让采集器读到约定目录之外的文件。
//
// 注意：数据根之外的绝对路径是既有契约（dataroot.Root.Abs 保留绝对输入，生产存在
// 合法的根外实例目录），故本修复只拒绝「解析后偏离原路径」的链接，不收窄绝对路径。
func TestRegisterInstanceRejectsSymlinkedWorkDir(t *testing.T) {
	vl, _ := newProjectionVL(t)
	cat := catalog.New(nil)
	m, err := newTestManager(t, Options{Root: t.TempDir(), VL: vl, Catalog: cat, Journal: cat.Journal()})
	require.NoError(t, err)

	base := t.TempDir()
	real := filepath.Join(base, "real-instance")
	require.NoError(t, os.MkdirAll(filepath.Join(real, "logs"), 0o700))

	t.Run("symlinked_workdir_rejected", func(t *testing.T) {
		link := filepath.Join(base, "linked-instance")
		createDirLink(t, real, link)
		err := m.RegisterInstance("uuid-link", "inst:1", "g1", "FILE_PRIMARY", link)
		require.Error(t, err, "指向别处的工作目录不得用于采集")
		require.Contains(t, err.Error(), "symlinked log work dir")
	})

	t.Run("real_directory_accepted", func(t *testing.T) {
		require.NoError(t, m.RegisterInstance("uuid-real", "inst:2", "g1", "FILE_PRIMARY", real))
	})

	t.Run("outside_root_absolute_still_accepted", func(t *testing.T) {
		// 根外绝对目录是既有契约（外来接管实例），修复不得打断它。
		outside := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(outside, "logs"), 0o700))
		require.NoError(t, m.RegisterInstance("uuid-outside", "inst:3", "g1", "FILE_PRIMARY", outside))
	})

	t.Run("filesystem_root_rejected", func(t *testing.T) {
		err := m.RegisterInstance("uuid-root", "inst:4", "g1", "FILE_PRIMARY", "/")
		require.Error(t, err, "不得以文件系统根作为采集目录")
	})
}
