package register

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIdentity_SaveLoadRoundtrip 写入后读回字段一致（FR-080，见 ADR-020 §3）。
func TestIdentity_SaveLoadRoundtrip(t *testing.T) {
	etcDir := t.TempDir()
	want := &Identity{NodeUUID: "uuid-123", NodeSecret: "secret-xyz", NodeName: "edge-1"}

	require.NoError(t, SaveIdentity(etcDir, want))

	got, err := LoadIdentity(etcDir)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, want.NodeUUID, got.NodeUUID)
	assert.Equal(t, want.NodeSecret, got.NodeSecret)
	assert.Equal(t, want.NodeName, got.NodeName)
}

// TestIdentity_LoadMissingReturnsNil 文件不存在返回 (nil, nil)，对应首次安装的正常情形。
func TestIdentity_LoadMissingReturnsNil(t *testing.T) {
	got, err := LoadIdentity(t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestIdentity_LoadCorruptErrors 文件存在但非法 JSON / 缺关键字段时报错（由调用方决定回退）。
func TestIdentity_LoadCorruptErrors(t *testing.T) {
	etcDir := t.TempDir()

	require.NoError(t, os.WriteFile(IdentityPath(etcDir), []byte("not-json"), 0o600))
	_, err := LoadIdentity(etcDir)
	assert.Error(t, err)

	require.NoError(t, os.WriteFile(IdentityPath(etcDir), []byte(`{"nodeUuid":"x"}`), 0o600))
	_, err = LoadIdentity(etcDir)
	assert.Error(t, err, "缺 nodeSecret 应报错")
}

// TestIdentity_SavePerm0600 身份文件含 node_secret，落盘权限须为 0600（类 Unix 平台断言）。
func TestIdentity_SavePerm0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不适用 POSIX 文件权限位")
	}
	etcDir := t.TempDir()
	require.NoError(t, SaveIdentity(etcDir, &Identity{NodeUUID: "u", NodeSecret: "s", NodeName: "n"}))

	info, err := os.Stat(IdentityPath(etcDir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// TestIdentity_SaveAtomicNoTempLeft 写入成功后不残留 .tmp 临时文件。
func TestIdentity_SaveAtomicNoTempLeft(t *testing.T) {
	etcDir := t.TempDir()
	require.NoError(t, SaveIdentity(etcDir, &Identity{NodeUUID: "u", NodeSecret: "s", NodeName: "n"}))

	_, err := os.Stat(filepath.Join(etcDir, identityFileName+".tmp"))
	assert.True(t, os.IsNotExist(err), "不应残留 .tmp 文件")
}
