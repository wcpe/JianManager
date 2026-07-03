package service

import (
	"archive/zip"
	"bytes"
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

func TestArchiveCoreJar_AutoBumpsNonMonotonicVersion(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)

	a1, err := svc.ArchiveCoreJar(strings.NewReader("updater-core-jar-v1"), "1")
	require.NoError(t, err)
	a2, err := svc.ArchiveCoreJar(strings.NewReader("updater-core-jar-v2"), "1")
	require.NoError(t, err)

	require.Equal(t, "1", a1.Version)
	require.Equal(t, "2", a2.Version, "不同 core 内容重复传 v1 时应自动递增，避免管理面全显示 v1")
}

func TestArchiveCoreJar_ReadsBuildMetadataFromJar(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	jar := testUpdaterCoreJar(t, "version=0.1.0-SNAPSHOT\ngitCommit=abc123def456\ndirty=true\nbuildTime=2026-07-03T00:00:00Z\n")

	asset, err := svc.ArchiveCoreJar(bytes.NewReader(jar), "1")
	require.NoError(t, err)
	require.Equal(t, "1", asset.Version, "归档数字版本仍保持独立递增轴")

	versions, err := svc.ListCoreVersions()
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, "0.1.0-SNAPSHOT", versions[0].CoreVersion)
	require.Equal(t, "abc123def456", versions[0].GitCommit)
	require.True(t, versions[0].Dirty)
	require.Equal(t, "2026-07-03T00:00:00Z", versions[0].BuildTime)
	require.Equal(t, "0.1.0-SNAPSHOT+abc123def456.dirty", versions[0].DisplayVersion)
}

func TestArchiveCoreJar_ReadsBuildMetadataFromManifestFallback(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	jar := testUpdaterCoreManifestJar(t, "Manifest-Version: 1.0\nJM-Updater-Core-Version: 0.2.0\nJM-Git-Commit: def456abc789\nJM-Git-Dirty: false\nJM-Build-Time: 2026-07-03T01:00:00Z\n")

	_, err := svc.ArchiveCoreJar(bytes.NewReader(jar), "1")
	require.NoError(t, err)
	versions, err := svc.ListCoreVersions()
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, "0.2.0", versions[0].CoreVersion)
	require.Equal(t, "def456abc789", versions[0].GitCommit)
	require.False(t, versions[0].Dirty)
	require.Equal(t, "2026-07-03T01:00:00Z", versions[0].BuildTime)
	require.Equal(t, "0.2.0+def456abc789", versions[0].DisplayVersion)
}

func TestArchiveCoreJar_AllowsHotfixJarWithoutBuildMetadata(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)

	asset, err := svc.ArchiveCoreJar(strings.NewReader("plain-hotfix-core"), "1")
	require.NoError(t, err)
	versions, err := svc.ListCoreVersions()
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, 1, versions[0].Version)
	require.Empty(t, versions[0].CoreVersion)
	require.Empty(t, versions[0].GitCommit)
	require.False(t, versions[0].Dirty)
	require.Equal(t, asset.SHA256, versions[0].SHA256)
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

// TestGetCoreEndpointInfo_SelectedVersion 选定版本后 → 返回选定制品，并使用频道级递增版本号。
func TestGetCoreEndpointInfo_SelectedVersion(t *testing.T) {
	svc, _, _ := newUpdaterCoreSvc(t)
	a1, err := svc.ArchiveCoreJar(strings.NewReader("jar-v1"), "1")
	require.NoError(t, err)
	a2, err := svc.ArchiveCoreJar(strings.NewReader("jar-v2"), "2")
	require.NoError(t, err)

	chSvc := NewClientChannelService(svc.db)
	_, _ = chSvc.CreateChannel("s1", "测试", "")

	// 选定最新 v2 时端点 version 保持归档版本，不无意义 +1。
	require.NoError(t, svc.SelectCoreVersion("s1", a2.SHA256))
	latest, err := svc.GetCoreEndpointInfo("s1")
	require.NoError(t, err)
	require.Equal(t, 2, latest.Version)
	require.Equal(t, a2.SHA256, latest.SHA256)

	// 选回 v1（旧版），回滚场景：sha 指向 v1，但端点 version 必须继续递增，楔子才会下载。
	require.NoError(t, svc.SelectCoreVersion("s1", a1.SHA256))

	info, err := svc.GetCoreEndpointInfo("s1")
	require.NoError(t, err)
	require.Equal(t, 3, info.Version, "回滚旧 SHA 时应返回频道级递增版本，避免楔子因 1 < 2 跳过下载")
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

func TestDelete_RejectsEmbeddedUpdaterCore(t *testing.T) {
	svc, assetSvc, _ := newUpdaterCoreSvc(t)
	a, err := svc.ArchiveCoreJar(strings.NewReader("jar-v1"), "1")
	require.NoError(t, err)

	err = assetSvc.Delete(a.ID)
	require.ErrorIs(t, err, ErrAssetInUse)
	var inUse *AssetInUseError
	require.ErrorAs(t, err, &inUse)
	require.Equal(t, AssetInUseEmbeddedUpdaterCore, inUse.Reason)
}

func TestDelete_RejectsSelectedUpdaterCore(t *testing.T) {
	svc, assetSvc, db := newUpdaterCoreSvc(t)
	a, err := assetSvc.Ingest(strings.NewReader("manual-core"), IngestParams{
		Type:     model.AssetTypeClientUpdaterCore,
		Name:     "updater-core",
		Version:  "1",
		Filename: "updater-core.jar",
		Metadata: `{"codec":"none","source":"manual-upload"}`,
	})
	require.NoError(t, err)
	_, _ = NewClientChannelService(db).CreateChannel("s1", "测试", "")
	require.NoError(t, svc.SelectCoreVersion("s1", a.SHA256))

	err = assetSvc.Delete(a.ID)
	require.ErrorIs(t, err, ErrAssetInUse)
	var inUse *AssetInUseError
	require.ErrorAs(t, err, &inUse)
	require.Equal(t, AssetInUseSelectedUpdaterCore, inUse.Reason)
	require.Equal(t, int64(1), inUse.Count)
}

func testUpdaterCoreJar(t *testing.T, properties string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("META-INF/jm-updater-core.properties")
	require.NoError(t, err)
	_, err = w.Write([]byte(properties))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func testUpdaterCoreManifestJar(t *testing.T, manifest string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("META-INF/MANIFEST.MF")
	require.NoError(t, err)
	_, err = w.Write([]byte(manifest))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}
