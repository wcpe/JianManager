package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

type fakeArtifactVersionProvider struct {
	releases []ArtifactRelease
	err      error
}

func (p fakeArtifactVersionProvider) ListVersions(_ context.Context, _ model.ArtifactSource) ([]ArtifactRelease, error) {
	return p.releases, p.err
}

func newArtifactVersionService(t *testing.T) (*ArtifactVersionService, *AssetService) {
	t.Helper()
	assets, _ := newAssetSvc(t)
	require.NoError(t, assets.db.AutoMigrate(
		&model.ArtifactPackage{},
		&model.ArtifactSource{},
		&model.ArtifactVersion{},
		&model.Node{},
		&model.Instance{},
	))
	return NewArtifactVersionService(assets.db, assets), assets
}

func TestArtifactVersionService_SyncOnlyRegistersVersions(t *testing.T) {
	svc, _ := newArtifactVersionService(t)
	pkg, source, err := svc.EnsureDefaultServerProbe()
	require.NoError(t, err)
	require.Equal(t, model.ServerProbePackageKey, pkg.Key)
	require.Equal(t, model.ArtifactProviderGitHubRelease, source.Provider)

	svc.SetProvider(model.ArtifactProviderGitHubRelease, fakeArtifactVersionProvider{releases: []ArtifactRelease{{
		Version:    "0.2.0",
		ReleaseRef: "v0.2.0",
		AssetName:  "ServerProbe-0.2.0.jar",
		URL:        "https://example.test/ServerProbe-0.2.0.jar",
		SHA256:     sha256Text("serverprobe-v0.2.0"),
	}}})

	created, err := svc.SyncSource(context.Background(), source.ID)
	require.NoError(t, err)
	require.Equal(t, 1, created)

	versions, err := svc.ListVersions(pkg.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Zero(t, versions[0].AssetID, "同步元数据不应预先下载 jar")
	require.Empty(t, versions[0].CachedAt)

	created, err = svc.SyncSource(context.Background(), source.ID)
	require.NoError(t, err)
	require.Zero(t, created, "同一 release 重复同步不得重复登记")
}

func TestArtifactVersionService_UploadLocalServerProbeReusesCAS(t *testing.T) {
	svc, assets := newArtifactVersionService(t)
	pkg, githubSource, err := svc.EnsureDefaultServerProbe()
	require.NoError(t, err)

	jar := []byte("serverprobe-v0.2.0")
	githubAsset, err := assets.Ingest(bytes.NewReader(jar), IngestParams{
		Type: model.AssetTypeServerProbe, Name: "ServerProbe", Version: "0.2.0", Filename: "ServerProbe-0.2.0.jar",
	})
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, svc.db.Create(&model.ArtifactVersion{
		PackageID: pkg.ID, SourceID: githubSource.ID, Version: "0.2.0", ReleaseRef: "v0.2.0",
		AssetName: "ServerProbe-0.2.0.jar", ExpectedSHA256: githubAsset.SHA256,
		SourceURL: "https://github.example/ServerProbe-0.2.0.jar", AssetID: githubAsset.ID, CachedAt: &now,
	}).Error)

	uploaded, err := svc.UploadLocalServerProbe("0.1.0", "ServerProbe-0.1.0.jar", bytes.NewReader(jar))
	require.NoError(t, err)
	require.Equal(t, githubAsset.ID, uploaded.AssetID, "同一字节必须复用 CAS 制品")
	require.NotNil(t, uploaded.CachedAt)
	require.Equal(t, githubAsset.SHA256, uploaded.ExpectedSHA256)
	require.Equal(t, "local://upload/"+strconv.FormatUint(uint64(uploaded.AssetID), 10), uploaded.SourceURL)
	require.Zero(t, pkg.DefaultVersionID, "上传不得改变全局默认版本")

	var source model.ArtifactSource
	require.NoError(t, svc.db.First(&source, uploaded.SourceID).Error)
	require.Equal(t, model.ArtifactProviderLocalUpload, source.Provider)
	require.Equal(t, "本地上传", source.Name)

	_, err = svc.UploadLocalServerProbe("0.1.0", "ServerProbe-0.1.0.jar", bytes.NewReader(jar))
	require.ErrorIs(t, err, ErrArtifactVersionAlreadyExists)
}

func TestArtifactVersionService_UploadLocalServerProbeAllowsMultipleVersions(t *testing.T) {
	svc, _ := newArtifactVersionService(t)

	first, err := svc.UploadLocalServerProbe("0.1.0", "ServerProbe-0.1.0.jar", strings.NewReader("serverprobe-v0.1.0"))
	require.NoError(t, err)
	second, err := svc.UploadLocalServerProbe("0.1.1", "ServerProbe-0.1.1.jar", strings.NewReader("serverprobe-v0.1.1"))
	require.NoError(t, err)
	require.Equal(t, first.SourceID, second.SourceID)
	require.NotEqual(t, first.ID, second.ID)
}

func TestArtifactVersionService_SyncRejectsLocalUploadSource(t *testing.T) {
	svc, _ := newArtifactVersionService(t)
	pkg, _, err := svc.EnsureDefaultServerProbe()
	require.NoError(t, err)
	source, err := svc.ensureLocalServerProbeSource(pkg.ID)
	require.NoError(t, err)

	_, err = svc.SyncSource(context.Background(), source.ID)
	require.ErrorIs(t, err, ErrArtifactSourceNotSyncable)
}

