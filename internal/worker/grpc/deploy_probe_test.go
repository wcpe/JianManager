package grpc

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/process"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// TestDeployServerProbe 验证探针自动部署：jar 与 config.yml 落到实例 plugins 目录（FR-010）。
func TestDeployServerProbe(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()

	const uuid = "22222222-2222-2222-2222-222222222222"
	workDir := filepath.Join(tmp, "inst")
	createResp, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "probe",
		StartCommand: "noop",
		WorkDir:      workDir,
		ProcessType:  "direct",
	})
	require.NoError(t, err)
	require.True(t, createResp.Success, createResp.Error)

	jar := []byte("fake-serverprobe-jar")
	cfg := "metrics:\n  enabled: true\n  port: 29940\n"
	libs := makeProbeLibrariesZip(t, map[string]string{
		"libraries/io/izzel/taboolib/common-env/6.3.0/common-env-6.3.0.jar":      "jar-bytes",
		"libraries/io/izzel/taboolib/common-env/6.3.0/common-env-6.3.0.jar.sha1": "sha1-bytes",
		"assets/4c/4cf1c21c1b0e91556f12f846accc3f69826fb682":                     "mapping-bytes",
	})

	t.Run("jar 与 config 同时落地", func(t *testing.T) {
		resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
			InstanceUuid: uuid,
			Jar:          jar,
			ConfigYaml:   cfg,
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error)

		gotJar, e1 := os.ReadFile(filepath.Join(workDir, "plugins", "ServerProbe.jar"))
		require.NoError(t, e1)
		assert.Equal(t, jar, gotJar)

		gotCfg, e2 := os.ReadFile(filepath.Join(workDir, "plugins", "ServerProbe", "config.yml"))
		require.NoError(t, e2)
		assert.Equal(t, cfg, string(gotCfg))
	})

	t.Run("依赖缓存落地到实例根 libraries", func(t *testing.T) {
		resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
			InstanceUuid: uuid,
			Jar:          jar,
			ConfigYaml:   cfg,
			LibrariesZip: libs,
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error)

		gotLib, e1 := os.ReadFile(filepath.Join(workDir, "libraries", "io", "izzel", "taboolib", "common-env", "6.3.0", "common-env-6.3.0.jar"))
		require.NoError(t, e1)
		assert.Equal(t, "jar-bytes", string(gotLib))

		gotSha1, e2 := os.ReadFile(filepath.Join(workDir, "libraries", "io", "izzel", "taboolib", "common-env", "6.3.0", "common-env-6.3.0.jar.sha1"))
		require.NoError(t, e2)
		assert.Equal(t, "sha1-bytes", string(gotSha1))

		gotAsset, e3 := os.ReadFile(filepath.Join(workDir, "assets", "4c", "4cf1c21c1b0e91556f12f846accc3f69826fb682"))
		require.NoError(t, e3)
		assert.Equal(t, "mapping-bytes", string(gotAsset))
	})

	t.Run("依赖缓存拒绝恶意路径", func(t *testing.T) {
		cases := map[string]string{
			"路径穿越":  "libraries/../evil.txt",
			"绝对路径":  "/abs.txt",
			"反斜杠穿越": "libraries/a\\..\\b.txt",
		}
		for name, zipName := range cases {
			t.Run(name, func(t *testing.T) {
				badZip := makeProbeLibrariesZip(t, map[string]string{zipName: "bad"})
				resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
					InstanceUuid: uuid,
					LibrariesZip: badZip,
				})
				require.NoError(t, err)
				assert.False(t, resp.Success)
			})
		}
		assert.NoFileExists(t, filepath.Join(workDir, "evil.txt"))
	})

	t.Run("依赖缓存拒绝超大条目", func(t *testing.T) {
		badZip := makeProbeZipWithDeclaredSize(t, "libraries/big.jar", uint32(probeMaxZipEntryBytes+1))
		resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
			InstanceUuid: uuid,
			LibrariesZip: badZip,
		})
		require.NoError(t, err)
		assert.False(t, resp.Success)
		assert.NoFileExists(t, filepath.Join(workDir, "libraries", "big.jar"))
	})

	t.Run("空 jar 仅写 config", func(t *testing.T) {
		resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
			InstanceUuid: uuid,
			ConfigYaml:   cfg,
		})
		require.NoError(t, err)
		require.True(t, resp.Success, resp.Error)
	})

	t.Run("未注册实例失败", func(t *testing.T) {
		resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
			InstanceUuid: "no-such",
			Jar:          jar,
		})
		require.NoError(t, err)
		assert.False(t, resp.Success)
	})
}

