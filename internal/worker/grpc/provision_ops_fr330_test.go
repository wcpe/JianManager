package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/artifactcache"
	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// newFR330Server 建一个带制品缓存的 worker gRPC server 测试基座，并注册一个实例。
func newFR330Server(t *testing.T, instanceUUID, workDirName string) (*Server, string) {
	t.Helper()
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	srv.SetArtifactCache(artifactcache.New(filepath.Join(tmp, "var", "artifact-cache")))
	workDir := filepath.Join(tmp, workDirName)
	resp, err := srv.CreateInstance(context.Background(), &workerpb.CreateInstanceRequest{
		InstanceUuid: instanceUUID, Name: workDirName, StartCommand: "noop", WorkDir: workDir, ProcessType: "direct",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	return srv, workDir
}

// TestDownloadCore_CompositeKeyHitSkipsNetwork 组合键缓存（FR-330）：sha256 为空的下载源
// （Sponge Maven 等）按 core|version|build 组合键缓存。首次下载入缓存；删工作目录文件、
// 关掉下载源后再次请求同组合键，应命中缓存秒拷、完全不走网络，且响应带 cache_hit。
func TestDownloadCore_CompositeKeyHitSkipsNetwork(t *testing.T) {
	const uuid = "77777777-7777-7777-7777-777777777777"
	srv, workDir := newFR330Server(t, uuid, "inst-ck")
	ctx := context.Background()

	jar := []byte("spongevanilla-core-jar-without-sha256")
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(jar)
	}))

	req := &workerpb.DownloadCoreRequest{
		InstanceUuid: uuid, DestFilename: "server.jar", DownloadUrl: ts.URL,
		CoreType: "spongevanilla", McVersion: "1.21.1", Build: 2665,
	}
	resp1, err := srv.DownloadCore(ctx, req)
	require.NoError(t, err)
	require.True(t, resp1.Success, resp1.Error)
	assert.False(t, resp1.CacheHit, "首次应走网络下载")
	assert.Equal(t, int32(1), hits.Load())

	// 模拟删实例再建：删工作目录文件 + 关下载源（命中则无需网络）。
	require.NoError(t, os.Remove(filepath.Join(workDir, "server.jar")))
	ts.Close()

	resp2, err := srv.DownloadCore(ctx, req)
	require.NoError(t, err)
	require.True(t, resp2.Success, resp2.Error)
	assert.True(t, resp2.CacheHit, "组合键第二次应缓存命中")
	assert.Equal(t, int64(len(jar)), resp2.Size)
	got, readErr := os.ReadFile(filepath.Join(workDir, "server.jar"))
	require.NoError(t, readErr)
	assert.Equal(t, jar, got, "缓存命中应拷出原内容")
}

// TestDownloadCore_IndeterminateKeyNotReusable 组合键成分不全（如 BungeeCord latest 无构建号）
// 不得参与组合键缓存：第二次请求仍走网络（latest 语义不可冻结）。
func TestDownloadCore_IndeterminateKeyNotReusable(t *testing.T) {
	const uuid = "78787878-7878-7878-7878-787878787878"
	srv, workDir := newFR330Server(t, uuid, "inst-latest")
	ctx := context.Background()

	jar := []byte("bungeecord-latest-jar")
	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(jar)
	}))
	defer ts.Close()

	req := &workerpb.DownloadCoreRequest{
		InstanceUuid: uuid, DestFilename: "server.jar", DownloadUrl: ts.URL,
		CoreType: "bungeecord", McVersion: "latest", Build: 0,
	}
	resp1, err := srv.DownloadCore(ctx, req)
	require.NoError(t, err)
	require.True(t, resp1.Success, resp1.Error)

	require.NoError(t, os.Remove(filepath.Join(workDir, "server.jar")))
	resp2, err := srv.DownloadCore(ctx, req)
	require.NoError(t, err)
	require.True(t, resp2.Success, resp2.Error)
	assert.False(t, resp2.CacheHit, "latest/无构建号不得组合键命中")
	assert.Equal(t, int32(2), hits.Load(), "不可冻结的 latest 必须每次重新下载")
}

