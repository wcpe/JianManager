package grpc

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
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

func makeProbeZipWithDeclaredSize(t *testing.T, name string, size uint32) []byte {
	t.Helper()
	b := makeProbeLibrariesZip(t, map[string]string{name: "small"})
	idx := bytes.Index(b, []byte{'P', 'K', 0x01, 0x02})
	require.NotEqual(t, -1, idx, "zip central directory not found")
	binary.LittleEndian.PutUint32(b[idx+24:idx+28], size)
	return b
}
