package service

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wxys233/JianManager/internal/controlplane/model"
	"github.com/wxys233/JianManager/internal/platform/dataroot"
)

func newAssetSvc(t *testing.T) (*AssetService, *dataroot.Root) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Asset{}))
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	return NewAssetService(db, root), root
}

func sha256hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func md5hex(b []byte) string    { s := md5.Sum(b); return hex.EncodeToString(s[:]) }

func TestIngest_HashingAndCASLayout(t *testing.T) {
	svc, root := newAssetSvc(t)
	data := []byte("paper-core-bytes")

	asset, err := svc.Ingest(strings.NewReader(string(data)), IngestParams{
		Type:     model.AssetTypeCore,
		Name:     "paper-1.20.4",
		Filename: "paper.jar",
	})
	require.NoError(t, err)

	wantSHA := sha256hex(data)
	require.Equal(t, wantSHA, asset.SHA256)
	require.Equal(t, md5hex(data), asset.MD5)
	require.Equal(t, int64(len(data)), asset.Size)
	require.Equal(t, model.AssetStorageHot, asset.StorageState)
	require.Equal(t, model.AssetBackendLocal, asset.StorageBackend)

	// CAS 布局：var/artifacts/<type>/<sha[:2]>/<sha>.jar，且按相对路径登记。
	wantRel := "var/artifacts/core/" + wantSHA[:2] + "/" + wantSHA + ".jar"
	require.Equal(t, wantRel, asset.RelPath)
	require.False(t, filepath.IsAbs(asset.RelPath))

	// 物理文件存在且内容一致。
	abs := svc.AbsPath(asset)
	require.Equal(t, root.Abs(wantRel), abs)
	got, err := os.ReadFile(abs)
	require.NoError(t, err)
	require.Equal(t, data, got)
}

