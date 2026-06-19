package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wxys233/JianManager/internal/worker/process"
	"github.com/wxys233/JianManager/proto/workerpb"
)

// TestDownloadCore_LocalStub 离线验证「一键建服」核心下载机制：
// 给定下载 URL 与 sha256，Worker 流式下载到实例工作目录并校验摘要（FR-034）。
// 与 service/core_live_test.go（PaperMC 解析联网）互补，覆盖下载+校验的另一半。
func TestDownloadCore_LocalStub(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()

	const uuid = "11111111-1111-1111-1111-111111111111"
	workDir := filepath.Join(tmp, "inst")
	createResp, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "stub",
		StartCommand: "noop",
		WorkDir:      workDir,
		ProcessType:  "direct",
	})
	require.NoError(t, err)
	require.True(t, createResp.Success, createResp.Error)

	jar := []byte("fake-paper-core-jar-contents-1234567890")
	sum := sha256.Sum256(jar)
	hexSum := hex.EncodeToString(sum[:])

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jar)
	}))
	defer ts.Close()

	t.Run("正确 sha256 下载成功并落地", func(t *testing.T) {
		resp, err := srv.DownloadCore(ctx, &workerpb.DownloadCoreRequest{
			InstanceUuid: uuid,
			DestFilename: "server.jar",
			DownloadUrl:  ts.URL,
			Sha256:       hexSum,
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error)
		assert.Equal(t, int64(len(jar)), resp.Size)
		got, readErr := os.ReadFile(filepath.Join(workDir, "server.jar"))
		require.NoError(t, readErr)
		assert.Equal(t, jar, got)
	})

	t.Run("sha256 不符则拒绝并删除", func(t *testing.T) {
		resp, err := srv.DownloadCore(ctx, &workerpb.DownloadCoreRequest{
			InstanceUuid: uuid,
			DestFilename: "bad.jar",
			DownloadUrl:  ts.URL,
			Sha256:       "deadbeef",
		})
		require.NoError(t, err)
		assert.False(t, resp.Success)
		_, statErr := os.Stat(filepath.Join(workDir, "bad.jar"))
		assert.True(t, os.IsNotExist(statErr), "校验失败的文件应被删除")
	})

	t.Run("路径穿越的目标文件名被拒绝", func(t *testing.T) {
		resp, err := srv.DownloadCore(ctx, &workerpb.DownloadCoreRequest{
			InstanceUuid: uuid,
			DestFilename: "../escape.jar",
			DownloadUrl:  ts.URL,
		})
		require.NoError(t, err)
		assert.False(t, resp.Success)
	})

	t.Run("未注册实例下载失败", func(t *testing.T) {
		resp, err := srv.DownloadCore(ctx, &workerpb.DownloadCoreRequest{
			InstanceUuid: "no-such-instance",
			DownloadUrl:  ts.URL,
		})
		require.NoError(t, err)
		assert.False(t, resp.Success)
	})
}
