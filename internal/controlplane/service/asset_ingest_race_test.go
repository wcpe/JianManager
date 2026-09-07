package service

import (
	"bytes"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
)

// 回归：并发 Ingest 同一内容（发布重试/并发上传常见）不得 500。
// 修复前：SELECT 未命中 → 两路 Create → 唯一索引 idx_assets_type_sha256 冲突方报
// 「登记资产失败: UNIQUE constraint failed」→ 上传/发布整体失败。
func TestAsset_IngestConcurrentDuplicateContent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Asset{}))
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	svc := NewAssetService(db, root)

	content := []byte("identical-content-ingested-concurrently")
	const n = 8
	results := make([]*model.Asset, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			asset, err := svc.Ingest(bytes.NewReader(content), IngestParams{Type: model.AssetTypeClientFile, Filename: "same.bin"})
			results[i], errs[i] = asset, err
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range errs {
		require.NoError(t, errs[i], "并发重复入库第 %d 路不得失败", i)
		require.NotNil(t, results[i])
		require.Equal(t, sha256hex(content), results[i].SHA256)
	}
	// 库内只有一条记录（唯一索引约束下的去重正确性）。
	var count int64
	require.NoError(t, db.Model(&model.Asset{}).Where("type = ?", model.AssetTypeClientFile).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