func TestArtifactVersionService_CacheVerifiesDigestAndResolvesInheritance(t *testing.T) {
	svc, _ := newArtifactVersionService(t)
	pkg, source, err := svc.EnsureDefaultServerProbe()
	require.NoError(t, err)
	jar := []byte("serverprobe-v0.2.0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jar)
	}))
	defer server.Close()
	svc.SetProvider(model.ArtifactProviderGitHubRelease, fakeArtifactVersionProvider{releases: []ArtifactRelease{{
		Version:    "0.2.0",
		ReleaseRef: "v0.2.0",
		AssetName:  "ServerProbe-0.2.0.jar",
		URL:        server.URL + "/ServerProbe-0.2.0.jar",
		SHA256:     sha256Text(string(jar)),
	}}})
	_, err = svc.SyncSource(context.Background(), source.ID)
	require.NoError(t, err)
	versions, err := svc.ListVersions(pkg.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)

	cached, err := svc.CacheVersion(context.Background(), versions[0].ID)
	require.NoError(t, err)
	require.NotZero(t, cached.AssetID)
	require.NotNil(t, cached.CachedAt)
	require.NotNil(t, cached.Asset)
	require.Equal(t, model.AssetTypeServerProbe, cached.Asset.Type)
	require.Equal(t, sha256Text(string(jar)), cached.Asset.SHA256)

	require.NoError(t, svc.SetPackageDefaultVersion(pkg.ID, cached.ID))
	node := &model.Node{Name: "node-a", Host: "127.0.0.1", Secret: "secret"}
	require.NoError(t, svc.db.Create(node).Error)
	inst := &model.Instance{Name: "paper", NodeID: node.ID, Type: model.InstanceTypeMinecraftJava, ProcessType: model.ProcessTypeDirect, StartCommand: "java"}
	require.NoError(t, svc.db.Create(inst).Error)

	resolved, origin, err := svc.ResolveInstanceProbeVersion(inst.ID)
	require.NoError(t, err)
	require.Equal(t, cached.ID, resolved.ID)
	require.Equal(t, ProbeVersionOriginGlobal, origin)

	require.NoError(t, svc.SetNodeProbeVersion(node.ID, cached.ID))
	resolved, origin, err = svc.ResolveInstanceProbeVersion(inst.ID)
	require.NoError(t, err)
	require.Equal(t, cached.ID, resolved.ID)
	require.Equal(t, ProbeVersionOriginNode, origin)

	require.NoError(t, svc.SetInstanceProbeVersion(inst.ID, cached.ID))
	resolved, origin, err = svc.ResolveInstanceProbeVersion(inst.ID)
	require.NoError(t, err)
	require.Equal(t, cached.ID, resolved.ID)
	require.Equal(t, ProbeVersionOriginInstance, origin)
}

func TestArtifactVersionService_RejectsUncachedDefaultAndReferencedDelete(t *testing.T) {
	svc, assets := newArtifactVersionService(t)
	pkg, source, err := svc.EnsureDefaultServerProbe()
	require.NoError(t, err)
	uncached := &model.ArtifactVersion{
		PackageID:      pkg.ID,
		SourceID:       source.ID,
		Version:        "0.1.0",
		ReleaseRef:     "v0.1.0",
		AssetName:      "ServerProbe-0.1.0.jar",
		ExpectedSHA256: sha256Text("serverprobe-v0.1.0"),
		SourceURL:      "https://example.test/ServerProbe-0.1.0.jar",
	}
	require.NoError(t, svc.db.Create(uncached).Error)
	require.ErrorIs(t, svc.SetPackageDefaultVersion(pkg.ID, uncached.ID), ErrArtifactVersionNotCached)

	asset, err := assets.Ingest(strings.NewReader("serverprobe-v0.1.0"), IngestParams{
		Type:     model.AssetTypeServerProbe,
		Filename: uncached.AssetName,
	})
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, svc.db.Model(uncached).Updates(map[string]any{"asset_id": asset.ID, "cached_at": now}).Error)
	require.NoError(t, svc.SetPackageDefaultVersion(pkg.ID, uncached.ID))
	require.ErrorIs(t, svc.DeleteVersion(uncached.ID), ErrArtifactVersionInUse)
}

func TestArtifactVersionService_ProbeDownloadTokenIsBoundToVersionAndWorker(t *testing.T) {
	svc, _ := newArtifactVersionService(t)

	token, err := svc.IssueProbeDownloadToken(ProbeDownloadTokenScope{
		VersionID: 7,
		NodeUUID:  "worker-a",
	})
	require.NoError(t, err)

	_, err = svc.ValidateProbeDownloadToken(token, ProbeDownloadTokenScope{VersionID: 7, NodeUUID: "worker-a"})
	require.NoError(t, err)
	_, err = svc.ValidateProbeDownloadToken(token, ProbeDownloadTokenScope{VersionID: 8, NodeUUID: "worker-a"})
	require.ErrorIs(t, err, ErrProbeDownloadTokenInvalid)
	_, err = svc.ValidateProbeDownloadToken(token, ProbeDownloadTokenScope{VersionID: 7, NodeUUID: "worker-b"})
	require.ErrorIs(t, err, ErrProbeDownloadTokenInvalid)

	downloadURL, err := svc.BuildProbeDownloadURL("https://cp.example.test/base", 7, token)
	require.NoError(t, err)
	parsed, err := url.Parse(downloadURL)
	require.NoError(t, err)
	require.Equal(t, "/base/probe-artifacts/7/download", parsed.Path)
	require.Equal(t, token, parsed.Query().Get("token"))
}

func sha256Text(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
