package vlsup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"testing"
	"time"
)

// 辅助进程只处理本测试的构造输出，不调用shell、不接触真实日志。
func TestExecDiagnosticHelper(t *testing.T) {
	mode := os.Getenv("JM_VL_DIAGNOSTIC_HELPER")
	if mode == "" {
		return
	}
	secret := "constructed-split-secret-173"
	switch mode {
	case "reject":
		fmt.Fprintln(os.Stderr, "flag provided but not defined: constructed.invalid; password="+secret)
		os.Exit(2)
	case "nonzero":
		fmt.Fprintln(os.Stderr, "constructed disk failure")
		os.Exit(7)
	case "short":
		time.Sleep(800 * time.Millisecond)
		os.Exit(7)
	case "normal":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		fmt.Println("constructed ready")
		<-ch
		os.Exit(0)
	case "storm":
		var wg sync.WaitGroup
		for _, f := range []*os.File{os.Stdout, os.Stderr} {
			wg.Add(1)
			go func(f *os.File) {
				defer wg.Done()
				fmt.Fprintln(f, strings.Repeat("x", 1<<20)+secret)
				for i := 0; i < 10000; i++ {
					fmt.Fprintln(f, "storm", secret)
				}
			}(f)
		}
		wg.Wait()
		os.Exit(9)
	}
	os.Exit(8)
}

func startDiagnosticHelper(t *testing.T, mode string, sink Sink) ExecHandle {
	t.Helper()
	p, err := (RealFactory{Env: []string{"JM_VL_DIAGNOSTIC_HELPER=" + mode}, DiagnosticSink: sink}).Start(context.Background(), os.Args[0], []string{"-test.run=^TestExecDiagnosticHelper$", "--", "-httpAuth.password=constructed-split-secret-173"})
	if err != nil {
		t.Fatal(err)
	}
	h := p.(ExecHandle)
	t.Cleanup(func() { _ = h.Stop(100 * time.Millisecond) })
	return h
}

func awaitDiagnosticExit(t *testing.T, h ExecHandle) ProcessSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !h.Exited() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !h.Exited() {
		t.Fatal("子进程未在有界窗口退出，可能输出流未持续排空")
	}
	p := h.(*realProcess)
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("单Wait未结束")
	}
	select {
	case <-p.diagnostics.reported:
	case <-time.After(time.Second):
		t.Fatal("诊断落点未完成")
	}
	return h.Snapshot()
}

func TestExecHandleRecordsActualExitAndBoundedRedactedDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		mode string
		code int
		root string
	}{{"reject", 2, "flag provided but not defined"}, {"nonzero", 7, "constructed disk failure"}, {"storm", 9, ""}} {
		t.Run(tc.mode, func(t *testing.T) {
			sink := &RecordingSink{}
			h := startDiagnosticHelper(t, tc.mode, sink)
			v := awaitDiagnosticExit(t, h)
			if !v.ExitCodeKnown || v.ExitCode != tc.code || v.StartedAt.IsZero() || v.ExitedAt.IsZero() || v.ExitedAt.Before(v.StartedAt) {
				t.Fatalf("真实退出证据错误: %+v", v)
			}
			before := v.ExitedAt
			time.Sleep(time.Millisecond)
			if !h.Snapshot().ExitedAt.Equal(before) {
				t.Fatal("读取时间不能替代实际退出时间")
			}
			all := strings.Join(v.Diagnostics, " ")
			for _, r := range sink.Snapshot() {
				all += " " + r.Err.Error()
			}
			if strings.Contains(all, "constructed-split-secret-173") {
				t.Fatal("秘密进入诊断快照或独立日志")
			}
			if tc.root != "" && !strings.Contains(all, tc.root) {
				t.Fatalf("根因丢失: %s", all)
			}
			if len(v.Diagnostics) > DiagnosticMaxLines {
				t.Fatal("环形缓冲超限")
			}
			for _, line := range v.Diagnostics {
				if len(line) > DiagnosticMaxLineBytes+16 {
					t.Fatal("脱敏后单行超限")
				}
			}
			if tc.mode == "storm" && (v.DroppedLines == 0 || v.TruncatedLines == 0) {
				t.Fatalf("风暴必须明确记录限流和截断: %+v", v)
			}
		})
	}
}

func TestExecHandleNormalStopIsSingleWaitAndExitZero(t *testing.T) {
	h := startDiagnosticHelper(t, "normal", &RecordingSink{})
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(h.Snapshot().Diagnostics, " "), "ready") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// grace 是「等子进程优雅退出」的预算，超了就走 Kill ⇒ ExitCode=-1。
	//
	// 固定 1 秒在普通构建下很宽裕，但在 -race 下不够：竞态检测器把整条路径放慢几倍，
	// 子进程收尾往往赶不上，于是「正常停止 ⇒ ExitCode==0」变红，而那个 -1 只是插桩开销。
	// 故只放宽预算，**三条断言（Stopped / ExitCodeKnown / ExitCode==0）一字不改**。
	grace := time.Second
	if raceEnabled {
		grace = 5 * time.Second
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := h.Stop(grace); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	v := awaitDiagnosticExit(t, h)
	if !v.Stopped || v.ExitCode != 0 || !v.ExitCodeKnown {
		t.Fatalf("正常停止错误: %+v", v)
	}
}

