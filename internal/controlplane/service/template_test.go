package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/database"
)

func setupTemplateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmpDir := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmpDir+"/test.db"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(db))
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	})
	return db
}

func TestTemplateService_CreateListDelete(t *testing.T) {
	db := setupTemplateTestDB(t)
	svc := NewTemplateService(db)

	tpl, err := svc.Create(CreateTemplateRequest{
		Name:         "Paper 1.20",
		Type:         "minecraft_java",
		StartCommand: "java -jar paper.jar",
		DownloadURL:  "https://example.com/paper.jar",
	})
	require.NoError(t, err)
	require.NotZero(t, tpl.ID)

	list, err := svc.List()
	require.NoError(t, err)
	assert.Len(t, list, 1)

	require.NoError(t, svc.Delete(tpl.ID))

	list, err = svc.List()
	require.NoError(t, err)
	assert.Len(t, list, 0)
}

func TestTemplateService_Delete_NonExistent(t *testing.T) {
	db := setupTemplateTestDB(t)
	svc := NewTemplateService(db)

	// 删除不存在的模板不应报错（GORM 软删除/硬删除对不存在行返回 nil）。
	assert.NoError(t, svc.Delete(9999))
}
