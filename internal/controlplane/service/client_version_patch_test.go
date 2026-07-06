package service

import (
	"bytes"
	"os"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

func newClientVersionPatchFixture(t *testing.T) (*ClientVersionService, *AssetService, *ClientDistSecurityService) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Asset{}, &model.ClientChannel{}, &model.ClientVersion{}, &model.ClientPullKey{}))
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	assetSvc := NewAssetService(db, root)
	channelSvc := NewClientChannelService(db)
	versionSvc := NewClientVersionService(db, assetSvc, channelSvc)
	securitySvc := NewClientDistSecurityService(db, channelSvc, versionSvc)
	_, err = channelSvc.CreateChannel("skyblock-s1", "空岛一服", "")
	require.NoError(t, err)
	return versionSvc, assetSvc, securitySvc
}

func publishPatchTestFile(t *testing.T, svc *ClientVersionService, content []byte) ManifestFile {
	t.Helper()
	asset, err := svc.PublishFile(bytes.NewReader(content), PublishFileParams{Filename: "big.bin", Codec: "none"})
	require.NoError(t, err)
	return ManifestFile{
		Path:     "mods/big.bin",
		SHA256:   sha256hex(content),
		MD5:      md5hex(content),
		Size:     int64(len(content)),
		Sync:     "strict",
		Artifact: ManifestArtifact{SHA256: asset.SHA256, Size: asset.Size, Codec: asset.Codec},
	}
}

func TestClientVersion_PublishGeneratesZstdPatchFromLatest(t *testing.T) {
	versionSvc, assetSvc, securitySvc := newClientVersionPatchFixture(t)
	oldContent := []byte("large resource pack version 1 -- shared payload -- shared payload")
	newContent := []byte("large resource pack version 2 -- shared payload -- shared payload")

	oldFile := publishPatchTestFile(t, versionSvc, oldContent)
	_, err := versionSvc.PublishVersion("skyblock-s1", PublishVersionParams{
		Files:       []ManifestFile{oldFile},
		ManagedDirs: []string{"mods"},
	})
	require.NoError(t, err)

	newFile := publishPatchTestFile(t, versionSvc, newContent)
	_, err = versionSvc.PublishVersion("skyblock-s1", PublishVersionParams{
		Files:       []ManifestFile{newFile},
		ManagedDirs: []string{"mods"},
	})
	require.NoError(t, err)

	manifest, err := versionSvc.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.Len(t, manifest.Files, 1)
	patch := manifest.Files[0].Patch
	require.NotNil(t, patch)
	require.Equal(t, oldFile.SHA256, patch.OldSHA256)
	require.Equal(t, newFile.SHA256, patch.NewSHA256)
	require.Equal(t, "zstd-patch", patch.Artifact.Codec)
	require.NotEmpty(t, patch.Artifact.SHA256)

	_, patchPath, err := versionSvc.OpenArtifact(patch.Artifact.SHA256)
	require.NoError(t, err)
	patchBytes := mustReadFile(t, patchPath)
	require.NotEmpty(t, patchBytes)
	dec, err := zstd.NewReader(nil, zstd.WithDecoderDictRaw(0, oldContent))
	require.NoError(t, err)
	decoded, err := dec.DecodeAll(patchBytes, nil)
	require.NoError(t, err)
	dec.Close()
	require.Equal(t, newContent, decoded)

	err = assetSvc.Delete(mustFindAssetID(t, assetSvc.db, patch.Artifact.SHA256))
	require.ErrorIs(t, err, ErrAssetInUse)
	ok, err := securitySvc.IsArtifactAllowedForChannel("skyblock-s1", patch.Artifact.SHA256)
	require.NoError(t, err)
	require.True(t, ok, "防护授权应允许 latest manifest 引用的 patch 制品")
}

func TestClientVersion_PublishSkipsPatchWithoutPriorVersion(t *testing.T) {
	versionSvc, _, _ := newClientVersionPatchFixture(t)
	file := publishPatchTestFile(t, versionSvc, []byte("first version content"))

	_, err := versionSvc.PublishVersion("skyblock-s1", PublishVersionParams{
		Files:       []ManifestFile{file},
		ManagedDirs: []string{"mods"},
	})
	require.NoError(t, err)

	manifest, err := versionSvc.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.Nil(t, manifest.Files[0].Patch)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func mustFindAssetID(t *testing.T, db *gorm.DB, sha string) uint {
	t.Helper()
	var asset model.Asset
	require.NoError(t, db.Where("type = ? AND sha256 = ?", model.AssetTypeClientFile, sha).First(&asset).Error)
	return asset.ID
}
