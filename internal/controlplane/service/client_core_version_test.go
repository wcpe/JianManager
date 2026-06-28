package service

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// newCoreVersionStack 装配 core 版本管理测试栈：共享内存 DB + dataroot 制品库 +
// 频道/版本/core 版本服务（含开发签名器），便于端到端断言 manifest agent.core 由 pin 驱动。
func newCoreVersionStack(t *testing.T) (*ClientCoreVersionService, *ClientChannelService, *ClientVersionService, *AssetService) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.Asset{}, &model.ClientChannel{}, &model.ClientPullKey{},
		&model.ClientVersion{}, &model.ClientCoreVersion{},
	))
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	assets := NewAssetService(db, root)
	channel := NewClientChannelService(db)
	coreVer := NewClientCoreVersionService(db, assets)
	signer, err := NewManifestSigner(DevSignPrivateKeyPKCS8Base64, DefaultSignKeyID)
	require.NoError(t, err)
	version := NewClientVersionService(db, assets, channel, signer)
	version.SetCoreVersions(coreVer)
	return coreVer, channel, version, assets
}

// uploadAndRegisterCore 上传一份 core jar 字节并登记为新 core 版本，返回登记记录。
func uploadAndRegisterCore(t *testing.T, svc *ClientCoreVersionService, bytes string, note string) *model.ClientCoreVersion {
	t.Helper()
	res, err := svc.UploadCore(strings.NewReader(bytes), UploadCoreParams{Filename: "updater-core.jar", Codec: "zstd"})
	require.NoError(t, err)
	ver, err := svc.RegisterVersion(RegisterCoreVersionParams{
		ArtifactSHA256: res.SHA256, ArtifactSize: res.Size, Codec: res.Codec, Note: note,
	})
	require.NoError(t, err)
	return ver
}

// TestRegisterCoreVersion_MonotonicVersions 登记的 core 版本号从 1 起单调递增，制品引用正确。
func TestRegisterCoreVersion_MonotonicVersions(t *testing.T) {
	core, _, _, _ := newCoreVersionStack(t)

	v1 := uploadAndRegisterCore(t, core, "core-bytes-v1", "首发")
	require.Equal(t, 1, v1.Version)
	require.NotEmpty(t, v1.ArtifactSHA256)

	v2 := uploadAndRegisterCore(t, core, "core-bytes-v2", "第二版")
	require.Equal(t, 2, v2.Version)
	require.NotEqual(t, v1.ArtifactSHA256, v2.ArtifactSHA256)

	// 列表 DESC，含 2 条。
	list, err := core.ListVersions()
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, 2, list[0].Version)
	require.Equal(t, 1, list[1].Version)
}

// TestRegisterCoreVersion_RejectsUnknownArtifact 登记版本时制品须已在 client-core 库，否则拒绝。
func TestRegisterCoreVersion_RejectsUnknownArtifact(t *testing.T) {
	core, _, _, _ := newCoreVersionStack(t)
	_, err := core.RegisterVersion(RegisterCoreVersionParams{
		ArtifactSHA256: strings.Repeat("a", 64), ArtifactSize: 10, Codec: "zstd",
	})
	require.ErrorIs(t, err, ErrCoreArtifactNotFound)
}

// TestResolveForChannel_PinZeroUsesLatest pin=0 解析为最新已登记 core 版本。
func TestResolveForChannel_PinZeroUsesLatest(t *testing.T) {
	core, _, _, _ := newCoreVersionStack(t)
	uploadAndRegisterCore(t, core, "core-bytes-v1", "")
	v2 := uploadAndRegisterCore(t, core, "core-bytes-v2", "")

	got, err := core.ResolveForChannel(0)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, v2.Version, got.Version)
}

