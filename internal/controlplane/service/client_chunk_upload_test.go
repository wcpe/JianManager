package service

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// 测试用分片大小 = 生产允许的最小分片（1 MiB）。用它而非任意小值，因服务把 <minChunkSize
// 的请求夹取上来（生产护栏）；测试须落在合法区间才验得到真实 chunkCount 行为。
const testChunk = minChunkSize

// newChunkedUploadSvc 建一个分块上传服务，底座为内存 SQLite + 临时数据根 + 复用 PublishFile 落 CAS。
// 返回服务、版本服务（供直接比对单次上传）、数据根。
func newChunkedUploadSvc(t *testing.T) (*ChunkedUploadService, *ClientVersionService, *dataroot.Root) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Asset{}))
	root, err := dataroot.Init(filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	assetSvc := NewAssetService(db, root)
	// PublishFile 只触及制品库（Ingest），channel 可为 nil。
	verSvc := NewClientVersionService(db, assetSvc, nil)
	return NewChunkedUploadService(root, verSvc), verSvc, root
}

// randBytes 生成 n 字节随机数据（非重复内容，错序拼装即 sha 不同，能验拼装顺序正确）。
func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

// uploadWhole 走完整 init→chunk→complete 上传 data，返回结果。分片按敲定 chunkSize 顺序传。
func uploadWhole(t *testing.T, svc *ChunkedUploadService, channelID, filename string, data []byte, chunkSize int64) *ClientFileResult {
	t.Helper()
	init, err := svc.Init(channelID, InitParams{Filename: filename, TotalSize: int64(len(data)), ChunkSize: chunkSize})
	require.NoError(t, err)
	for i := int64(0); i < init.ChunkCount; i++ {
		start := i * init.ChunkSize
		end := start + init.ChunkSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		_, werr := svc.WriteChunk(channelID, init.UploadID, i, bytes.NewReader(data[start:end]))
		require.NoError(t, werr)
	}
	res, err := svc.Complete(channelID, init.UploadID, CompleteParams{Codec: "none"})
	require.NoError(t, err)
	return res
}

func TestChunkedUpload_ResultMatchesSingleShot(t *testing.T) {
	svc, verSvc, _ := newChunkedUploadSvc(t)
	// 2.5 片：跨多片 + 末片余量。
	data := randBytes(t, int(testChunk*2+testChunk/2))

	chunked := uploadWhole(t, svc, "skyblock-s1", "pack.zip", data, testChunk)

	// 同一文件走单次 PublishFile。
	single, err := verSvc.PublishFile(bytes.NewReader(data), PublishFileParams{Filename: "pack.zip", Codec: "none"})
	require.NoError(t, err)

	// 验收核心：逐字段一致（同一 CAS、内容寻址一致）。
	require.Equal(t, single.SHA256, chunked.SHA256)
	require.Equal(t, single.MD5, chunked.MD5)
	require.Equal(t, single.Size, chunked.Size)
	require.Equal(t, single.Codec, chunked.Codec)
	require.Equal(t, int64(len(data)), chunked.Size)
}

func TestChunkedUpload_SingleChunkExactSize(t *testing.T) {
	svc, verSvc, _ := newChunkedUploadSvc(t)
	data := randBytes(t, int(testChunk)) // 恰为一整片

	init, err := svc.Init("ch", InitParams{Filename: "a.bin", TotalSize: int64(len(data)), ChunkSize: testChunk})
	require.NoError(t, err)
	require.Equal(t, int64(1), init.ChunkCount)

	res, err := svc.WriteChunk("ch", init.UploadID, 0, bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, int64(1), res.Received)

	out, err := svc.Complete("ch", init.UploadID, CompleteParams{})
	require.NoError(t, err)
	single, err := verSvc.PublishFile(bytes.NewReader(data), PublishFileParams{Filename: "a.bin"})
	require.NoError(t, err)
	require.Equal(t, single.SHA256, out.SHA256)
}

func TestChunkedUpload_OutOfOrderChunks(t *testing.T) {
	svc, verSvc, _ := newChunkedUploadSvc(t)
	data := randBytes(t, int(testChunk*3+123)) // 4 片（3 整 + 极小末片）

	init, err := svc.Init("ch", InitParams{TotalSize: int64(len(data)), ChunkSize: testChunk})
	require.NoError(t, err)
	require.Equal(t, int64(4), init.ChunkCount)

	// 乱序：3,1,0,2。
	order := []int64{3, 1, 0, 2}
	for _, i := range order {
		start := i * testChunk
		end := start + testChunk
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		_, werr := svc.WriteChunk("ch", init.UploadID, i, bytes.NewReader(data[start:end]))
		require.NoError(t, werr)
	}
	out, err := svc.Complete("ch", init.UploadID, CompleteParams{})
	require.NoError(t, err)
	single, err := verSvc.PublishFile(bytes.NewReader(data), PublishFileParams{})
	require.NoError(t, err)
	require.Equal(t, single.SHA256, out.SHA256)
}

