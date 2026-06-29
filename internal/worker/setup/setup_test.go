package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/register"
)

// TestCollectInputs_NonTTY_FlagsBeatEnv 无 TTY 形态：命令行参数优先于环境变量，端口/默认值正确。
func TestCollectInputs_NonTTY_FlagsBeatEnv(t *testing.T) {
	t.Setenv("JIANMANAGER_CONTROL_PLANE", "env-cp:9100")
	t.Setenv("JIANMANAGER_ENROLL_TOKEN", "jmet_env")
	t.Setenv("JIANMANAGER_NODE_NAME", "env-node")

	in, err := CollectInputs(Options{
		IsTTY: false,
		Args:  []string{"--control-plane", "flag-cp:9100", "--token", "jmet_flag", "--grpc-port", "19101"},
	})
	require.NoError(t, err)
	assert.Equal(t, "flag-cp:9100", in.ControlPlane, "flag 覆盖 env")
	assert.Equal(t, "jmet_flag", in.EnrollToken, "flag 覆盖 env")
	assert.Equal(t, "env-node", in.NodeName, "未给 flag 时回退 env")
	assert.Equal(t, 19101, in.GRPCPort)
	assert.Equal(t, defaultWSPort, in.WSPort, "未给则用默认 ws 端口")
}

// TestCollectInputs_NonTTY_EnvOnly 无 TTY 形态：仅环境变量提供必填项也可成功。
func TestCollectInputs_NonTTY_EnvOnly(t *testing.T) {
	t.Setenv("JIANMANAGER_CONTROL_PLANE_GRPC", "cp.example:9100") // 历史别名
	t.Setenv("JIANMANAGER_ENROLL_TOKEN", "jmet_abc")

	in, err := CollectInputs(Options{IsTTY: false, Args: nil})
	require.NoError(t, err)
	assert.Equal(t, "cp.example:9100", in.ControlPlane)
	assert.Equal(t, "jmet_abc", in.EnrollToken)
	assert.Equal(t, defaultGRPCPort, in.GRPCPort)
	assert.Equal(t, defaultWSPort, in.WSPort)
}

