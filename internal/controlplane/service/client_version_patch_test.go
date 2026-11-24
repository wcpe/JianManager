package service

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
	// 关闭底层连接：共享内存库在最后一个连接关闭后才释放。不关就长期占着句柄，
	// 整包跑时累积数百个未释放的库，后续用例的 AutoMigrate（要写锁）会长时间等锁
	// ——表现为「单个用例秒过、整包随机卡住」，且每次卡住的用例都不一样。
	if sqlDB, derr := db.DB(); derr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
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

// stubBrokenZstdOnPath 在 PATH 最前放一个必然失败的 zstd 桩,
// 强制 buildPatchFile 的 CLI 路径失败、落到内存 fallback——
// 否则宿主 zstd 可用时测试根本没走到 fallback,回退修复点无从验证。
// (Windows 上 .bat 桩被 Go exec 以 ErrBatch 拒绝,同样得到 err,语义一致。)
func stubBrokenZstdOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	name, body := "zstd", "#!/bin/sh\nexit 1\n"
	if runtime.GOOS == "windows" {
		name, body = "zstd.bat", "@exit /b 1\r\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestClientVersion_PublishGeneratesPatchViaFallbackWhenZstdFails(t *testing.T) {
	stubBrokenZstdOnPath(t)
	versionSvc, _, _ := newClientVersionPatchFixture(t)
	// 三个文件同版发布,worker 并发均走 fallback——覆盖信号量串行化
	// (单文件用例只有一个 worker,验证不到 patchInMemoryFallbackSem 的互斥)。
	var oldFiles, newFiles []ManifestFile
	oldContents := [][]byte{
		[]byte("fallback pack A v1 -- shared payload -- shared payload"),
		[]byte("fallback pack B v1 -- shared payload -- shared payload"),
		[]byte("fallback pack C v1 -- shared payload -- shared payload"),
	}
	newContents := [][]byte{
		[]byte("fallback pack A v2 -- shared payload -- shared payload"),
		[]byte("fallback pack B v2 -- shared payload -- shared payload"),
		[]byte("fallback pack C v2 -- shared payload -- shared payload"),
	}
	for i := range oldContents {
		path := fmt.Sprintf("mods/pack-%c.bin", 'A'+i)
		oldFiles = append(oldFiles, publishPatchTestFileNamed(t, versionSvc, oldContents[i], path))
		newFiles = append(newFiles, publishPatchTestFileNamed(t, versionSvc, newContents[i], path))
	}
	_, err := versionSvc.PublishVersion("skyblock-s1", PublishVersionParams{
		Files:       oldFiles,
		ManagedDirs: []string{"mods"},
	})
	require.NoError(t, err)

	_, err = versionSvc.PublishVersion("skyblock-s1", PublishVersionParams{
		Files:       newFiles,
		ManagedDirs: []string{"mods"},
	})
	require.NoError(t, err)

	manifest, err := versionSvc.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.Len(t, manifest.Files, len(newFiles))
	for i, file := range manifest.Files {
		patch := file.Patch
		require.NotNil(t, patch, "zstd CLI 失败时应回退内存编码仍产出 patch")
		_, patchPath, err := versionSvc.OpenArtifact(patch.Artifact.SHA256)
		require.NoError(t, err)
		dec, err := zstd.NewReader(nil, zstd.WithDecoderDictRaw(0, oldContents[i]))
		require.NoError(t, err)
		decoded, err := dec.DecodeAll(mustReadFile(t, patchPath), nil)
		require.NoError(t, err)
		dec.Close()
		require.Equal(t, newContents[i], decoded)
	}
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
