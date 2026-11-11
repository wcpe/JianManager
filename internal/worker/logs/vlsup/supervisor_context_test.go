package vlsup

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSupervisorStartContextChild only runs in a child created by these tests.
// It never opens a production data directory or a listening port.
func TestSupervisorStartContextChild(t *testing.T) {
	mode := os.Getenv("JM_VL_START_CONTEXT_CHILD")
	if mode == "" {
		return
	}
	if mode == "failed" {
		fmt.Fprintln(os.Stderr, "constructed startup failure")
		os.Exit(7)
	}
	if mode == "ignore-interrupt" {
		signal.Ignore(os.Interrupt)
		fmt.Println("constructed context child ready")
		for {
			time.Sleep(time.Hour)
		}
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	fmt.Println("constructed context child ready")
	<-interrupt
	os.Exit(0)
}

type startContextChildFactory struct {
	mode           string
	processContext context.Context
	proc           *realProcess
	onObservation  func(*realProcess)
}

func (f *startContextChildFactory) Start(ctx context.Context, _ string, args []string) (Process, error) {
	return f.start(ctx, "", args)
}

func (f *startContextChildFactory) start(ctx context.Context, ns Namespace, args []string) (Process, error) {
	f.processContext = ctx
	factory := RealFactory{
		Env:            []string{"JM_VL_START_CONTEXT_CHILD=" + f.mode},
		DiagnosticSink: &RecordingSink{},
	}
	args = append([]string{"-test.run=^TestSupervisorStartContextChild$", "--"}, args...)
	var proc Process
	var err error
	if ns == "" {
		proc, err = factory.Start(ctx, os.Args[0], args)
	} else {
		proc, err = factory.StartNamespace(ctx, ns, os.Args[0], args)
	}
	if err != nil {
		return nil, err
	}
	f.proc = proc.(*realProcess)
	if f.onObservation != nil {
		return &startObservedChild{realProcess: f.proc, observe: f.onObservation}, nil
	}
	return proc, nil
}

type namespaceStartContextChildFactory struct {
	*startContextChildFactory
	namespace Namespace
}

func (f *namespaceStartContextChildFactory) StartNamespace(ctx context.Context, ns Namespace, _ string, args []string) (Process, error) {
	f.namespace = ns
	return f.start(ctx, ns, args)
}

type startObservedChild struct {
	*realProcess
	once    sync.Once
	observe func(*realProcess)
}

func (p *startObservedChild) Exited() bool {
	p.once.Do(func() { p.observe(p.realProcess) })
	return p.realProcess.Exited()
}

func requireStartContextChildReady(t *testing.T, p *realProcess) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.Contains(strings.Join(p.Snapshot().Diagnostics, " "), "context child ready")
	}, 5*time.Second, time.Millisecond, "test child did not become ready")
}

func requireStartContextChildReaped(t *testing.T, p *realProcess) ProcessSnapshot {
	t.Helper()
	v := awaitDiagnosticExit(t, p)
	require.True(t, v.Exited)
	require.True(t, v.ExitCodeKnown)
	if runtime.GOOS == "linux" {
		require.False(t, processAlive(p.PID()), "Wait must reap the child PID")
	}
	t.Logf("child pid=%d reaped; Wait and diagnostics completed; stopped=%t exit_code=%d", p.PID(), v.Stopped, v.ExitCode)
	return v
}

func TestSupervisorManagedProcessOutlivesStartContext(t *testing.T) {
	for _, namespaceFactory := range []bool{false, true} {
		t.Run(fmt.Sprintf("namespace_factory=%t", namespaceFactory), func(t *testing.T) {
			f := &startContextChildFactory{mode: "running"}
			var factory CmdFactory = f
			if namespaceFactory {
				factory = &namespaceStartContextChildFactory{startContextChildFactory: f}
			}
			s := newTestSupervisor(t, factory, &RecordingSink{}, false)
			t.Cleanup(func() { s.StopAll(context.Background()) })
			type requestValueKey struct{}
			ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), requestValueKey{}, "start-request"), 10*time.Second)
			defer cancel()
			require.NoError(t, s.Start(ctx, NamespaceHot))
			p := f.proc
			require.NotNil(t, p)
			requireStartContextChildReady(t, p)

			cancel() // Returning from an RPC cancels this exact request context.
			require.Never(t, p.Exited, 250*time.Millisecond, time.Millisecond,
				"a successful managed start must outlive its request context")
			require.Nil(t, f.processContext.Done())
			require.NoError(t, f.processContext.Err())
			_, hasDeadline := f.processContext.Deadline()
			require.False(t, hasDeadline, "the child must not inherit an RPC deadline")
			require.Equal(t, "start-request", f.processContext.Value(requestValueKey{}))
			if f, ok := factory.(*namespaceStartContextChildFactory); ok {
				require.Equal(t, NamespaceHot, f.namespace)
			}
			status, err := s.Status(NamespaceHot)
			require.NoError(t, err)
			require.Equal(t, StateRunning, status.State)
			require.Equal(t, p.PID(), status.PID)
			t.Logf("request canceled after successful Start; child pid=%d remains RUNNING", p.PID())

			// Worker shutdown must still reclaim the child even with a canceled ctx.
			s.StopAll(ctx)
			v := requireStartContextChildReaped(t, p)
			require.True(t, v.Stopped)
			status, err = s.Status(NamespaceHot)
			require.NoError(t, err)
			require.Equal(t, StateStopped, status.State)
			require.Zero(t, status.PID)
		})
	}
}