// TestCollectInputs_NonTTY_MissingTokenFails 无 TTY 缺 token → 明确报错（不卡住等输入）。
func TestCollectInputs_NonTTY_MissingTokenFails(t *testing.T) {
	// 显式清空可能从外部继承的 token 环境变量。
	t.Setenv("JIANMANAGER_ENROLL_TOKEN", "")
	_, err := CollectInputs(Options{
		IsTTY: false,
		Args:  []string{"--control-plane", "cp:9100"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token")
}

// TestCollectInputs_NonTTY_CPDefaults 无 TTY 缺 CP 地址时回退默认 localhost:9100（仅 token 必填）。
func TestCollectInputs_NonTTY_CPDefaults(t *testing.T) {
	t.Setenv("JIANMANAGER_CONTROL_PLANE", "")
	t.Setenv("JIANMANAGER_CONTROL_PLANE_GRPC", "")
	in, err := CollectInputs(Options{IsTTY: false, Args: []string{"--token", "jmet_x"}})
	require.NoError(t, err)
	assert.Equal(t, defaultControlPlane, in.ControlPlane)
}

// TestCollectInputs_NonTTY_BadPortFails 端口非法 → 报错。
func TestCollectInputs_NonTTY_BadPortFails(t *testing.T) {
	t.Setenv("JIANMANAGER_ENROLL_TOKEN", "")
	_, err := CollectInputs(Options{
		IsTTY: false,
		Args:  []string{"--token", "jmet_x", "--grpc-port", "abc"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "grpc-port")
}

// TestParseFlags_EqualsForm --key=value 形态解析正确，未知 flag 忽略。
func TestParseFlags_EqualsForm(t *testing.T) {
	f := parseFlags([]string{"--control-plane=cp:9100", "--unknown=x", "--token", "jmet_y", "positional"})
	assert.Equal(t, "cp:9100", f["control-plane"])
	assert.Equal(t, "jmet_y", f["token"])
	_, hasUnknown := f["unknown"]
	assert.False(t, hasUnknown, "未知 flag 不解析")
}

// TestCollectInputs_TTY_Interactive 交互式形态：逐项输入被正确采集（驱动 reader，不需真 TTY）。
func TestCollectInputs_TTY_Interactive(t *testing.T) {
	// 顺序：CP / token / name / grpc / ws / data_dir。空行接受默认。
	input := strings.Join([]string{
		"cp-host:9100", // CP
		"jmet_interactive",
		"my-node",
		"",     // grpc 端口默认
		"",     // ws 端口默认
		"",     // data_dir 默认（空）
		"",     // 兜底
	}, "\n")
	var out bytes.Buffer
	in, err := CollectInputs(Options{
		IsTTY: true,
		In:    strings.NewReader(input),
		Out:   &out,
	})
	require.NoError(t, err)
	assert.Equal(t, "cp-host:9100", in.ControlPlane)
	assert.Equal(t, "jmet_interactive", in.EnrollToken)
	assert.Equal(t, "my-node", in.NodeName)
	assert.Equal(t, defaultGRPCPort, in.GRPCPort)
	assert.Equal(t, defaultWSPort, in.WSPort)
	assert.Empty(t, in.DataDir)
}

// TestCollectInputs_TTY_EOFOnToken 交互式 token 处 EOF（输入流断）→ 报错，不死循环。
func TestCollectInputs_TTY_EOFOnToken(t *testing.T) {
	// 给了 CP（默认回车），随后 EOF，token 采集应失败。
	var out bytes.Buffer
	_, err := CollectInputs(Options{
		IsTTY: true,
		In:    strings.NewReader("cp:9100\n"), // CP 一行后即 EOF
		Out:   &out,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EOF")
}

// TestRun_WritesYMLRegistersPersists Run 端到端（mock 注册）：写 yml、注册、持久化身份、返回内存配置。
func TestRun_WritesYMLRegistersPersists(t *testing.T) {
	t.Setenv("JIANMANAGER_ENROLL_TOKEN", "") // 清外部继承
	workDir := t.TempDir()
	dataDir := filepath.Join(workDir, "data")

	var capturedCfg register.Config
	mockReg := func(ctx context.Context, cfg register.Config) (*register.Result, error) {
		capturedCfg = cfg
		return &register.Result{NodeUUID: "uuid-123", NodeSecret: "secret-xyz"}, nil
	}

	res, err := Run(context.Background(), workDir, Options{
		IsTTY: false,
		Args: []string{
			"--control-plane", "cp:9100",
			"--token", "jmet_run",
			"--name", "edge-7",
			"--data-dir", dataDir,
		},
		Registrar: mockReg,
		Out:       &bytes.Buffer{},
	})
	require.NoError(t, err)
	require.NotNil(t, res)

	// 注册以 enrollment token 携带、节点名/端口正确。
	assert.Equal(t, "jmet_run", capturedCfg.EnrollToken)
	assert.Equal(t, "edge-7", capturedCfg.NodeName)
	assert.Equal(t, defaultGRPCPort, capturedCfg.GrpcPort)

	// worker.yml 写出且 token 不在其中。
	ymlPath := filepath.Join(workDir, "worker.yml")
	raw, err := os.ReadFile(ymlPath)
	require.NoError(t, err)
	text := string(raw)
	assert.Contains(t, text, "control_plane:")
	assert.Contains(t, text, "edge-7")
	assert.NotContains(t, text, "jmet_run", "enrollment token 绝不写入 worker.yml")
	assert.NotContains(t, text, "secret-xyz", "node_secret 不写入 worker.yml")

	// 身份持久化到 <data-dir>/etc/node-identity.json。
	idPath := filepath.Join(dataDir, "etc", "node-identity.json")
	loaded, err := register.LoadIdentity(filepath.Join(dataDir, "etc"))
	require.NoError(t, err)
	require.NotNil(t, loaded)
	assert.Equal(t, "uuid-123", loaded.NodeUUID)
	assert.Equal(t, "secret-xyz", loaded.NodeSecret)
	assert.Equal(t, "edge-7", loaded.NodeName)

	// 身份文件 0600（POSIX 才校验权限位）。
	if runtime.GOOS != "windows" {
		st, statErr := os.Stat(idPath)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	}

	// 返回内存配置可用于转 run。
	require.NotNil(t, res.Config)
	assert.Equal(t, "cp:9100", res.Config.ControlPlane)
	assert.Equal(t, "edge-7", res.Config.Name)
	require.NotNil(t, res.Identity)
	assert.Equal(t, "uuid-123", res.Identity.NodeUUID)
}

// TestRun_RegistrationFailureAborts 注册失败 → Run 报错（不留半截 run）。
func TestRun_RegistrationFailureAborts(t *testing.T) {
	t.Setenv("JIANMANAGER_ENROLL_TOKEN", "")
	workDir := t.TempDir()
	failReg := func(ctx context.Context, cfg register.Config) (*register.Result, error) {
		return nil, errors.New("CP 拒绝：token 已消费")
	}
	_, err := Run(context.Background(), workDir, Options{
		IsTTY:     false,
		Args:      []string{"--control-plane", "cp:9100", "--token", "jmet_bad", "--data-dir", filepath.Join(workDir, "data")},
		Registrar: failReg,
		Out:       &bytes.Buffer{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "注册")
}

// TestRun_RefusesOverwriteExistingYML 已存在 worker.yml 时 Run 拒绝覆盖（防御自检竞态）。
func TestRun_RefusesOverwriteExistingYML(t *testing.T) {
	t.Setenv("JIANMANAGER_ENROLL_TOKEN", "")
	workDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "worker.yml"), []byte("name: pre\n"), 0o644))

	called := false
	reg := func(ctx context.Context, cfg register.Config) (*register.Result, error) {
		called = true
		return &register.Result{NodeUUID: "u", NodeSecret: "s"}, nil
	}
	_, err := Run(context.Background(), workDir, Options{
		IsTTY:     false,
		Args:      []string{"--control-plane", "cp:9100", "--token", "jmet_x"},
		Registrar: reg,
		Out:       &bytes.Buffer{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "已存在")
	assert.False(t, called, "拒绝覆盖时不应发起注册")
}