// TestResolveForChannel_NoneRegistered 无任何已登记 core → 返回 nil（无错误），manifest 回退手填透传。
func TestResolveForChannel_NoneRegistered(t *testing.T) {
	core, _, _, _ := newCoreVersionStack(t)
	got, err := core.ResolveForChannel(0)
	require.NoError(t, err)
	require.Nil(t, got)
}

// TestManifestAgentCore_DrivenByPin manifest agent.core 由频道 pin 驱动：版本号 + 三平台同制品 fan-out。
func TestManifestAgentCore_DrivenByPin(t *testing.T) {
	core, channel, version, _ := newCoreVersionStack(t)
	_, err := channel.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)

	// 频道发一版内容（含手填透传 agent.core，应被 pin 驱动覆盖）。
	res, err := version.PublishFile(strings.NewReader("mod-bytes"), PublishFileParams{Filename: "foo.jar", Codec: "zstd"})
	require.NoError(t, err)
	_, err = version.PublishVersion("skyblock-s1", PublishVersionParams{
		Files: []ManifestFile{{
			Path: "mods/foo.jar", SHA256: "ab12", MD5: "cd34", Size: 1, Sync: "strict",
			Artifact: ManifestArtifact{SHA256: res.SHA256, Size: res.Size, Codec: "zstd"},
		}},
		ManagedDirs: []string{"mods"},
		Agent: &ManifestAgent{Core: &ManifestCore{
			Version:   99, // 手填旧值，应被 pin 驱动覆盖。
			Platforms: map[string]ManifestAgentArtifact{"windows": {SHA256: "stale", Size: 1, Codec: "none"}},
		}},
	})
	require.NoError(t, err)

	v1 := uploadAndRegisterCore(t, core, "core-bytes-v1", "")
	// 频道默认 pin=0 → 用最新 core v1。
	m, err := version.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.NotNil(t, m.Agent)
	require.NotNil(t, m.Agent.Core)
	require.Equal(t, v1.Version, m.Agent.Core.Version, "agent.core.version 应由 pin 驱动、覆盖手填 99")
	// 三平台 fan-out，同一制品。
	for _, os := range []string{"windows", "macos", "linux"} {
		art, ok := m.Agent.Core.Platforms[os]
		require.True(t, ok, "应填 %s 键", os)
		require.Equal(t, v1.ArtifactSHA256, art.SHA256)
		require.Equal(t, v1.ArtifactSize, art.Size)
		require.Equal(t, "zstd", art.Codec)
	}
}

// TestManifestAgentCore_NoCoreRegistered_FallsBackToPassthrough 无 core 注册时沿用手填透传（不破 FR-087/088）。
func TestManifestAgentCore_NoCoreRegistered_FallsBackToPassthrough(t *testing.T) {
	_, channel, version, _ := newCoreVersionStack(t)
	_, err := channel.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	res, err := version.PublishFile(strings.NewReader("mod-bytes"), PublishFileParams{Filename: "foo.jar", Codec: "zstd"})
	require.NoError(t, err)
	_, err = version.PublishVersion("skyblock-s1", PublishVersionParams{
		Files: []ManifestFile{{
			Path: "mods/foo.jar", SHA256: "ab12", MD5: "cd34", Size: 1, Sync: "strict",
			Artifact: ManifestArtifact{SHA256: res.SHA256, Size: res.Size, Codec: "zstd"},
		}},
		ManagedDirs: []string{"mods"},
		Agent: &ManifestAgent{Core: &ManifestCore{
			Version:   7,
			Platforms: map[string]ManifestAgentArtifact{"windows": {SHA256: "kept", Size: 2, Codec: "none"}},
		}},
	})
	require.NoError(t, err)

	m, err := version.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.NotNil(t, m.Agent.Core)
	require.Equal(t, 7, m.Agent.Core.Version, "无 core 注册应沿用手填透传 version")
	require.Equal(t, "kept", m.Agent.Core.Platforms["windows"].SHA256)
}

