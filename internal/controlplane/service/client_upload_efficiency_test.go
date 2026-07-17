package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// newUploadEfficiencySvc 建上传增效服务（FR-346）：内存 SQLite + 临时数据根 + 复用 PublishFile 落 CAS。
// 返回增效服务、版本服务（供比对真上传结果）、DB（供直改 last_used_at 等验证）。
func newUploadEfficiencySvc(t *testing.T) (*ClientUploadEfficiencyService, *ClientVersionService, *gorm.DB) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Asset{}))
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	assetSvc := NewAssetService(db, root)
	verSvc := NewClientVersionService(db, assetSvc, nil)
	return NewClientUploadEfficiencyService(db, verSvc), verSvc, db
}

// contentSHA256 返回内容的 sha256 十六进制（小写），即前端预查所用的查重键。
func contentSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ---- 秒传预查 ----

func TestUploadPrecheck_HitMissMixedAligned(t *testing.T) {
	svc, verSvc, _ := newUploadEfficiencySvc(t)
	a := randBytes(t, 2048)
	b := randBytes(t, 4096)

	// 先真上传两份制品（走单次 PublishFile，codec=none）。
	resA, err := verSvc.PublishFile(bytes.NewReader(a), PublishFileParams{Filename: "a.jar", Codec: "none"})
	require.NoError(t, err)
	resB, err := verSvc.PublishFile(bytes.NewReader(b), PublishFileParams{Filename: "b.jar", Codec: "none"})
	require.NoError(t, err)

	missSha := contentSHA256(randBytes(t, 8)) // 未入库内容

	out, err := svc.Precheck([]PrecheckEntry{
		{SHA256: resA.SHA256, Size: int64(len(a))},
		{SHA256: missSha, Size: 8},
		{SHA256: resB.SHA256, Size: int64(len(b))},
	})
	require.NoError(t, err)
	require.Len(t, out, 3)

	// 顺序对齐 + 命中结果与真上传逐字段一致（验收核心）。
	require.True(t, out[0].Hit)
	require.NotNil(t, out[0].Result)
	require.Equal(t, resA.SHA256, out[0].Result.SHA256)
	require.Equal(t, resA.MD5, out[0].Result.MD5)
	require.Equal(t, resA.Size, out[0].Result.Size)
	require.Equal(t, "none", out[0].Result.Codec)

	require.False(t, out[1].Hit)
	require.Nil(t, out[1].Result)
	require.Equal(t, missSha, out[1].SHA256)

	require.True(t, out[2].Hit)
	require.Equal(t, resB.MD5, out[2].Result.MD5)
}

func TestUploadPrecheck_SizeMismatchIsMiss(t *testing.T) {
	svc, verSvc, _ := newUploadEfficiencySvc(t)
	data := randBytes(t, 1024)
	res, err := verSvc.PublishFile(bytes.NewReader(data), PublishFileParams{Filename: "x.bin"})
	require.NoError(t, err)

	out, err := svc.Precheck([]PrecheckEntry{{SHA256: res.SHA256, Size: int64(len(data)) + 1}})
	require.NoError(t, err)
	require.False(t, out[0].Hit, "size 不符须按未命中处理（防前端 hash 算错文件）")
}

func TestUploadPrecheck_CaseInsensitiveSHA(t *testing.T) {
	svc, verSvc, _ := newUploadEfficiencySvc(t)
	data := randBytes(t, 512)
	res, err := verSvc.PublishFile(bytes.NewReader(data), PublishFileParams{Filename: "x.bin"})
	require.NoError(t, err)

	out, err := svc.Precheck([]PrecheckEntry{{SHA256: strings.ToUpper(res.SHA256), Size: int64(len(data))}})
	require.NoError(t, err)
	require.True(t, out[0].Hit)
	// 返回的 sha256 归一为小写（与真上传返回一致）。
	require.Equal(t, res.SHA256, out[0].SHA256)
}