func TestChunkedUpload_IdempotentRetransmit(t *testing.T) {
	svc, verSvc, _ := newChunkedUploadSvc(t)
	data := randBytes(t, int(testChunk*2+10)) // 3 片

	init, err := svc.Init("ch", InitParams{TotalSize: int64(len(data)), ChunkSize: testChunk})
	require.NoError(t, err)
	// 传片 0 两次（模拟重试）——received 不应重复计数。
	seg := data[0:testChunk]
	r1, err := svc.WriteChunk("ch", init.UploadID, 0, bytes.NewReader(seg))
	require.NoError(t, err)
	require.Equal(t, int64(1), r1.Received)
	r2, err := svc.WriteChunk("ch", init.UploadID, 0, bytes.NewReader(seg))
	require.NoError(t, err)
	require.Equal(t, int64(1), r2.Received) // 幂等：仍是 1

	// 补齐其余片。
	for i := int64(1); i < init.ChunkCount; i++ {
		start := i * testChunk
		end := start + testChunk
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		_, werr := svc.WriteChunk("ch", init.UploadID, i, bytes.NewReader(data[start:end]))
		require.NoError(t, werr)
	}
	out, err := svc.Complete("ch", init.UploadID, CompleteParams{})
	require.NoError(t, err)
	single, err := verSvc.PublishFile(bytes.NewReader(data), PublishFileParams{})
	require.NoError(t, err)
	require.Equal(t, single.SHA256, out.SHA256)
}

func TestChunkedUpload_MissingChunkRejected(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	data := randBytes(t, int(testChunk*2+50)) // 3 片
	init, err := svc.Init("ch", InitParams{TotalSize: int64(len(data)), ChunkSize: testChunk})
	require.NoError(t, err)
	require.Equal(t, int64(3), init.ChunkCount)

	// 只传片 0、1，缺片 2。
	for i := int64(0); i < 2; i++ {
		start := i * testChunk
		_, werr := svc.WriteChunk("ch", init.UploadID, i, bytes.NewReader(data[start:start+testChunk]))
		require.NoError(t, werr)
	}
	_, err = svc.Complete("ch", init.UploadID, CompleteParams{})
	require.ErrorIs(t, err, ErrUploadIncomplete)
}

func TestChunkedUpload_WrongChunkSizeRejected(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	data := randBytes(t, int(testChunk*2+50)) // 3 片
	init, err := svc.Init("ch", InitParams{TotalSize: int64(len(data)), ChunkSize: testChunk})
	require.NoError(t, err)

	// 片 0 非末片，须恰为 chunkSize；传少了 → 拒。
	_, err = svc.WriteChunk("ch", init.UploadID, 0, bytes.NewReader(data[0:100]))
	require.ErrorIs(t, err, ErrInvalidChunkSize)

	// 传多了（超 chunkSize）→ 也拒。
	tooBig := randBytes(t, int(testChunk)+10)
	_, err = svc.WriteChunk("ch", init.UploadID, 0, bytes.NewReader(tooBig))
	require.ErrorIs(t, err, ErrInvalidChunkSize)
}

func TestChunkedUpload_InvalidIndexRejected(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	data := randBytes(t, int(testChunk*2)) // 2 片
	init, err := svc.Init("ch", InitParams{TotalSize: int64(len(data)), ChunkSize: testChunk})
	require.NoError(t, err)
	require.Equal(t, int64(2), init.ChunkCount)

	seg := data[0:testChunk]
	// index = chunkCount 越界。
	_, err = svc.WriteChunk("ch", init.UploadID, 2, bytes.NewReader(seg))
	require.ErrorIs(t, err, ErrInvalidChunkIndex)
	// 负 index 越界。
	_, err = svc.WriteChunk("ch", init.UploadID, -1, bytes.NewReader(seg))
	require.ErrorIs(t, err, ErrInvalidChunkIndex)
}

func TestChunkedUpload_ChannelMismatchRejected(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	init, err := svc.Init("chan-a", InitParams{TotalSize: testChunk, ChunkSize: testChunk})
	require.NoError(t, err)
	seg := randBytes(t, int(testChunk))
	// 用别的频道操作会话 → 拒（防越权）。
	_, err = svc.WriteChunk("chan-b", init.UploadID, 0, bytes.NewReader(seg))
	require.ErrorIs(t, err, ErrUploadChannelMismatch)
	_, err = svc.Complete("chan-b", init.UploadID, CompleteParams{})
	require.ErrorIs(t, err, ErrUploadChannelMismatch)
	require.ErrorIs(t, svc.Abort("chan-b", init.UploadID), ErrUploadChannelMismatch)
}

