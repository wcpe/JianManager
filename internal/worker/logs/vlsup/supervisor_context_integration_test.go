package vlsup

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wcpe/JianManager/internal/platform/logasset"
)

// This optional check uses only the approved binary, a random loopback port,
// and a fresh test data root; it never controls an existing VL or Worker.
func TestIntegrationManagedVLStartOutlivesRequestContext(t *testing.T) {
	bin := os.Getenv("JM_VL_BIN")
	if bin == "" {
		t.Skip("JM_VL_BIN not set; skip optional approved VictoriaLogs integration")
	}
	require.NoError(t, VerifyAsset(bin, logasset.LinuxExecutableSHA256))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	s, err := New(Options{
		BinaryPath:         bin,
		AssetSHA256:        logasset.LinuxExecutableSHA256,
		DataRoot:           t.TempDir(),
		AuthUsername:       "constructed",
		AuthPassword:       "constructed-start-context-secret",
		Ports:              map[Namespace]int{NamespaceHot: port},
		MemoryAllowedBytes: map[Namespace]int64{NamespaceHot: 128 << 20},
		MemoryLimitBytes:   256 << 20,
		Sink:               &RecordingSink{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { s.StopAll(context.Background()) })
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelRequest()
	require.NoError(t, s.Start(requestCtx, NamespaceHot))
	healthCtx, cancelHealth := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHealth()
	require.NoError(t, s.WaitHealthy(healthCtx, NamespaceHot, 5*time.Second))
	s.mu.Lock()
	p := s.instances[NamespaceHot].proc.(*realProcess)
	s.mu.Unlock()

	cancelRequest()
	require.Never(t, p.Exited, 250*time.Millisecond, time.Millisecond,
		"returning from the start RPC must not kill the approved VL process")
	require.NoError(t, s.Health(healthCtx, NamespaceHot))
	status, err := s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, StateRunning, status.State)
	require.Equal(t, p.PID(), status.PID)
	t.Logf("approved VL pid=%d port=%d remains healthy/RUNNING after request cancellation", p.PID(), port)

	s.StopAll(requestCtx)
	v := requireStartContextChildReaped(t, p)
	require.True(t, v.Stopped)
	require.Zero(t, v.ExitCode, "approved VL should stop gracefully")
	status, err = s.Status(NamespaceHot)
	require.NoError(t, err)
	require.Equal(t, StateStopped, status.State)
	require.Zero(t, status.PID)
	listener, err = net.Listen("tcp", ListenAddr(port))
	require.NoError(t, err, "StopAll must release the random test port")
	require.NoError(t, listener.Close())
	t.Logf("StopAll with canceled ctx reaped approved VL pid=%d and released port=%d", p.PID(), port)
}