// TestDeployServerProbe_DownloadsJarFromControlPlane 验证新版路径只让 Worker 从 CP 拉 jar：
// 不预置运行库，校验成功后原子替换，校验失败保留旧 jar。
func TestDeployServerProbe_DownloadsJarFromControlPlane(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()
	const uuid = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	workDir := filepath.Join(tmp, "inst")
	requireCreatedProbeInstance(t, srv, ctx, uuid, workDir)

	jar := []byte("serverprobe-from-control-plane")
	jarSum := sha256.Sum256(jar)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jar)
	}))
	defer server.Close()

	oldJarPath := filepath.Join(workDir, "plugins", serverProbeJarName)
	require.NoError(t, os.MkdirAll(filepath.Dir(oldJarPath), 0o755))
	require.NoError(t, os.WriteFile(oldJarPath, []byte("old-probe"), 0o644))

	resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
		InstanceUuid: uuid,
		DownloadUrl:  server.URL + "/probe-artifacts/7/download?token=short-lived",
		Sha256:       hex.EncodeToString(jarSum[:]),
		Version:      "0.2.0",
		ConfigYaml:   "metrics:\n  enabled: true\n",
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)
	got, err := os.ReadFile(oldJarPath)
	require.NoError(t, err)
	require.Equal(t, jar, got)
	assert.NoDirExists(t, filepath.Join(workDir, "libraries"), "新版下发不应预置 TabooLib 运行库")

	resp, err = srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
		InstanceUuid: uuid,
		DownloadUrl:  server.URL + "/probe-artifacts/7/download?token=short-lived",
		Sha256:       strings.Repeat("0", 64),
		Version:      "0.2.0",
	})
	require.NoError(t, err)
	require.False(t, resp.Success)
	got, err = os.ReadFile(oldJarPath)
	require.NoError(t, err)
	require.Equal(t, jar, got, "校验失败不得覆盖已部署的 jar")
}

func makeProbeLibrariesZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// TestDeployServerProbe_PrefetchesTabooLibLibraries 验证部署探针时会按 jar 元数据把 TabooLib 运行期依赖预置到实例 libraries 目录。
func TestDeployServerProbe_PrefetchesTabooLibLibraries(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()
	const uuid = "33333333-3333-3333-3333-333333333333"
	workDir := filepath.Join(tmp, "inst")
	requireCreatedProbeInstance(t, srv, ctx, uuid, workDir)

	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".jar"):
			_, _ = w.Write([]byte("taboolib-module-jar"))
		case strings.HasSuffix(r.URL.Path, ".pom"):
			_, _ = w.Write([]byte("<project/>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer repo.Close()

	resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
		InstanceUuid: uuid,
		Jar:          makeProbeJar(t, repo.URL, "libraries", "basic-configuration"),
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)

	cachedJar := filepath.Join(workDir, "libraries", "io", "izzel", "taboolib", "basic-configuration", "6.3.0-test", "basic-configuration-6.3.0-test.jar")
	got, err := os.ReadFile(cachedJar)
	require.NoError(t, err)
	assert.Equal(t, "taboolib-module-jar", string(got))
}

func TestDeployServerProbe_UsesCachedTabooLibLibrariesWhenRepoOffline(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()
	const uuid = "55555555-5555-5555-5555-555555555555"
	workDir := filepath.Join(tmp, "inst")
	requireCreatedProbeInstance(t, srv, ctx, uuid, workDir)

	var jarHits int
	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".jar"):
			jarHits++
			_, _ = w.Write([]byte("cached-taboolib-module"))
		case strings.HasSuffix(r.URL.Path, ".pom"):
			_, _ = w.Write([]byte("<project/>"))
		default:
			http.NotFound(w, r)
		}
	}))
	jar := makeProbeJar(t, repo.URL, "libraries", "basic-configuration")

	first, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{InstanceUuid: uuid, Jar: jar})
	require.NoError(t, err)
	require.True(t, first.Success, first.Error)
	repo.Close()

	second, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{InstanceUuid: uuid, Jar: jar})
	require.NoError(t, err)
	require.True(t, second.Success, second.Error)
	require.Equal(t, 1, jarHits, "已预置的探针依赖不应在离线重部署时重复访问远端仓库")
}