func TestChunkedUpload_InvalidInitRejected(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	// totalSize=0 合法（空文件，见 TestChunkedUpload_ZeroByteFile）；仅负数拒绝。
	_, err := svc.Init("ch", InitParams{TotalSize: -5})
	require.ErrorIs(t, err, ErrInvalidUploadInit)
}

// 空内容的标准摘要（RFC 6234 / RFC 1321 已知值），验证空文件 CAS 入库内容寻址正确。
const (
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	emptyMD5    = "d41d8cd98f00b204e9800998ecf8427e"
)

// 整合包常见 0 字节文件（.gitkeep / 空配置）：init(totalSize=0) 须成功且 chunkCount=0，
// 无片可传（任何 index 越界），complete 直接拼空内容喂 CAS，结果与单次上传空内容一致。
func TestChunkedUpload_ZeroByteFile(t *testing.T) {
	svc, verSvc, _ := newChunkedUploadSvc(t)

	init, err := svc.Init("ch", InitParams{Filename: ".gitkeep", TotalSize: 0, ChunkSize: testChunk})
	require.NoError(t, err)
	require.Equal(t, int64(0), init.ChunkCount)

	// 0 片会话不接受任何分片写入。
	_, err = svc.WriteChunk("ch", init.UploadID, 0, bytes.NewReader(nil))
	require.ErrorIs(t, err, ErrInvalidChunkIndex)

	tempDir := filepath.Join(svc.uploadsRoot(), init.UploadID)
	require.DirExists(t, tempDir)

	out, err := svc.Complete("ch", init.UploadID, CompleteParams{Codec: "none"})
	require.NoError(t, err)
	require.Equal(t, int64(0), out.Size)
	require.Equal(t, emptySHA256, out.SHA256)
	require.Equal(t, emptyMD5, out.MD5)
	require.Equal(t, "none", out.Codec)

	// 与单次 PublishFile 空内容逐字段一致（同 CAS 去重）。
	single, err := verSvc.PublishFile(bytes.NewReader(nil), PublishFileParams{Filename: ".gitkeep"})
	require.NoError(t, err)
	require.Equal(t, single.SHA256, out.SHA256)
	require.Equal(t, single.MD5, out.MD5)
	require.Equal(t, single.Size, out.Size)

	// complete 后清临时目录 + 会话移除（与非空路径一致）。
	require.NoDirExists(t, tempDir)
	_, err = svc.Complete("ch", init.UploadID, CompleteParams{})
	require.ErrorIs(t, err, ErrUploadNotFound)
}

// 空文件 + 期望校验和：ExpectedSHA256 为空内容 sha256 时 complete 应通过（Ingest 比对路径不豁免空流）。
func TestChunkedUpload_ZeroByteFileExpectedSha256(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	init, err := svc.Init("ch", InitParams{Filename: "empty.cfg", TotalSize: 0})
	require.NoError(t, err)
	out, err := svc.Complete("ch", init.UploadID, CompleteParams{ExpectedSHA256: emptySHA256})
	require.NoError(t, err)
	require.Equal(t, emptySHA256, out.SHA256)
}

func TestChunkedUpload_ChunkSizeClampAndCount(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)

	// chunkSize<=0 → 默认 8MiB。
	init, err := svc.Init("ch", InitParams{TotalSize: 100, ChunkSize: 0})
	require.NoError(t, err)
	require.Equal(t, defaultChunkSize, init.ChunkSize)
	require.Equal(t, int64(1), init.ChunkCount)

	// 过小 → 夹到 minChunkSize（chunkCount 据夹后值算）。
	init2, err := svc.Init("ch", InitParams{TotalSize: minChunkSize * 3, ChunkSize: 1})
	require.NoError(t, err)
	require.Equal(t, minChunkSize, init2.ChunkSize)
	require.Equal(t, int64(3), init2.ChunkCount)

	// 过大 → 夹到 maxChunkSize。
	init3, err := svc.Init("ch", InitParams{TotalSize: 100, ChunkSize: maxChunkSize * 10})
	require.NoError(t, err)
	require.Equal(t, maxChunkSize, init3.ChunkSize)
}

func TestChunkedUpload_AbortCleansTempDir(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	init, err := svc.Init("ch", InitParams{TotalSize: testChunk * 2, ChunkSize: testChunk})
	require.NoError(t, err)
	_, err = svc.WriteChunk("ch", init.UploadID, 0, bytes.NewReader(randBytes(t, int(testChunk))))
	require.NoError(t, err)

	tempDir := filepath.Join(svc.uploadsRoot(), init.UploadID)
	require.DirExists(t, tempDir)

	require.NoError(t, svc.Abort("ch", init.UploadID))
	require.NoDirExists(t, tempDir)
	// 弃单后再操作 → 会话不存在。
	require.ErrorIs(t, svc.Abort("ch", init.UploadID), ErrUploadNotFound)
	_, err = svc.Complete("ch", init.UploadID, CompleteParams{})
	require.ErrorIs(t, err, ErrUploadNotFound)
}