// TestUpdateChannelPin_DrivesNewerCore 更新 pin 到更高版本 → manifest agent.core.version 升。
func TestUpdateChannelPin_DrivesNewerCore(t *testing.T) {
	core, channel, version, _ := newCoreVersionStack(t)
	_, err := channel.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	publishMinimalVersion(t, version, "skyblock-s1")

	v1 := uploadAndRegisterCore(t, core, "core-bytes-v1", "")
	v2 := uploadAndRegisterCore(t, core, "core-bytes-v2", "")

	// 显式 pin 到 v1。
	require.NoError(t, core.SetChannelPin("skyblock-s1", v1.Version))
	m, err := version.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.Equal(t, v1.Version, m.Agent.Core.Version)

	// 更新 pin 到 v2 → agent.core.version 升。
	require.NoError(t, core.SetChannelPin("skyblock-s1", v2.Version))
	m, err = version.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.Equal(t, v2.Version, m.Agent.Core.Version)
}

// TestSetChannelPin_RejectsUnknownVersion pin 到不存在的 core 版本 → 拒绝。
func TestSetChannelPin_RejectsUnknownVersion(t *testing.T) {
	core, channel, _, _ := newCoreVersionStack(t)
	_, err := channel.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	require.ErrorIs(t, core.SetChannelPin("skyblock-s1", 999), ErrCoreVersionNotFound)
}

// TestRollbackChannelCore_RepublishesHigherVersion 回退坏 core = 以更高版本号重发旧字节，
// agent.core.version 只升不降，pin 指向新版本、内容为旧 core 字节（ADR-045 决策 4）。
func TestRollbackChannelCore_RepublishesHigherVersion(t *testing.T) {
	core, channel, version, _ := newCoreVersionStack(t)
	_, err := channel.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	publishMinimalVersion(t, version, "skyblock-s1")

	v1 := uploadAndRegisterCore(t, core, "good-core-bytes", "好版本")
	v2 := uploadAndRegisterCore(t, core, "bad-core-bytes", "坏版本")
	require.NoError(t, core.SetChannelPin("skyblock-s1", v2.Version))

	// 回退坏 core v2 → 以 v1（好版本）字节重发为更高版本 v3，pin 指向 v3。
	v3, err := core.RollbackChannelCore("skyblock-s1", v1.Version, 0, "")
	require.NoError(t, err)
	require.Equal(t, 3, v3.Version, "回退必须以更高版本号重发，不可降版")
	require.Equal(t, v1.ArtifactSHA256, v3.ArtifactSHA256, "重发的应是旧好版本 v1 的字节（同一内容寻址制品）")
	require.Equal(t, v1.Version, v3.SourceVersion)

	// manifest agent.core.version = 3（升），内容 = v1 字节。
	m, err := version.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.Equal(t, 3, m.Agent.Core.Version)
	require.Equal(t, v1.ArtifactSHA256, m.Agent.Core.Platforms["windows"].SHA256)

	// 频道 pin 已落到 3。
	got, err := channel.GetChannel("skyblock-s1")
	require.NoError(t, err)
	require.Equal(t, 3, got.PinnedCoreVersion)
}

// publishMinimalVersion 发一版最小内容（一个文件），供 BuildManifest 有 latest 可组装。
func publishMinimalVersion(t *testing.T, version *ClientVersionService, channelID string) {
	t.Helper()
	res, err := version.PublishFile(strings.NewReader("mod-bytes-"+channelID), PublishFileParams{Filename: "foo.jar", Codec: "zstd"})
	require.NoError(t, err)
	_, err = version.PublishVersion(channelID, PublishVersionParams{
		Files: []ManifestFile{{
			Path: "mods/foo.jar", SHA256: "ab12", MD5: "cd34", Size: 1, Sync: "strict",
			Artifact: ManifestArtifact{SHA256: res.SHA256, Size: res.Size, Codec: "zstd"},
		}},
		ManagedDirs: []string{"mods"},
	})
	require.NoError(t, err)
}