// TestDownloadCore_CorruptCacheEntryRedownloads 命中校验回退（FR-330）：缓存 blob 损坏时
// 不得把脏内容交付给实例——作废条目、回退远程下载，并用新下载内容重建缓存。
func TestDownloadCore_CorruptCacheEntryRedownloads(t *testing.T) {
	const uuid = "79797979-7979-7979-7979-797979797979"
	srv, workDir := newFR330Server(t, uuid, "inst-corrupt")
	ctx := context.Background()

	jar := []byte("paper-core-jar-payload-for-corruption-test")
	sum := sha256.Sum256(jar)
	hexSum := hex.EncodeToString(sum[:])

	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(jar)
	}))

	req := &workerpb.DownloadCoreRequest{
		InstanceUuid: uuid, DestFilename: "server.jar", DownloadUrl: ts.URL, Sha256: hexSum,
		CoreType: "paper", McVersion: "1.21.8", Build: 263,
	}
	resp1, err := srv.DownloadCore(ctx, req)
	require.NoError(t, err)
	require.True(t, resp1.Success, resp1.Error)
	require.Equal(t, int32(1), hits.Load())

	// 篡改缓存 blob（布局 <root>/<sha[:2]>/<sha>，见 artifactcache 包注释）。
	blob := filepath.Join(filepath.Dir(workDir), "var", "artifact-cache", hexSum[:2], hexSum)
	require.FileExists(t, blob)
	require.NoError(t, os.WriteFile(blob, []byte("bit-rot"), 0o644))
	require.NoError(t, os.Remove(filepath.Join(workDir, "server.jar")))

	// 损坏条目：作废 + 回退网络重下，交付内容必须正确。
	resp2, err := srv.DownloadCore(ctx, req)
	require.NoError(t, err)
	require.True(t, resp2.Success, resp2.Error)
	assert.False(t, resp2.CacheHit, "损坏条目不算命中")
	assert.Equal(t, int32(2), hits.Load(), "损坏条目应回退远程下载")
	got, readErr := os.ReadFile(filepath.Join(workDir, "server.jar"))
	require.NoError(t, readErr)
	assert.Equal(t, jar, got)

	// 重下后缓存已重建：关源再来一次应命中。
	require.NoError(t, os.Remove(filepath.Join(workDir, "server.jar")))
	ts.Close()
	resp3, err := srv.DownloadCore(ctx, req)
	require.NoError(t, err)
	require.True(t, resp3.Success, resp3.Error)
	assert.True(t, resp3.CacheHit, "重下后缓存应已重建")
}

// TestDownloadCore_ConcurrentSameCoreSingleflight 并发单飞（FR-330）：多个实例并发搭建同一
// 核心（同缓存键）时只允许一次远程下载，其余等待领队完成后从缓存取，互不踩踏。
func TestDownloadCore_ConcurrentSameCoreSingleflight(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	srv.SetArtifactCache(artifactcache.New(filepath.Join(tmp, "var", "artifact-cache")))
	ctx := context.Background()

	jar := []byte("shared-core-jar-for-concurrent-provision-1234567890")
	sum := sha256.Sum256(jar)
	hexSum := hex.EncodeToString(sum[:])

	var hits atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		once.Do(func() { close(started) })
		<-release // 卡住领队下载，给其余并发请求汇入单飞的窗口
		_, _ = w.Write(jar)
	}))
	defer ts.Close()

	const n = 4
	uuids := make([]string, n)
	workDirs := make([]string, n)
	for i := 0; i < n; i++ {
		uuids[i] = fmt.Sprintf("88888888-8888-8888-8888-88888888888%d", i)
		workDirs[i] = filepath.Join(tmp, fmt.Sprintf("inst-conc-%d", i))
		resp, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
			InstanceUuid: uuids[i], Name: fmt.Sprintf("conc-%d", i), StartCommand: "noop",
			WorkDir: workDirs[i], ProcessType: "direct",
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error)
	}

	results := make([]*workerpb.DownloadCoreResponse, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, _ := srv.DownloadCore(ctx, &workerpb.DownloadCoreRequest{
				InstanceUuid: uuids[i], DestFilename: "server.jar", DownloadUrl: ts.URL, Sha256: hexSum,
				CoreType: "paper", McVersion: "1.21.8", Build: 263,
			})
			results[i] = r
		}(i)
	}

	<-started // 领队已开始网络下载
	close(release)
	wg.Wait()

	assert.Equal(t, int32(1), hits.Load(), "同核心并发搭建只允许一次远程下载")
	hitCount := 0
	for i := 0; i < n; i++ {
		require.NotNil(t, results[i])
		require.True(t, results[i].Success, results[i].Error)
		got, readErr := os.ReadFile(filepath.Join(workDirs[i], "server.jar"))
		require.NoError(t, readErr)
		assert.Equal(t, jar, got, "实例 %d 内容不完整", i)
		if results[i].CacheHit {
			hitCount++
		}
	}
	assert.Equal(t, n-1, hitCount, "除领队外其余应从缓存取（cache_hit）")
}