func TestSupervisorStartCanceledBeforeSpawn(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline_expired=%t", expired), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if expired {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				want = context.DeadlineExceeded
			}
			cancel()
			f := &fakeFactory{}
			s := newTestSupervisor(t, f, &RecordingSink{}, false)
			t.Cleanup(func() { s.StopAll(context.Background()) })
			err := s.Start(ctx, NamespaceHot)
			require.Zero(t, f.startCount(), "an already canceled start must never spawn")
			require.ErrorIs(t, err, want)
			status, err := s.Status(NamespaceHot)
			require.NoError(t, err)
			require.Equal(t, StateFailed, status.State)
			require.Zero(t, status.PID)
		})
	}
}

func TestSupervisorStartCancellationDuringObservationReapsChild(t *testing.T) {
	for _, mode := range []string{"running", "ignore-interrupt"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var canceledAt time.Time
			f := &startContextChildFactory{mode: mode, onObservation: func(p *realProcess) {
				requireStartContextChildReady(t, p)
				canceledAt = time.Now()
				cancel()
			}}
			s := newTestSupervisor(t, f, &RecordingSink{}, false)
			t.Cleanup(func() { s.StopAll(context.Background()) })
			err := s.Start(ctx, NamespaceHot)
			require.NotZero(t, canceledAt, "cancellation must occur inside startup observation")
			require.Less(t, time.Since(canceledAt), DefaultStopGrace+2*time.Second,
				"cancellation cleanup must use a bounded Stop even if SIGINT is ignored")
			require.ErrorIs(t, err, context.Canceled)
			v := requireStartContextChildReaped(t, f.proc)
			require.True(t, v.Stopped)
			if mode == "ignore-interrupt" {
				require.Equal(t, -1, v.ExitCode, "cleanup must force-kill a child that ignores SIGINT")
			}
			status, statusErr := s.Status(NamespaceHot)
			require.NoError(t, statusErr)
			require.Equal(t, StateFailed, status.State)
			require.True(t, status.ExitCodeKnown)
			require.NotZero(t, status.ExitedAt)
			t.Logf("startup canceled; cleanup=%s; state=%s", time.Since(canceledAt), status.State)
		})
	}
}

func TestSupervisorStartupFailureReapsChildDiagnostics(t *testing.T) {
	f := &startContextChildFactory{mode: "failed"}
	s := newTestSupervisor(t, f, &RecordingSink{}, false)
	t.Cleanup(func() { s.StopAll(context.Background()) })
	require.Error(t, s.Start(context.Background(), NamespaceHot))
	v := requireStartContextChildReaped(t, f.proc)
	require.Equal(t, 7, v.ExitCode)
	status, err := s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, StateFailed, status.State)
	require.Equal(t, 7, status.ExitCode)
	require.Contains(t, status.LastError, "constructed startup failure")
}

func TestRealFactoryDirectContextCancellationStillReapsChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &startContextChildFactory{mode: "running"}
	proc, err := f.Start(ctx, "", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = proc.Stop(100 * time.Millisecond) })
	requireStartContextChildReady(t, f.proc)
	cancel()
	v := requireStartContextChildReaped(t, f.proc)
	require.False(t, v.Stopped, "direct RealFactory callers retain CommandContext cancellation")
	require.Equal(t, -1, v.ExitCode)
}

type cancelAtLivenessWindowEnd struct {
	Process
	cancel context.CancelFunc
	checks int
}

func (p *cancelAtLivenessWindowEnd) Exited() bool {
	p.checks++
	if p.checks == 2 {
		p.cancel()
	}
	return false
}

func TestStartupLivenessRejectsCancellationAtWindowEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &cancelAtLivenessWindowEnd{cancel: cancel}
	exited, err := waitStartupLiveness(ctx, p, 0)
	require.False(t, exited)
	require.ErrorIs(t, err, context.Canceled, "the final liveness check must not hide cancellation")
}

// gatedStartContextChildFactory holds the operation lock before Supervisor has
// registered a process, exposing shutdown's process-ownership handoff window.
type gatedStartContextChildFactory struct {
	*startContextChildFactory
	entered chan struct{}
	release chan struct{}
}

func (f *gatedStartContextChildFactory) Start(ctx context.Context, bin string, args []string) (Process, error) {
	close(f.entered)
	<-f.release
	return f.startContextChildFactory.Start(ctx, bin, args)
}

func TestSupervisorStopAllWaitsForInFlightStart(t *testing.T) {
	f := &gatedStartContextChildFactory{
		startContextChildFactory: &startContextChildFactory{mode: "running"},
		entered:                  make(chan struct{}),
		release:                  make(chan struct{}),
	}
	s := newTestSupervisor(t, f, &RecordingSink{}, false)
	startDone := make(chan error, 1)
	startReturned := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(f.release) }) }
	t.Cleanup(func() {
		release()
		select {
		case <-startReturned:
		case <-time.After(10 * time.Second):
			t.Error("in-flight test Start did not return")
		}
		s.StopAll(context.Background())
	})
	go func() {
		startDone <- s.Start(context.Background(), NamespaceHot)
		close(startReturned)
	}()
	select {
	case <-f.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not reach the process-ownership handoff window")
	}

	shutdownCtx, cancel := context.WithCancel(context.Background())
	cancel()
	stopDone := make(chan struct{})
	go func() {
		s.StopAll(shutdownCtx)
		close(stopDone)
	}()
	returnedEarly := false
	select {
	case <-stopDone:
		returnedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-startDone:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not finish after releasing the factory")
	}
	select {
	case <-stopDone:
	case <-time.After(DefaultStopGrace + 2*time.Second):
		t.Fatal("StopAll did not complete after Start")
	}
	require.False(t, returnedEarly, "StopAll must serialize with a Start that has not registered its process yet")
	v := requireStartContextChildReaped(t, f.proc)
	require.True(t, v.Stopped)
	status, err := s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, StateStopped, status.State)
	require.Zero(t, status.PID)
}
