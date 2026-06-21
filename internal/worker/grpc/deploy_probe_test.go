package grpc

import (
	"context"
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