func TestUploadPrecheck_BumpsLastUsedAt(t *testing.T) {
	svc, verSvc, db := newUploadEfficiencySvc(t)
	data := randBytes(t, 256)
	res, err := verSvc.PublishFile(bytes.NewReader(data), PublishFileParams{Filename: "x.bin"})
	require.NoError(t, err)

	// 人为把 last_used_at 拨回过去，预查命中后应被 bump（与真上传去重路径一致）。
	past := time.Now().Add(-48 * time.Hour)
	require.NoError(t, db.Model(&model.Asset{}).Where("sha256 = ?", res.SHA256).Update("last_used_at", &past).Error)

	out, err := svc.Precheck([]PrecheckEntry{{SHA256: res.SHA256, Size: int64(len(data))}})
	require.NoError(t, err)
	require.True(t, out[0].Hit)

	var asset model.Asset
	require.NoError(t, db.Where("sha256 = ?", res.SHA256).First(&asset).Error)
	require.NotNil(t, asset.LastUsedAt)
	require.True(t, asset.LastUsedAt.After(past.Add(time.Hour)), "命中应 bump last_used_at")
}

func TestUploadPrecheck_Validation(t *testing.T) {
	svc, _, _ := newUploadEfficiencySvc(t)

	// 空列表。
	_, err := svc.Precheck(nil)
	require.ErrorIs(t, err, ErrUploadPrecheckInvalid)

	// sha256 非法（长度不足 / 非 hex）。
	_, err = svc.Precheck([]PrecheckEntry{{SHA256: "abc", Size: 1}})
	require.ErrorIs(t, err, ErrUploadPrecheckInvalid)
	_, err = svc.Precheck([]PrecheckEntry{{SHA256: strings.Repeat("z", 64), Size: 1}})
	require.ErrorIs(t, err, ErrUploadPrecheckInvalid)

	// size 为负。
	_, err = svc.Precheck([]PrecheckEntry{{SHA256: strings.Repeat("a", 64), Size: -1}})
	require.ErrorIs(t, err, ErrUploadPrecheckInvalid)

	// 超出单次上限。
	over := make([]PrecheckEntry, precheckMaxEntries+1)
	for i := range over {
		over[i] = PrecheckEntry{SHA256: strings.Repeat("a", 64), Size: 1}
	}
	_, err = svc.Precheck(over)
	require.ErrorIs(t, err, ErrBatchLimitExceeded)
}

// ---- 小文件聚合上传 ----

func TestBatchIngest_ResultsMatchSingleShot(t *testing.T) {
	svc, verSvc, _ := newUploadEfficiencySvc(t)
	files := [][]byte{randBytes(t, 100), randBytes(t, 2048), randBytes(t, 3)}

	metas := make([]BatchFileMeta, len(files))
	for i, f := range files {
		metas[i] = BatchFileMeta{Filename: "f.bin", Size: int64(len(f)), SHA256: contentSHA256(f)}
	}
	require.NoError(t, svc.ValidateBatchMetas(metas))

	for i, f := range files {
		got, err := svc.IngestBatchFile(metas[i], bytes.NewReader(f))
		require.NoError(t, err)
		// 与真上传逐字段一致（同一 CAS，内容寻址一致）。
		want, err := verSvc.PublishFile(bytes.NewReader(f), PublishFileParams{Filename: "f.bin", Codec: "none"})
		require.NoError(t, err)
		require.Equal(t, want.SHA256, got.SHA256)
		require.Equal(t, want.MD5, got.MD5)
		require.Equal(t, want.Size, got.Size)
		require.Equal(t, "none", got.Codec)
	}
}

func TestBatchIngest_ZeroByteFile(t *testing.T) {
	svc, _, _ := newUploadEfficiencySvc(t)
	meta := BatchFileMeta{Filename: "empty.txt", Size: 0, SHA256: contentSHA256(nil)}
	require.NoError(t, svc.ValidateBatchMetas([]BatchFileMeta{meta}))

	got, err := svc.IngestBatchFile(meta, bytes.NewReader(nil))
	require.NoError(t, err)
	require.Equal(t, int64(0), got.Size)
	require.Equal(t, contentSHA256(nil), got.SHA256)
}