func TestIngest_DedupReusesAndBumpsLastUsed(t *testing.T) {
	svc, _ := newAssetSvc(t)
	data := []byte("same-content")

	a1, err := svc.Ingest(strings.NewReader(string(data)), IngestParams{Type: model.AssetTypeCore, Filename: "a.jar"})
	require.NoError(t, err)
	a2, err := svc.Ingest(strings.NewReader(string(data)), IngestParams{Type: model.AssetTypeCore, Filename: "b.jar"})
	require.NoError(t, err)

	// 同 (type, sha256) → 复用同一条记录（同 ID），不新增。
	require.Equal(t, a1.ID, a2.ID)
	var count int64
	require.NoError(t, svc.db.Model(&model.Asset{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.NotNil(t, a2.LastUsedAt)
}

func TestIngest_DifferentTypeNotDeduped(t *testing.T) {
	svc, _ := newAssetSvc(t)
	data := []byte("cross-type")

	a1, err := svc.Ingest(strings.NewReader(string(data)), IngestParams{Type: model.AssetTypeCore, Filename: "x.jar"})
	require.NoError(t, err)
	a2, err := svc.Ingest(strings.NewReader(string(data)), IngestParams{Type: model.AssetTypePlugin, Filename: "x.jar"})
	require.NoError(t, err)

	// 同内容、不同类型 → 两条记录，物理分目录。
	require.NotEqual(t, a1.ID, a2.ID)
	require.Equal(t, a1.SHA256, a2.SHA256)
	require.True(t, strings.HasPrefix(a1.RelPath, "var/artifacts/core/"))
	require.True(t, strings.HasPrefix(a2.RelPath, "var/artifacts/plugin/"))
}

func TestIngest_ChecksumVerification(t *testing.T) {
	data := []byte("verify-me")
	good := sha256hex(data)

	cases := []struct {
		name    string
		params  IngestParams
		wantErr bool
	}{
		{"matching sha256 accepted", IngestParams{Type: model.AssetTypeBlob, ExpectedSHA256: good}, false},
		{"mismatching sha256 rejected", IngestParams{Type: model.AssetTypeBlob, ExpectedSHA256: strings.Repeat("0", 64)}, true},
		{"matching md5 accepted", IngestParams{Type: model.AssetTypeBlob, ExpectedMD5: md5hex(data)}, false},
		{"mismatching md5 rejected", IngestParams{Type: model.AssetTypeBlob, ExpectedMD5: strings.Repeat("f", 32)}, true},
		{"uppercase sha256 accepted", IngestParams{Type: model.AssetTypeBlob, ExpectedSHA256: strings.ToUpper(good)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _ := newAssetSvc(t)
			_, err := svc.Ingest(strings.NewReader(string(data)), c.params)
			if c.wantErr {
				require.ErrorIs(t, err, ErrChecksumMismatch)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestIngest_RejectsBadType(t *testing.T) {
	svc, _ := newAssetSvc(t)
	_, err := svc.Ingest(strings.NewReader("x"), IngestParams{Type: model.AssetType("malware")})
	require.ErrorIs(t, err, ErrInvalidAssetType)
}

func TestIngestFromPath(t *testing.T) {
	svc, _ := newAssetSvc(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "plugin.jar")
	require.NoError(t, os.WriteFile(src, []byte("plugin-bytes"), 0o644))

	asset, err := svc.IngestFromPath(src, IngestParams{Type: model.AssetTypePlugin})
	require.NoError(t, err)
	require.Equal(t, "plugin.jar", asset.Filename)
	require.Equal(t, sha256hex([]byte("plugin-bytes")), asset.SHA256)
}

func TestList_FilterAndPaginate(t *testing.T) {
	svc, _ := newAssetSvc(t)
	for i, b := range [][]byte{[]byte("c1"), []byte("c2"), []byte("p1")} {
		typ := model.AssetTypeCore
		if i == 2 {
			typ = model.AssetTypePlugin
		}
		_, err := svc.Ingest(strings.NewReader(string(b)), IngestParams{Type: typ, Filename: "f.jar"})
		require.NoError(t, err)
	}

	cores, total, err := svc.List(model.AssetTypeCore, 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, cores, 2)

	all, totalAll, err := svc.List("", 1, 10)
	require.NoError(t, err)
	require.Equal(t, int64(3), totalAll)
	require.Len(t, all, 3)

	// 分页：每页 2，第 2 页应剩 1 条。
	page2, _, err := svc.List("", 2, 2)
	require.NoError(t, err)
	require.Len(t, page2, 1)

	// 非法类型过滤被拒。
	_, _, err = svc.List(model.AssetType("nope"), 1, 10)
	require.ErrorIs(t, err, ErrInvalidAssetType)
}

func TestDelete_RefProtection(t *testing.T) {
	svc, _ := newAssetSvc(t)
	asset, err := svc.Ingest(strings.NewReader("referenced"), IngestParams{Type: model.AssetTypeCore, Filename: "core.jar"})
	require.NoError(t, err)

	// 模拟被引用。
	require.NoError(t, svc.db.Model(&model.Asset{}).Where("id = ?", asset.ID).Update("ref_count", 1).Error)
	err = svc.Delete(asset.ID)
	require.ErrorIs(t, err, ErrAssetInUse)

	// 物理文件仍在。
	require.FileExists(t, svc.AbsPath(asset))

	// 解除引用后可删除，物理文件随之清除。
	require.NoError(t, svc.db.Model(&model.Asset{}).Where("id = ?", asset.ID).Update("ref_count", 0).Error)
	abs := svc.AbsPath(asset)
	require.NoError(t, svc.Delete(asset.ID))
	_, err = svc.GetByID(asset.ID)
	require.ErrorIs(t, err, ErrAssetNotFound)
	require.NoFileExists(t, abs)
}

func TestDelete_NotFound(t *testing.T) {
	svc, _ := newAssetSvc(t)
	require.ErrorIs(t, svc.Delete(9999), ErrAssetNotFound)
}
