package service

import (
	"bytes"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// publishPatchTestFileNamed 上传一份制品并构造 manifest 条目（路径可指定）。
func publishPatchTestFileNamed(t *testing.T, svc *ClientVersionService, content []byte, path string) ManifestFile {
	t.Helper()
	asset, err := svc.PublishFile(bytes.NewReader(content), PublishFileParams{Filename: path, Codec: "none"})
	require.NoError(t, err)
	return ManifestFile{
		Path:     path,
		SHA256:   sha256hex(content),
		MD5:      md5hex(content),
		Size:     int64(len(content)),
		Sync:     "strict",
		Artifact: ManifestArtifact{SHA256: asset.SHA256, Size: asset.Size, Codec: asset.Codec},
	}
}

// 回归：发布「过多文件」的长清单（回归 INTERNAL_ERROR，见 2026-09 客户端分发发布排障）。
// 覆盖点：2000 文件一次发布成功；同一频道二次发布（触发 patch 构建）同样成功。
func TestClientVersion_PublishManyFilesScale(t *testing.T) {
	versionSvc, _, _ := newClientVersionPatchFixture(t)

	publish := func(tag string) {
		files := make([]ManifestFile, 0, 2000)
		for i := 0; i < 2000; i++ {
			content := []byte(tag + "-" + strconv.Itoa(i) + "-payloadpayloadpayload")
			files = append(files, publishPatchTestFileNamed(t, versionSvc, content, "mods/f"+strconv.Itoa(i)+".bin"))
		}
		_, err := versionSvc.PublishVersion("skyblock-s1", PublishVersionParams{
			Files:       files,
			ManagedDirs: []string{"mods"},
		})
		require.NoError(t, err)
	}
	publish("v1")
	publish("v2")
}

// 回归：patch 总预算耗尽时发布仍成功（剩余文件无 patch 降级），manifest 完整可用。
func TestClientVersion_PatchBudgetExceededSkipsPatches(t *testing.T) {
	versionSvc, _, _ := newClientVersionPatchFixture(t)
	versionSvc.SetPatchBudget(1) // 1ns：所有 patch 都不再启动

	publish := func(tag string) {
		files := make([]ManifestFile, 0, 50)
		for i := 0; i < 50; i++ {
			content := []byte(fmt.Sprintf("%s-%d", tag, i))
			files = append(files, publishPatchTestFileNamed(t, versionSvc, content, "mods/f"+strconv.Itoa(i)+".bin"))
		}
		_, err := versionSvc.PublishVersion("skyblock-s1", PublishVersionParams{Files: files})
		require.NoError(t, err, "预算耗尽不得让发布失败")
	}
	publish("v1")
	publish("v2-CHANGED")

	manifest, err := versionSvc.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.Len(t, manifest.Files, 50)
	// 预算 1ns 由 AfterFunc 强制停喂：至多 workers 个在途 job 漏过（4），其余必须全部跳过。
	patched := 0
	for _, f := range manifest.Files {
		if f.Patch != nil {
			patched++
		}
	}
	require.LessOrEqual(t, patched, 4, "预算耗尽后几乎全部文件应无 patch")
}

// 回归：单个文件 patch 构建失败（坏 zstd 制品）只跳过该文件补丁，发布整体成功。
// 修复前该错误会冒泡成 500 INTERNAL_ERROR 使整个发布报废。
func TestClientVersion_PatchBuildFailureSkipsFile(t *testing.T) {
	versionSvc, _, _ := newClientVersionPatchFixture(t)

	// v1：正常内容。
	oldFile := publishPatchTestFileNamed(t, versionSvc, []byte("v1 content"), "mods/corrupt.bin")
	_, err := versionSvc.PublishVersion("skyblock-s1", PublishVersionParams{Files: []ManifestFile{oldFile}})
	require.NoError(t, err)

	// v2：声明 codec=zstd 但内容不是合法 zstd 流——patch 物化解码失败（修复前 → 发布 500）。
	garbage := []byte("this-is-not-a-valid-zstd-stream")
	asset, err := versionSvc.PublishFile(bytes.NewReader(garbage), PublishFileParams{Filename: "corrupt.bin", Codec: "none"})
	require.NoError(t, err)
	newFile := ManifestFile{
		Path:     "mods/corrupt.bin",
		SHA256:   sha256hex(garbage),
		MD5:      md5hex(garbage),
		Size:     int64(len(garbage)),
		Sync:     "strict",
		Artifact: ManifestArtifact{SHA256: asset.SHA256, Size: asset.Size, Codec: "zstd"},
	}
	_, err = versionSvc.PublishVersion("skyblock-s1", PublishVersionParams{Files: []ManifestFile{newFile}})
	require.NoError(t, err, "patch 构建失败必须降级跳过而不是 500")

	manifest, err := versionSvc.BuildManifest("skyblock-s1")
	require.NoError(t, err)
	require.Len(t, manifest.Files, 1)
	require.Nil(t, manifest.Files[0].Patch, "构建失败的文件不应带 patch")
	require.Equal(t, newFile.Artifact.SHA256, manifest.Files[0].Artifact.SHA256)
}