func TestChunkedUpload_CompleteCleansTempDir(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	data := randBytes(t, int(testChunk*2+7))
	init, err := svc.Init("ch", InitParams{TotalSize: int64(len(data)), ChunkSize: testChunk})
	require.NoError(t, err)
	tempDir := filepath.Join(svc.uploadsRoot(), init.UploadID)

	for i := int64(0); i < init.ChunkCount; i++ {
		start := i * init.ChunkSize
		end := start + init.ChunkSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		_, werr := svc.WriteChunk("ch", init.UploadID, i, bytes.NewReader(data[start:end]))
		require.NoError(t, werr)
	}
	_, err = svc.Complete("ch", init.UploadID, CompleteParams{})
	require.NoError(t, err)
	// complete 后临时目录清空 + 会话移除。
	require.NoDirExists(t, tempDir)
	_, err = svc.Complete("ch", init.UploadID, CompleteParams{})
	require.ErrorIs(t, err, ErrUploadNotFound)
}

func TestChunkedUpload_TTLSweep(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	// 注入可控时间源。
	base := time.Now()
	svc.now = func() time.Time { return base }

	init, err := svc.Init("ch", InitParams{TotalSize: testChunk * 2, ChunkSize: testChunk})
	require.NoError(t, err)
	tempDir := filepath.Join(svc.uploadsRoot(), init.UploadID)
	require.DirExists(t, tempDir)

	// 未过 TTL：不清。
	require.Equal(t, 0, svc.SweepExpired())
	require.DirExists(t, tempDir)

	// 推进超 TTL：清理会话 + 临时目录。
	svc.now = func() time.Time { return base.Add(uploadSessionTTL + time.Minute) }
	require.Equal(t, 1, svc.SweepExpired())
	require.NoDirExists(t, tempDir)
	_, err = svc.Complete("ch", init.UploadID, CompleteParams{})
	require.ErrorIs(t, err, ErrUploadNotFound)
}

func TestChunkedUpload_StartClearsResidue(t *testing.T) {
	svc, _, root := newChunkedUploadSvc(t)
	// 模拟上次进程残留的无主分片目录。
	residue := filepath.Join(root.CacheDir(), "client-uploads", "stale-session")
	require.NoError(t, os.MkdirAll(residue, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(residue, "0.part"), []byte("orphan"), 0o644))

	svc.Start()
	defer svc.Stop()
	// 启动即清空整个 client-uploads/。
	require.NoDirExists(t, residue)
}

func TestChunkedUpload_ExpectedSha256Mismatch(t *testing.T) {
	svc, _, _ := newChunkedUploadSvc(t)
	data := randBytes(t, int(testChunk+500))
	init, err := svc.Init("ch", InitParams{TotalSize: int64(len(data)), ChunkSize: testChunk})
	require.NoError(t, err)
	for i := int64(0); i < init.ChunkCount; i++ {
		start := i * init.ChunkSize
		end := start + init.ChunkSize
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		_, werr := svc.WriteChunk("ch", init.UploadID, i, bytes.NewReader(data[start:end]))
		require.NoError(t, werr)
	}
	// 期望 sha256 与实际不符 → Complete 报 ErrChecksumMismatch，且会话保留供重试。
	_, err = svc.Complete("ch", init.UploadID, CompleteParams{ExpectedSHA256: "00"})
	require.ErrorIs(t, err, ErrChecksumMismatch)

	// 会话仍在：正确的 complete 应成功。
	res, err := svc.Complete("ch", init.UploadID, CompleteParams{ExpectedSHA256: sha256hex(data)})
	require.NoError(t, err)
	require.Equal(t, sha256hex(data), res.SHA256)
}

func TestChunkedUpload_LargeRandomMultiChunk(t *testing.T) {
	svc, verSvc, _ := newChunkedUploadSvc(t)
	// 随机内容确保拼装顺序正确（非重复字节，错序即 sha 不同）。
	data := randBytes(t, int(testChunk*3+12345)) // 4 片 + 零头

	chunked := uploadWhole(t, svc, "ch", "big.bin", data, testChunk)
	single, err := verSvc.PublishFile(bytes.NewReader(data), PublishFileParams{Filename: "big.bin"})
	require.NoError(t, err)
	require.Equal(t, single.SHA256, chunked.SHA256)
	require.Equal(t, int64(len(data)), chunked.Size)
}

func TestUploadSession_ReceivedIndexesSorted(t *testing.T) {
	sess := &uploadSession{received: map[int64]struct{}{3: {}, 0: {}, 2: {}}}
	require.Equal(t, []int64{0, 2, 3}, sess.receivedIndexes())
}
