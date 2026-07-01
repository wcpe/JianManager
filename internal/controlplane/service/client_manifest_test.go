package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// sampleSignedManifest 构造一份固定样例 manifest（contract §2），供结构断言复用。
func sampleSignedManifest() *SignedManifest {
	return &SignedManifest{
		SchemaVersion: 1,
		Channel:       "skyblock-s1",
		Version:       42,
		IssuedAt:      "2026-06-23T10:00:00Z",
		ManagedDirs:   []string{"mods", "config"},
		Files: []ManifestFile{
			{
				Path: "mods/foo.jar", SHA256: "ab12", MD5: "cd34", Size: 123456,
				Sync: "strict", Platform: "",
				Artifact: ManifestArtifact{SHA256: "ef56", Size: 45678, Codec: "zstd"},
			},
			{
				Path: "config/opt.txt", SHA256: "9988", MD5: "7766", Size: 12,
				Sync: "once", Platform: "windows",
				Artifact: ManifestArtifact{SHA256: "aa00", Size: 20, Codec: "none"},
			},
		},
		Agent: &ManifestAgent{
			Wedge: &ManifestWedge{Version: 3},
			Core: &ManifestCore{
				Version: 5,
				Platforms: map[string]ManifestAgentArtifact{
					"windows": {SHA256: "c1", Size: 100, Codec: "zstd"},
				},
			},
		},
	}
}

// TestSignedManifest_JSONStructureMatchesContract 断言序列化 JSON 含 contract §2 全部字段与结构，
// 可被客户端 Manifest.parse 解析（字段名/嵌套/类型对齐）。FR-256 起 manifest 不再含 sig 段。
func TestSignedManifest_JSONStructureMatchesContract(t *testing.T) {
	m := sampleSignedManifest()

	raw, err := json.Marshal(m)
	require.NoError(t, err)

	var obj map[string]any
	require.NoError(t, json.Unmarshal(raw, &obj))

	// 顶层字段（Manifest.parse 读取的键）。
	require.EqualValues(t, 1, obj["schemaVersion"])
	require.Equal(t, "skyblock-s1", obj["channel"])
	require.EqualValues(t, 42, obj["version"])
	require.Contains(t, obj, "issuedAt")
	require.ElementsMatch(t, []any{"mods", "config"}, obj["managedDirs"])

	files := obj["files"].([]any)
	require.Len(t, files, 2)
	f0 := files[0].(map[string]any)
	for _, k := range []string{"path", "sha256", "md5", "size", "sync", "platform", "artifact"} {
		require.Contains(t, f0, k, "files[] 须含契约字段 %s", k)
	}
	require.Nil(t, f0["platform"], "全平台文件 platform 须为 null")
	art := f0["artifact"].(map[string]any)
	for _, k := range []string{"sha256", "size", "codec"} {
		require.Contains(t, art, k, "artifact 须含契约字段 %s", k)
	}

	// FR-256 起 manifest 不再含 sig 段。
	require.NotContains(t, obj, "sig", "manifest 不应再含签名段")

	// 自更新段。
	agent := obj["agent"].(map[string]any)
	core := agent["core"].(map[string]any)
	require.EqualValues(t, 5, core["version"])
	platforms := core["platforms"].(map[string]any)
	require.Contains(t, platforms, "windows")
}

// TestSignedManifest_CleanExcludeOmit 锁定 FR-255 方案 A 的 omitempty 语义：
// cleanExclude 为空时 JSON 不含该字段（老 manifest 字节不变，向后兼容）。
func TestSignedManifest_CleanExcludeOmit(t *testing.T) {
	m := sampleSignedManifest()
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "cleanExclude",
		"cleanExclude 为空时 JSON 不得包含该字段（omitempty 向后兼容）")
}

// TestSignedManifest_WithCleanExclude 锁定 FR-255 cleanExclude 非空时的 JSON 输出。
func TestSignedManifest_WithCleanExclude(t *testing.T) {
	m := sampleSignedManifest()
	m.ManagedDirs = []string{"*"}
	m.CleanExclude = []string{"mods/keep", "custom"}

	raw, err := json.Marshal(m)
	require.NoError(t, err)
	got := string(raw)
	// cleanExclude 出现在 channel 与 files 之间（码点序）。
	require.Contains(t, got, `"channel":"skyblock-s1","cleanExclude":["mods/keep","custom"],"files":[`)
	// managedDirs 含 "*" 哨兵原样输出。
	require.Contains(t, got, `"managedDirs":["*"]`)
}
