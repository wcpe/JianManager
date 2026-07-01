package service

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// newUpdaterCoreSvc 建一个含制品库 + 频道 + 版本服务的最小测试装配（FR-259）。
func newUpdaterCoreSvc(t *testing.T) (*ClientVersionService, *AssetService, *gorm.DB) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Asset{}, &model.ClientChannel{}, &model.ClientPullKey{}, &model.ClientVersion{}))
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	assetSvc := NewAssetService(db, root)
	channelSvc := NewClientChannelService(db)
	versionSvc := NewClientVersionService(db, assetSvc, channelSvc)
	return versionSvc, assetSvc, db
}

// TestArchiveCoreJar_IngestsAsClientUpdaterCore 归档 core jar → 入库为 client-updater-core 类型、
// 版本号写入 Asset.Version、内容寻址去重（同 sha256 复用）。
func TestArchiveCoreJar_IngestsAsClientUpdaterCore(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	jar := []byte("updater-core-jar-v1")

	asset, err := svc.ArchiveCoreJar(strings.NewReader(string(jar)), "1")
	require.NoError(t, err)
	require.Equal(t, model.AssetTypeClientUpdaterCore, asset.Type)
	require.Equal(t, sha256hex(jar), asset.SHA256)
	require.Equal(t, int64(len(jar)), asset.Size)
	require.Equal(t, "1", asset.Version)

	// 同一 jar 再归档 → 去重复用（不产生新记录）。
	asset2, err := svc.ArchiveCoreJar(strings.NewReader(string(jar)), "1")
	require.NoError(t, err)
	require.Equal(t, asset.ID, asset2.ID)
}

// TestArchiveCoreJar_DifferentVersionsArchiveSeparately 不同版本 jar → 各自归档（不覆盖旧版）。
func TestArchiveCoreJar_DifferentVersionsArchiveSeparately(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	jarV1 := []byte("updater-core-jar-v1")
	jarV2 := []byte("updater-core-jar-v2")

	a1, err := svc.ArchiveCoreJar(strings.NewReader(string(jarV1)), "1")
	require.NoError(t, err)
	a2, err := svc.ArchiveCoreJar(strings.NewReader(string(jarV2)), "2")
	require.NoError(t, err)
	require.NotEqual(t, a1.SHA256, a2.SHA256)
	require.NotEqual(t, a1.ID, a2.ID)
}

// TestGetCoreEndpointInfo_DefaultLatest 未选定版本时 → 回退最新归档版本。
func TestGetCoreEndpointInfo_DefaultLatest(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	_, err := svc.ArchiveCoreJar(strings.NewReader("jar-v1"), "1")
	require.NoError(t, err)
	a2, err := svc.ArchiveCoreJar(strings.NewReader("jar-v2"), "2")
	require.NoError(t, err)

	// 建频道（未选定 core 版本）。
	_, _ = NewClientChannelService(svc.db).CreateChannel("s1", "测试", "")

	info, err := svc.GetCoreEndpointInfo("s1")
	require.NoError(t, err)
	require.Equal(t, 2, info.Version)
	require.Equal(t, a2.SHA256, info.SHA256)
	require.Equal(t, a2.Size, info.Size)
}

// TestGetCoreEndpointInfo_SelectedVersion 选定版本后 → 返回选定版本（非最新）。
func TestGetCoreEndpointInfo_SelectedVersion(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	a1, err := svc.ArchiveCoreJar(strings.NewReader("jar-v1"), "1")
	require.NoError(t, err)
	_, err = svc.ArchiveCoreJar(strings.NewReader("jar-v2"), "2")
	require.NoError(t, err)

	chSvc := NewClientChannelService(svc.db)
	_, _ = chSvc.CreateChannel("s1", "测试", "")

	// 选定 v1（旧版），回滚场景。
	require.NoError(t, svc.SelectCoreVersion("s1", a1.SHA256))

	info, err := svc.GetCoreEndpointInfo("s1")
	require.NoError(t, err)
	require.Equal(t, 1, info.Version, "应返回选定版本 v1 而非最新 v2")
	require.Equal(t, a1.SHA256, info.SHA256)
}

// TestGetCoreEndpointInfo_NoArchive 无任何归档 → ErrNoCoreVersion。
func TestGetCoreEndpointInfo_NoArchive(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	_, _ = NewClientChannelService(svc.db).CreateChannel("s1", "测试", "")

	_, err := svc.GetCoreEndpointInfo("s1")
	require.ErrorIs(t, err, ErrNoCoreVersion)
}

// TestGetCoreEndpointInfo_ChannelNotFound 频道不存在 → ErrChannelNotFound。
func TestGetCoreEndpointInfo_ChannelNotFound(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	_, err := svc.ArchiveCoreJar(strings.NewReader("jar-v1"), "1")
	require.NoError(t, err)

	_, err = svc.GetCoreEndpointInfo("nonexistent")
	require.ErrorIs(t, err, ErrChannelNotFound)
}

// TestListCoreVersions 列出所有归档版本（按版本号 DESC）+ 标记频道选定版本。
func TestListCoreVersions(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	a1, _ := svc.ArchiveCoreJar(strings.NewReader("jar-v1"), "1")
	a2, _ := svc.ArchiveCoreJar(strings.NewReader("jar-v2"), "2")

	chSvc := NewClientChannelService(svc.db)
	_, _ = chSvc.CreateChannel("s1", "测试", "")
	// 选定 v1（旧版）。
	require.NoError(t, svc.SelectCoreVersion("s1", a1.SHA256))

	versions, err := svc.ListCoreVersions("s1")
	require.NoError(t, err)
	require.Len(t, versions, 2)
	// 最新在前。
	require.Equal(t, 2, versions[0].Version)
	require.Equal(t, a2.SHA256, versions[0].SHA256)
	require.False(t, versions[0].Selected, "v2 非选定")
	// v1 是选定版本。
	require.Equal(t, 1, versions[1].Version)
	require.Equal(t, a1.SHA256, versions[1].SHA256)
	require.True(t, versions[1].Selected, "v1 应标记为选定")
}

// TestSelectCoreVersion_InvalidSHA 选定不存在的 sha256 → ErrCoreVersionNotFound。
func TestSelectCoreVersion_InvalidSHA(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	_, _ = NewClientChannelService(svc.db).CreateChannel("s1", "测试", "")

	err := svc.SelectCoreVersion("s1", sha256hex([]byte("nonexistent")))
	require.ErrorIs(t, err, ErrCoreVersionNotFound)
}

// TestSelectCoreVersion_ChannelNotFound 频道不存在 → ErrChannelNotFound。
func TestSelectCoreVersion_ChannelNotFound(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	a1, _ := svc.ArchiveCoreJar(strings.NewReader("jar-v1"), "1")

	err := svc.SelectCoreVersion("nonexistent", a1.SHA256)
	require.ErrorIs(t, err, ErrChannelNotFound)
}

// TestOpenArtifact_SupportsClientUpdaterCore OpenArtifact 扩展支持 client-updater-core 类型，
// 使 /client-artifacts/:sha256 端点能分发 core jar。
func TestOpenArtifact_SupportsClientUpdaterCore(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	jar := []byte("updater-core-jar-v1")
	a, err := svc.ArchiveCoreJar(strings.NewReader(string(jar)), "1")
	require.NoError(t, err)

	asset, absPath, err := svc.OpenArtifact(a.SHA256)
	require.NoError(t, err)
	require.Equal(t, model.AssetTypeClientUpdaterCore, asset.Type)
	require.NotEmpty(t, absPath)
}
