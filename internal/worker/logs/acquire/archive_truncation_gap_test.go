package acquire

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// visibleArchiveGap 判定台账是否留下了可见的归档读/损坏痕迹（FR-474 §5#1「截断可见」）。
func visibleArchiveGap(ent *ledger.Entry) bool {
	if ent == nil {
		return false
	}
	for _, g := range ent.Gaps {
		if g.Reason == "ARCHIVE_READ_ERROR" || g.Reason == "ARCHIVE_GZIP_CORRUPT" {
			return true
		}
	}
	return false
}

// gzipAtFlushBoundary 返回「写到 Flush 边界」的 gz 完整字节与边界长度。
//
// 边界长度是**确定性**截断点：Flush 之后的字节正好构成第一块，故 raw[:firstBlock]
// 在任何 Go 版本与平台上都必然解出完整首行、再以零残余字节报错——这正是
// archive.go 读循环里「有错但 len(line)==0」的分支。
func gzipAtFlushBoundary(t *testing.T) (raw []byte, firstBlock int) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte("line-1\n"))
	require.NoError(t, err)
	require.NoError(t, zw.Flush())
	firstBlock = buf.Len()
	_, err = zw.Write([]byte("line-2\nline-3\n"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes(), firstBlock
}

// FR-474 §5#1：截断归档必须留下可见缺口。
//
// 本用例锁住的是「读错误但零残余字节」这条分支：它原先直接 break，绕过了记录缺口的路径，
// 于是截断归档静默丢尾。是否命中该分支取决于压缩布局（截断点落在行边界还是行中），
// 而 gzip 输出长度随 Go 版本/平台变化——Linux CI 长期为绿、Windows（不同输出长度）暴露了它。
func TestArchiveTruncationAtFlushBoundaryRecordsGap(t *testing.T) {
	raw, firstBlock := gzipAtFlushBoundary(t)
	require.Greater(t, firstBlock, 0)
	require.Less(t, firstBlock, len(raw), "截断点必须落在流中间，才可能产生读错误")

	path := filepath.Join(t.TempDir(), "truncated-at-flush.gz")
	require.NoError(t, os.WriteFile(path, raw[:firstBlock], 0o644))

	key := testKey("src-trunc-flush", "g1")
	led := ledger.New()
	res, err := NewArchiveImporter(led, key).ImportGzip(path)
	require.NoError(t, err, "截断归档不得硬失败")
	require.True(t, visibleArchiveGap(led.Get(key)),
		"零残余字节的读错误同样必须留下可见缺口（否则静默丢尾）；res=%+v", res)
}

// 覆盖全部截断点：任何截断都必须给出**可见**结论——要么记缺口，要么被分类为损坏归档
// （ImportGzip 对该情形同样记 ARCHIVE_GZIP_CORRUPT）。逐点遍历使结论不依赖某个特定布局。
func TestArchiveTruncationAtEveryOffsetIsVisible(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.gz")
	writeGzip(t, full, []string{"line-1", "line-2", "line-3"})
	raw, err := os.ReadFile(full)
	require.NoError(t, err)
	require.Greater(t, len(raw), 10)

	for cut := 1; cut < len(raw); cut++ {
		path := filepath.Join(dir, fmt.Sprintf("cut-%d.gz", cut))
		require.NoError(t, os.WriteFile(path, raw[:cut], 0o644))

		key := testKey(fmt.Sprintf("src-cut-%d", cut), "g1")
		led := ledger.New()
		res, err := NewArchiveImporter(led, key).ImportGzip(path)
		require.NoErrorf(t, err, "cut=%d 不得硬失败", cut)
		require.Truef(t, visibleArchiveGap(led.Get(key)),
			"cut=%d/%d 截断必须可见（缺口或损坏分类）；res=%+v", cut, len(raw), res)
	}
}
