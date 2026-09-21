package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// binarySourceArg 的参数解析（FR-441 MCP 暴露）：合法结构逐字段落位，非法结构明确报错
// 而非静默丢弃（静默丢弃会让 binary 请求以「缺少 kind」失败，用户看不出是参数写错）。

func TestBinarySourceArg_AbsentReturnsNil(t *testing.T) {
	// MC 核心路径不关心该参数：缺省必须返回 nil（而不是报错），否则既有调用全被拒。
	src, err := binarySourceArg(map[string]any{"coreType": "paper", "mcVersion": "1.21.1"})
	require.NoError(t, err)
	assert.Nil(t, src)
}

func TestBinarySourceArg_ParsesAllKinds(t *testing.T) {
	src, err := binarySourceArg(map[string]any{
		"binarySource": map[string]any{
			"kind": "url", "url": "https://x.com/b", "sha256": "abc",
			"filename": "beacon", "nodePath": "/opt/b", "assetId": float64(12),
			"executable": false,
		},
	})
	require.NoError(t, err)
	require.NotNil(t, src)
	assert.Equal(t, service.BinarySourceKind("url"), src.Kind)
	assert.Equal(t, "https://x.com/b", src.URL)
	assert.Equal(t, "abc", src.SHA256)
	assert.Equal(t, "beacon", src.Filename)
	assert.Equal(t, "/opt/b", src.NodePath)
	assert.Equal(t, uint(12), src.AssetID)
	require.NotNil(t, src.Executable)
	assert.False(t, *src.Executable)
	assert.False(t, src.ExecutableOrDefault())
}

func TestBinarySourceArg_DefaultsExecutableTrue(t *testing.T) {
	// 未显式指定时可执行位默认开：二进制不置可执行位等于搭建出来也跑不起来。
	src, err := binarySourceArg(map[string]any{
		"binarySource": map[string]any{"kind": "url", "url": "https://x.com/b", "filename": "b"},
	})
	require.NoError(t, err)
	require.NotNil(t, src)
	assert.Nil(t, src.Executable)
	assert.True(t, src.ExecutableOrDefault())
}

func TestBinarySourceArg_RejectsMalformed(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"非对象", map[string]any{"binarySource": "url"}},
		{"assetId 非数字", map[string]any{"binarySource": map[string]any{"kind": "asset", "assetId": "abc"}}},
		{"executable 非布尔", map[string]any{"binarySource": map[string]any{"kind": "url", "executable": "yes"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := binarySourceArg(tc.args)
			assert.Error(t, err, "非法参数应明确报错，不静默丢弃")
		})
	}
}

// TestToolSpec_ProvisionServerExposesBinarySource 工具协议必须暴露 binary 参数与说明，
// 否则 Agent 无从发现该能力（schema 是 MCP 侧唯一的能力发现面）。
func TestToolSpec_ProvisionServerExposesBinarySource(t *testing.T) {
	var spec *ToolDef
	for i := range allToolSpecs {
		if allToolSpecs[i].Def.Name == "instance_provision_server" {
			spec = &allToolSpecs[i].Def
			break
		}
	}
	require.NotNil(t, spec, "instance_provision_server 工具应存在")

	props, ok := spec.InputSchema["properties"].(map[string]any)
	require.True(t, ok)
	assert.Contains(t, props, "binarySource")
	assert.Contains(t, props, "startCommand")
	assert.Contains(t, spec.Description, "binary")
	assert.Contains(t, spec.Description, "binarySource")

	// mcVersion 不应再是必填：binary 不需要它，硬性必填会把二进制请求挡在参数校验外。
	required, ok := spec.InputSchema["required"].([]string)
	require.True(t, ok)
	assert.NotContains(t, required, "mcVersion")
	assert.Contains(t, required, "coreType")

	// 嵌套 schema 同样要能被 Agent 读到（三类来源的字段与用途）。
	bs, ok := props["binarySource"].(map[string]any)
	require.True(t, ok)
	bsProps, ok := bs["properties"].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"kind", "assetId", "url", "sha256", "nodePath", "filename", "executable"} {
		assert.Contains(t, bsProps, key, "binarySource 应暴露 %s", key)
	}
}
