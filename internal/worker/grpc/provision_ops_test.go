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

	"github.com/wcpe/JianManager/internal/worker/artifactcache"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
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

// TestDownloadCore_TruncatedDownloadLeavesNoPartialFile 复现「核心下载中途截断仍留损坏 jar」的
// robustness bug（FR-034）：下载源限速 / 超时 / 连接中断导致 io.Copy 中途失败时，
// DownloadCore 必须报失败且**不得**在工作目录留下半成品 jar（否则用户会拿到 1.3MB 的坏核心）。
func TestDownloadCore_TruncatedDownloadLeavesNoPartialFile(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()

	const uuid = "55555555-5555-5555-5555-555555555555"
	workDir := filepath.Join(tmp, "inst-trunc")
	createResp, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "trunc", StartCommand: "noop", WorkDir: workDir, ProcessType: "direct",
	})
	require.NoError(t, err)
	require.True(t, createResp.Success, createResp.Error)

	// 声明较大的 Content-Length，却只写少量字节即结束 → 客户端读到 io.ErrUnexpectedEOF（截断）。
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial-truncated-core-bytes"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer ts.Close()

	resp, err := srv.DownloadCore(ctx, &workerpb.DownloadCoreRequest{
		InstanceUuid: uuid,
		DestFilename: "server.jar",
		DownloadUrl:  ts.URL,
		Sha256:       "0000000000000000000000000000000000000000000000000000000000000000",
	})
	require.NoError(t, err)
	assert.False(t, resp.Success, "截断下载必须报失败")
	_, statErr := os.Stat(filepath.Join(workDir, "server.jar"))
	assert.True(t, os.IsNotExist(statErr), "截断下载不得在工作目录留下损坏 jar")
}

// TestDownloadCore_CacheHitSkipsNetwork 验证节点制品缓存（FR-178）：
// 同一 sha256 第一次下载落地后存入缓存；删掉工作目录文件、关掉下载源后再建实例，
// 应从缓存秒拷命中、完全不走网络（命中痛点：删实例再建免重下大 jar）。
func TestInstallForgeServer_InstallsForgeAndSpongeForge(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()

	const uuid = "33333333-3333-3333-3333-333333333333"
	workDir := filepath.Join(tmp, "forge-inst")
	createResp, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "forge",
		StartCommand: "noop",
		WorkDir:      workDir,
		ProcessType:  "direct",
		JdkPath:      filepath.Join(tmp, "jdk"),
	})
	require.NoError(t, err)
	require.True(t, createResp.Success, createResp.Error)

	installer := []byte("fake-forge-installer")
	mod := []byte("fake-spongeforge-mod")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/installer.jar":
			_, _ = w.Write(installer)
		case "/spongeforge.jar":
			_, _ = w.Write(mod)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	oldRunner := runForgeInstaller
	runForgeInstaller = func(_ context.Context, javaBin, _ string, gotWorkDir string, env []string) ([]byte, error) {
		require.Contains(t, javaBin, filepath.Join("jdk", "bin"))
		require.Equal(t, workDir, gotWorkDir)
		require.NotEmpty(t, env)
		return []byte("安装完成"), os.WriteFile(filepath.Join(gotWorkDir, "forge-1.21.1-52.1.5-server.jar"), []byte("forge-server"), 0o644)
	}
	t.Cleanup(func() { runForgeInstaller = oldRunner })

	resp, err := srv.InstallForgeServer(ctx, &workerpb.InstallForgeServerRequest{
		InstanceUuid:        uuid,
		ForgeInstallerUrl:   ts.URL + "/installer.jar",
		SpongeforgeUrl:      ts.URL + "/spongeforge.jar",
		SpongeforgeFilename: "SpongeForge.jar",
		LaunchJar:           "forge-1.21.1-52.1.5-server.jar",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	require.Equal(t, "forge-1.21.1-52.1.5-server.jar", resp.LaunchJar)
	require.Equal(t, int64(len(mod)), resp.SpongeforgeSize)

	got, readErr := os.ReadFile(filepath.Join(workDir, "mods", "SpongeForge.jar"))
	require.NoError(t, readErr)
	assert.Equal(t, mod, got)
	_, statErr := os.Stat(filepath.Join(workDir, ".jianmanager", "forge-installer.jar"))
	require.NoError(t, statErr)
}

func TestInstallForgeServer_RejectsUnsafeFilenames(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()
	const uuid = "44444444-4444-4444-4444-444444444444"
	createResp, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "forge", StartCommand: "noop", WorkDir: filepath.Join(tmp, "forge-bad"), ProcessType: "direct",
	})
	require.NoError(t, err)
	require.True(t, createResp.Success, createResp.Error)

	resp, err := srv.InstallForgeServer(ctx, &workerpb.InstallForgeServerRequest{
		InstanceUuid:        uuid,
		ForgeInstallerUrl:   "https://example.invalid/installer.jar",
		SpongeforgeUrl:      "https://example.invalid/spongeforge.jar",
		SpongeforgeFilename: "../SpongeForge.jar",
		LaunchJar:           "forge.jar",
	})
	require.NoError(t, err)
	assert.False(t, resp.Success)
	assert.Contains(t, resp.Error, "非法")
}

func TestDownloadCore_CacheHitSkipsNetwork(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	srv.SetArtifactCache(artifactcache.New(filepath.Join(tmp, "var", "artifact-cache")))
	ctx := context.Background()

	const uuid = "22222222-2222-2222-2222-222222222222"
	workDir := filepath.Join(tmp, "inst2")
	createResp, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid, Name: "stub2", StartCommand: "noop", WorkDir: workDir, ProcessType: "direct",
	})
	require.NoError(t, err)
	require.True(t, createResp.Success, createResp.Error)

	jar := []byte("cache-me-core-jar-payload-abcdefghij")
	sum := sha256.Sum256(jar)
	hexSum := hex.EncodeToString(sum[:])

	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = w.Write(jar)
	}))

	// 第一次：未命中，走网络下载并存入缓存。
	resp1, err := srv.DownloadCore(ctx, &workerpb.DownloadCoreRequest{
		InstanceUuid: uuid, DestFilename: "server.jar", DownloadUrl: ts.URL, Sha256: hexSum,
	})
	require.NoError(t, err)
	require.True(t, resp1.Success, resp1.Error)
	assert.Equal(t, 1, hits, "首次应走网络")

	// 模拟删实例再建：删掉工作目录文件，并关掉下载源（命中则不需要网络）。
	require.NoError(t, os.Remove(filepath.Join(workDir, "server.jar")))
	ts.Close()

	resp2, err := srv.DownloadCore(ctx, &workerpb.DownloadCoreRequest{
		InstanceUuid: uuid, DestFilename: "server.jar", DownloadUrl: ts.URL, Sha256: hexSum,
	})
	require.NoError(t, err)
	require.True(t, resp2.Success, resp2.Error)
	assert.Equal(t, int64(len(jar)), resp2.Size)
	got, readErr := os.ReadFile(filepath.Join(workDir, "server.jar"))
	require.NoError(t, readErr)
	assert.Equal(t, jar, got, "缓存命中应拷出原内容")
}