func TestBatchIngest_ChecksumMismatchRejected_EarlierFilesKept(t *testing.T) {
	svc, _, _ := newUploadEfficiencySvc(t)
	ok := randBytes(t, 1024)
	bad := randBytes(t, 1024)

	// 第 1 个成功入库。
	okMeta := BatchFileMeta{Filename: "ok.bin", Size: int64(len(ok)), SHA256: contentSHA256(ok)}
	_, err := svc.IngestBatchFile(okMeta, bytes.NewReader(ok))
	require.NoError(t, err)

	// 第 2 个声明 sha 与字节不符 → 拒收（fail-fast 由 handler 层中止后续）。
	badMeta := BatchFileMeta{Filename: "bad.bin", Size: int64(len(bad)), SHA256: contentSHA256(ok)}
	_, err = svc.IngestBatchFile(badMeta, bytes.NewReader(bad))
	require.ErrorIs(t, err, ErrChecksumMismatch)

	// 前序文件保留在 CAS：预查命中（重试整批即秒传）。
	out, err := svc.Precheck([]PrecheckEntry{{SHA256: okMeta.SHA256, Size: okMeta.Size}})
	require.NoError(t, err)
	require.True(t, out[0].Hit)
}

func TestBatchIngest_OversizeBodyRejected(t *testing.T) {
	svc, _, _ := newUploadEfficiencySvc(t)
	data := randBytes(t, 512)
	// 声明 256 字节但实际给 512：LimitReader 截读 → 与声明 sha（按完整 512 算）不符 → 拒收。
	meta := BatchFileMeta{Filename: "x.bin", Size: 256, SHA256: contentSHA256(data)}
	_, err := svc.IngestBatchFile(meta, bytes.NewReader(data))
	require.ErrorIs(t, err, ErrChecksumMismatch)
}

func TestBatchIngest_DuplicateContentInBatchDedups(t *testing.T) {
	svc, _, _ := newUploadEfficiencySvc(t)
	data := randBytes(t, 777)
	meta := BatchFileMeta{Filename: "dup.bin", Size: int64(len(data)), SHA256: contentSHA256(data)}

	first, err := svc.IngestBatchFile(meta, bytes.NewReader(data))
	require.NoError(t, err)
	second, err := svc.IngestBatchFile(meta, bytes.NewReader(data))
	require.NoError(t, err)
	// 同内容第二次为 CAS 去重命中，结果一致。
	require.Equal(t, first.SHA256, second.SHA256)
	require.Equal(t, first.MD5, second.MD5)
	require.Equal(t, first.Size, second.Size)
}

func TestValidateBatchMetas_Limits(t *testing.T) {
	svc, _, _ := newUploadEfficiencySvc(t)
	validSha := strings.Repeat("a", 64)

	// 空批。
	require.ErrorIs(t, svc.ValidateBatchMetas(nil), ErrBatchInvalid)

	// 超文件数。
	over := make([]BatchFileMeta, batchMaxFiles+1)
	for i := range over {
		over[i] = BatchFileMeta{Filename: "f", Size: 1, SHA256: validSha}
	}
	require.ErrorIs(t, svc.ValidateBatchMetas(over), ErrBatchLimitExceeded)

	// 单文件超上限。
	require.ErrorIs(t, svc.ValidateBatchMetas([]BatchFileMeta{
		{Filename: "big", Size: batchFileMaxBytes + 1, SHA256: validSha},
	}), ErrBatchLimitExceeded)

	// 总字节超上限（5 个 8 MiB = 40 MiB > 32 MiB，单个均不超限）。
	var metas []BatchFileMeta
	for i := 0; i < 5; i++ {
		metas = append(metas, BatchFileMeta{Filename: "f", Size: batchFileMaxBytes, SHA256: validSha})
	}
	require.ErrorIs(t, svc.ValidateBatchMetas(metas), ErrBatchLimitExceeded)

	// sha 非法 / size 为负。
	require.ErrorIs(t, svc.ValidateBatchMetas([]BatchFileMeta{{Filename: "f", Size: 1, SHA256: "bad"}}), ErrBatchInvalid)
	require.ErrorIs(t, svc.ValidateBatchMetas([]BatchFileMeta{{Filename: "f", Size: -1, SHA256: validSha}}), ErrBatchInvalid)
}
