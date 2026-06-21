package service

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newStorageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.BackupStorage{}, &model.Backup{}))
	return db
}

// TestCreate_RejectsPlaintextCredential 凭证非 ${ENV_VAR} 引用（明文）时拒绝创建。
func TestCreate_RejectsPlaintextCredential(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	_, err := svc.Create(&model.BackupStorage{
		Name: "s3-plain", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b",
		AccessKeyEnv: "AKIAPLAINTEXT", // 明文，非 ${VAR}
	})
	require.ErrorIs(t, err, ErrCredentialNotEnvRef)
}

// TestCreate_RejectsInvalidType 非法类型被拒。
func TestCreate_RejectsInvalidType(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	_, err := svc.Create(&model.BackupStorage{Name: "x", Type: "ftp"})
	require.ErrorIs(t, err, ErrInvalidStorageType)
}

// TestCreate_DefaultsS3Region S3 未填 region 时默认 us-east-1。
func TestCreate_DefaultsS3Region(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	st, err := svc.Create(&model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b",
		AccessKeyEnv: "${AK}", SecretKeyEnv: "${SK}",
	})
	require.NoError(t, err)
	require.Equal(t, "us-east-1", st.Region)
}

// TestResolveSpec_FromEnv 凭证从环境变量解析为明文下发 spec。
func TestResolveSpec_FromEnv(t *testing.T) {
	t.Setenv("JM_TEST_BK_AK", "ak-secret")
	t.Setenv("JM_TEST_BK_SK", "sk-secret")
	svc := NewBackupStorageService(newStorageTestDB(t))
	st, err := svc.Create(&model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local:9000", Bucket: "backups",
		Prefix: "jm", AccessKeyEnv: "${JM_TEST_BK_AK}", SecretKeyEnv: "${JM_TEST_BK_SK}", UseSSL: false,
	})
	require.NoError(t, err)

	spec, err := svc.ResolveSpec(st.ID)
	require.NoError(t, err)
	require.Equal(t, "s3", spec.Type)
	require.Equal(t, "backups", spec.Bucket)
	require.Equal(t, "jm", spec.Prefix)
	require.Equal(t, "ak-secret", spec.AccessKey) // 已解析明文
	require.Equal(t, "sk-secret", spec.SecretKey)
	require.False(t, spec.UseSsl)
}

// TestResolveSpec_MissingEnv 引用的环境变量未设置时报错（不静默空凭证）。
func TestResolveSpec_MissingEnv(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	st, err := svc.Create(&model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b",
		AccessKeyEnv: "${JM_TEST_DEFINITELY_MISSING_VAR}",
	})
	require.NoError(t, err)
	_, err = svc.ResolveSpec(st.ID)
	require.ErrorIs(t, err, ErrCredentialEnvMissing)
}

// TestDelete_RejectedWhenReferencedByBackup 被备份引用的存储后端不可删除。
func TestDelete_RejectedWhenReferencedByBackup(t *testing.T) {
	db := newStorageTestDB(t)
	svc := NewBackupStorageService(db)
	st, err := svc.Create(&model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b",
		AccessKeyEnv: "${AK}", SecretKeyEnv: "${SK}",
	})
	require.NoError(t, err)

	require.NoError(t, db.Create(&model.Backup{InstanceID: 1, Name: "bk", StorageID: &st.ID}).Error)

	err = svc.Delete(st.ID)
	require.ErrorIs(t, err, ErrStorageInUse)
}

// TestDelete_OK 无引用的后端可删除。
func TestDelete_OK(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	st, err := svc.Create(&model.BackupStorage{
		Name: "dav", Type: model.BackupStorageWebDAV, Endpoint: "https://dav.local",
	})
	require.NoError(t, err)
	require.NoError(t, svc.Delete(st.ID))

	_, err = svc.GetByID(st.ID)
	require.ErrorIs(t, err, ErrStorageNotFound)
}
