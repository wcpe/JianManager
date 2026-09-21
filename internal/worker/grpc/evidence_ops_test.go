package grpc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/daemon"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestProbeInstanceEvidence_DaemonPIDFile PID 文件显示 wrapper 存活时证据 running=true（FR-455③）。
func TestProbeInstanceEvidence_DaemonPIDFile(t *testing.T) {
	dir := t.TempDir()
	mgr := process.NewManager(dir)
	uuid := "probe-daemon"
	require.NoError(t, daemon.NewPIDFile(filepath.Join(dir, uuid+".pid")).WriteRecord(daemon.PIDRecord{
		WrapperPID:   os.Getpid(), // 以本测试进程充当存活的 wrapper
		InstanceUUID: uuid,
		WorkDir:      dir,
	}))

	s := NewServer(mgr, "node-probe", nil, nil, nil)
	resp, err := s.ProbeInstanceEvidence(context.Background(), &workerpb.ProbeInstanceEvidenceRequest{
		InstanceUuids: []string{uuid},
	})
	require.NoError(t, err)
	require.Len(t, resp.Evidence, 1)
	require.True(t, resp.Evidence[0].Running, "存活的 wrapper 应判为仍在运行")
	require.True(t, resp.Evidence[0].ProcessAlive)
	require.Equal(t, int32(os.Getpid()), resp.Evidence[0].RootPid)
}

// TestProbeInstanceEvidence_UnknownInstance 无 PID 文件、无内存表条目 → 无存活证据（running=false）。
func TestProbeInstanceEvidence_UnknownInstance(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	s := NewServer(mgr, "node-probe", nil, nil, nil)
	resp, err := s.ProbeInstanceEvidence(context.Background(), &workerpb.ProbeInstanceEvidenceRequest{
		InstanceUuids: []string{"ghost", ""},
	})
	require.NoError(t, err)
	// 空 UUID 被跳过。
	require.Len(t, resp.Evidence, 1)
	require.False(t, resp.Evidence[0].Running)
}

// TestProbeInstanceEvidence_BatchCap 超过上限时截断，防止异常大请求拖垮 Worker。
func TestProbeInstanceEvidence_BatchCap(t *testing.T) {
	mgr := process.NewManager(t.TempDir())
	s := NewServer(mgr, "node-probe", nil, nil, nil)
	uuids := make([]string, maxEvidenceBatch+10)
	for i := range uuids {
		uuids[i] = fmt.Sprintf("u-%d", i)
	}
	resp, err := s.ProbeInstanceEvidence(context.Background(), &workerpb.ProbeInstanceEvidenceRequest{
		InstanceUuids: uuids,
	})
	require.NoError(t, err)
	require.Len(t, resp.Evidence, maxEvidenceBatch)
}