// TestDeployServerProbe_LibrariesZipSatisfiesDependenciesOffline 钉死离线依赖 zip 先于依赖预置落盘：
// 远端仓库完全不可达（真机：CN 主机 repo.tabooproject.org 连接被重置），但请求随身携带的离线依赖
// zip 已包含全部所需 pom/jar——部署必须成功且零联网。修复前顺序相反（先预置后解压 zip），
// 离线缓存在手却先去外网下 pom 而整体失败。
func TestDeployServerProbe_LibrariesZipSatisfiesDependenciesOffline(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()
	const uuid = "66666666-6666-6666-6666-666666666666"
	workDir := filepath.Join(tmp, "inst")
	requireCreatedProbeInstance(t, srv, ctx, uuid, workDir)

	// 仓库对任何请求都 404（等效外网不可达）。
	repo := httptest.NewServer(http.NotFoundHandler())
	defer repo.Close()

	depDir := "libraries/io/izzel/taboolib/basic-configuration/6.3.0-test/"
	zipData := makeProbeLibrariesZip(t, map[string]string{
		depDir + "basic-configuration-6.3.0-test.pom": "<project/>",
		depDir + "basic-configuration-6.3.0-test.jar": "offline-zip-module",
	})

	resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
		InstanceUuid: uuid,
		Jar:          makeProbeJar(t, repo.URL, "libraries", "basic-configuration"),
		LibrariesZip: zipData,
	})
	require.NoError(t, err)
	require.True(t, resp.Success, resp.Error)

	got, err := os.ReadFile(filepath.Join(workDir, "libraries", "io", "izzel", "taboolib", "basic-configuration", "6.3.0-test", "basic-configuration-6.3.0-test.jar"))
	require.NoError(t, err)
	assert.Equal(t, "offline-zip-module", string(got), "依赖应来自离线 zip 而非远端仓库")
}

// TestDeployServerProbe_DependencyPrefetchDiagnostics 覆盖缺依赖诊断和缓存目录越界保护。
func TestDeployServerProbe_DependencyPrefetchDiagnostics(t *testing.T) {
	tmp := t.TempDir()
	srv := NewServer(process.NewManager(tmp), "test-node", nil, nil, nil)
	ctx := context.Background()
	const uuid = "44444444-4444-4444-4444-444444444444"
	workDir := filepath.Join(tmp, "inst")
	requireCreatedProbeInstance(t, srv, ctx, uuid, workDir)

	repo := httptest.NewServer(http.NotFoundHandler())
	defer repo.Close()

	t.Run("下载失败返回明确诊断", func(t *testing.T) {
		resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
			InstanceUuid: uuid,
			Jar:          makeProbeJar(t, repo.URL, "libraries", "basic-configuration"),
		})
		require.NoError(t, err)
		assert.False(t, resp.Success)
		assert.Contains(t, resp.Error, "预置探针依赖失败")
		assert.Contains(t, resp.Error, "io.izzel.taboolib:basic-configuration:6.3.0-test")
	})

	t.Run("拒绝越界缓存目录", func(t *testing.T) {
		resp, err := srv.DeployServerProbe(ctx, &workerpb.DeployServerProbeRequest{
			InstanceUuid: uuid,
			Jar:          makeProbeJar(t, repo.URL, "../outside", "basic-configuration"),
		})
		require.NoError(t, err)
		assert.False(t, resp.Success)
		assert.Contains(t, resp.Error, "探针依赖缓存目录越界")
		assert.NoDirExists(t, filepath.Join(tmp, "outside"))
	})
}

func requireCreatedProbeInstance(t *testing.T, srv *Server, ctx context.Context, uuid string, workDir string) {
	t.Helper()
	createResp, err := srv.CreateInstance(ctx, &workerpb.CreateInstanceRequest{
		InstanceUuid: uuid,
		Name:         "probe",
		StartCommand: "noop",
		WorkDir:      workDir,
		ProcessType:  "direct",
	})
	require.NoError(t, err)
	require.True(t, createResp.Success, createResp.Error)
}

func makeProbeJar(t *testing.T, repoURL string, fileLibs string, modules string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	writeZipEntry(t, zw, "META-INF/taboolib/env.properties", fmt.Sprintf(`
repo-central=%s
repo-taboolib=%s
file-libs=%s
module=%s
`, repoURL, repoURL, fileLibs, modules))
	writeZipEntry(t, zw, "META-INF/taboolib/version.properties", `
kotlin=null
kotlin-coroutines=null
taboolib=6.3.0-test
`)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func makeProbeZipWithDeclaredSize(t *testing.T, name string, size uint32) []byte {
	t.Helper()
	b := makeProbeLibrariesZip(t, map[string]string{name: "small"})
	idx := bytes.Index(b, []byte{'P', 'K', 0x01, 0x02})
	require.NotEqual(t, -1, idx, "zip central directory not found")
	binary.LittleEndian.PutUint32(b[idx+24:idx+28], size)
	return b
}

func writeZipEntry(t *testing.T, zw *zip.Writer, name string, body string) {
	t.Helper()
	w, err := zw.Create(name)
	require.NoError(t, err)
	_, err = w.Write([]byte(body))
	require.NoError(t, err)
}