func TestDiagnosticWriterRedactsSecretsAcrossWritesAndTruncation(t *testing.T) {
	sink := &RecordingSink{}
	d := newProcessDiagnostics("hot", sink, []string{"alpha-secret-omega"})
	w := &diagnosticWriter{d: d, stream: "stderr"}
	for _, part := range []string{"prefix alpha-", "secret-", "omega suffix\n", strings.Repeat("x", DiagnosticMaxLineBytes-5), "alpha-secret-omega\n"} {
		_, _ = w.Write([]byte(part))
	}
	w.flush()
	d.close()
	<-d.reported
	lines, _, truncated := d.snapshot()
	all := strings.Join(lines, " ")
	if strings.Contains(all, "alpha-") || strings.Contains(all, "secret-omega") || !strings.Contains(all, "REDACTED") || truncated != 1 {
		t.Fatalf("跨Write或截断脱敏失败: %q, truncated=%d", all, truncated)
	}
	if len(w.pending) != 0 {
		t.Fatal("完整行后不能残留无界缓冲")
	}
}

type shortLifeFactory struct {
	count int
	last  ExecHandle
}

func (f *shortLifeFactory) Start(ctx context.Context, _ string, args []string) (Process, error) {
	p, err := (RealFactory{Env: []string{"JM_VL_DIAGNOSTIC_HELPER=short"}, DiagnosticSink: &RecordingSink{}}).Start(ctx, os.Args[0], append([]string{"-test.run=^TestExecDiagnosticHelper$", "--"}, args...))
	if err == nil {
		f.count++
		f.last = p.(ExecHandle)
	}
	return p, err
}

func TestGuardianRealShortLifeDoesNotRestartAtDelayedFirstObservation(t *testing.T) {
	f := &shortLifeFactory{}
	s := newTestSupervisor(t, f, &RecordingSink{}, false)
	if err := s.Start(context.Background(), NamespaceHot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.StopAll(context.Background()) })
	v := awaitDiagnosticExit(t, f.last)
	clock := newFakeClock()
	clock.Advance(31 * time.Second)
	s.checkOnce(context.Background(), guardianTestOptions(clock))
	if f.count != 1 {
		t.Fatal("真实短命进程在死后31s首次观察时不能重启")
	}
	if v.ExitedAt.Sub(v.StartedAt) >= 30*time.Second {
		t.Fatal("夹具实际活过了稳定窗，反例无效")
	}
	t.Logf("真实短命进程 started=%s exited=%s actual_alive=%s delayed_guard=31s starts=%d", v.StartedAt.Format(time.RFC3339Nano), v.ExitedAt.Format(time.RFC3339Nano), v.ExitedAt.Sub(v.StartedAt), f.count)
}

type secretStopProcess struct{ Process }

func (p secretStopProcess) Stop(time.Duration) error {
	return errors.New("constructed stop failure auth=local-only")
}

func TestRuntimeRegressionStopAndHealthErrorsAreRedacted(t *testing.T) {
	for _, mode := range []string{"stop", "health"} {
		t.Run(mode, func(t *testing.T) {
			sink := &RecordingSink{}
			s := newTestSupervisor(t, &fakeFactory{}, sink, false)
			if err := s.Start(context.Background(), NamespaceHot); err != nil {
				t.Fatal(err)
			}
			var err error
			if mode == "stop" {
				s.mu.Lock()
				s.instances[NamespaceHot].proc = secretStopProcess{s.instances[NamespaceHot].proc}
				s.mu.Unlock()
				err = s.Stop(context.Background(), NamespaceHot)
			} else {
				s.newClient = func(string, string, string) (*Client, error) {
					return nil, errors.New("constructed client failure auth=local-only")
				}
				err = s.Health(context.Background(), NamespaceHot)
			}
			if err == nil {
				t.Fatal("构造错误必须返回")
			}
			status, _ := s.Status(NamespaceHot)
			all := err.Error() + status.LastError
			for _, r := range sink.Snapshot() {
				all += r.Err.Error()
			}
			if strings.Contains(all, "local-only") {
				t.Fatal("生命周期错误不能把凭据传播到error/status/log")
			}
		})
	}
}

type rejectingSecretFactory struct{}

func (rejectingSecretFactory) Start(ctx context.Context, _ string, args []string) (Process, error) {
	return (RealFactory{Env: []string{"JM_VL_DIAGNOSTIC_HELPER=reject"}, DiagnosticSink: &RecordingSink{}}).Start(ctx, os.Args[0], append([]string{"-test.run=^TestExecDiagnosticHelper$", "--"}, args...))
}

func TestSupervisorNeverLeaksProcessSecretToStatusOrReturnedError(t *testing.T) {
	bin, sha := writeFakeBinary(t)
	sink := &RecordingSink{}
	s, err := New(Options{BinaryPath: bin, AssetSHA256: sha, DataRoot: t.TempDir(), AuthUsername: "jm", AuthPassword: "constructed-split-secret-173", Factory: rejectingSecretFactory{}, Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Start(context.Background(), NamespaceHot)
	if err == nil {
		t.Fatal("错误参数进程必须拒绝启动")
	}
	st, _ := s.Status(NamespaceHot)
	all := err.Error() + st.LastError
	for _, r := range sink.Snapshot() {
		all += r.Err.Error()
	}
	if strings.Contains(all, "constructed-split-secret-173") {
		t.Fatal("秘密进入返回error/status/log")
	}
	if !st.ExitCodeKnown || st.ExitCode != 2 || st.ExitedAt.IsZero() {
		t.Fatalf("失败状态必须保留真实退出证据: %+v", st)
	}
}
