package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	cpgrpc "github.com/wcpe/JianManager/internal/controlplane/grpc"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/dataroot"
	"github.com/wcpe/JianManager/proto/workerpb"
)

func newStorageTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.BackupStorage{}, &model.Backup{}))
	return db
}

type fakeStorageTestWorker struct {
	workerpb.WorkerServiceClient
	got  *workerpb.TestStorageBackendRequest
	resp *workerpb.TestStorageBackendResponse
	err  error
}

func (f *fakeStorageTestWorker) TestStorageBackend(ctx context.Context, req *workerpb.TestStorageBackendRequest, _ ...grpc.CallOption) (*workerpb.TestStorageBackendResponse, error) {
	f.got = req
	return f.resp, f.err
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

// TestCreate_NameConflict 名称与既有后端冲突时回业务错误（FR-338 收口，替代裸撞唯一索引的 500）。
func TestCreate_NameConflict(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	_, err := svc.Create(&model.BackupStorage{Name: "dup", Type: model.BackupStorageWebDAV, Endpoint: "https://dav.local"})
	require.NoError(t, err)
	_, err = svc.Create(&model.BackupStorage{Name: "dup", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b"})
	require.ErrorIs(t, err, ErrStorageNameConflict)
}

// TestUpdate_OK 编辑改名/改凭证引用/改 endpoint 落库生效，且 lastTest* 清空（FR-338）。
func TestUpdate_OK(t *testing.T) {
	db := newStorageTestDB(t)
	svc := NewBackupStorageService(db)
	st, err := svc.Create(&model.BackupStorage{
		Name: "s3-old", Type: model.BackupStorageS3, Endpoint: "old.local:9000", Bucket: "old-bucket",
		Region: "us-east-1", Prefix: "old/", AccessKeyEnv: "${OLD_AK}", SecretKeyEnv: "${OLD_SK}", UseSSL: true,
	})
	require.NoError(t, err)

	// 预置一次测试结论，编辑后应被清空（旧连通性结论失效）。
	now := time.Now().UTC()
	require.NoError(t, db.Model(&model.BackupStorage{}).Where("id = ?", st.ID).Updates(map[string]any{
		"last_test_at": &now, "last_test_ok": true, "last_test_message": "连接正常",
	}).Error)

	updated, err := svc.Update(st.ID, model.BackupStorage{
		Name: "s3-new", Type: model.BackupStorageS3, Endpoint: "new.local:9000", Bucket: "new-bucket",
		Region: "eu-west-1", Prefix: "new/", AccessKeyEnv: "${NEW_AK}", SecretKeyEnv: "${NEW_SK}", UseSSL: false,
	})
	require.NoError(t, err)
	require.Equal(t, "s3-new", updated.Name)
	require.Equal(t, "new.local:9000", updated.Endpoint)
	require.Equal(t, "new-bucket", updated.Bucket)
	require.Equal(t, "eu-west-1", updated.Region)
	require.Equal(t, "new/", updated.Prefix)
	require.Equal(t, "${NEW_AK}", updated.AccessKeyEnv)
	require.Equal(t, "${NEW_SK}", updated.SecretKeyEnv)
	require.False(t, updated.UseSSL) // 显式 false 落库，未被 GORM 零值跳过吞掉
	require.Nil(t, updated.LastTestAt)
	require.False(t, updated.LastTestOk)
	require.Empty(t, updated.LastTestMessage)
}

// TestUpdate_NotFound 不存在的后端回 ErrStorageNotFound（404）。
func TestUpdate_NotFound(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	_, err := svc.Update(9999, model.BackupStorage{Name: "x", Type: model.BackupStorageS3})
	require.ErrorIs(t, err, ErrStorageNotFound)
}

// TestUpdate_TypeImmutable 改 type 被拒（改型=删重建，FR-338 拍板）。
func TestUpdate_TypeImmutable(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	st, err := svc.Create(&model.BackupStorage{Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b"})
	require.NoError(t, err)
	_, err = svc.Update(st.ID, model.BackupStorage{Name: "s3", Type: model.BackupStorageSFTP, Endpoint: "sftp.local"})
	require.ErrorIs(t, err, ErrStorageTypeImmutable)
}

// TestUpdate_NameConflict 名称撞其他后端回 422 业务错误；撞自身名（不改名保存）放行。
func TestUpdate_NameConflict(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	_, err := svc.Create(&model.BackupStorage{Name: "a", Type: model.BackupStorageWebDAV, Endpoint: "https://a.local"})
	require.NoError(t, err)
	b, err := svc.Create(&model.BackupStorage{Name: "b", Type: model.BackupStorageWebDAV, Endpoint: "https://b.local"})
	require.NoError(t, err)

	_, err = svc.Update(b.ID, model.BackupStorage{Name: "a", Type: model.BackupStorageWebDAV, Endpoint: "https://b.local"})
	require.ErrorIs(t, err, ErrStorageNameConflict)

	// 排除自身：保持原名保存不误伤。
	updated, err := svc.Update(b.ID, model.BackupStorage{Name: "b", Type: model.BackupStorageWebDAV, Endpoint: "https://b2.local"})
	require.NoError(t, err)
	require.Equal(t, "https://b2.local", updated.Endpoint)
}

// TestUpdate_RejectsPlaintextCredential 凭证非 ${ENV_VAR} 引用（明文）被拒，与 Create 同源校验。
func TestUpdate_RejectsPlaintextCredential(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	st, err := svc.Create(&model.BackupStorage{Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b"})
	require.NoError(t, err)
	_, err = svc.Update(st.ID, model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b",
		AccessKeyEnv: "AKIAPLAINTEXT", // 明文，非 ${VAR}
	})
	require.ErrorIs(t, err, ErrCredentialNotEnvRef)
}

// TestUpdate_DefaultsS3Region S3 未填 region 时默认 us-east-1，与 Create 一致。
func TestUpdate_DefaultsS3Region(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	st, err := svc.Create(&model.BackupStorage{Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b", Region: "eu-west-1"})
	require.NoError(t, err)
	updated, err := svc.Update(st.ID, model.BackupStorage{Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b"})
	require.NoError(t, err)
	require.Equal(t, "us-east-1", updated.Region)
}

// TestUpdate_AllowedWhenReferencedByBackup 被备份引用的后端仍可编辑（换密钥/endpoint 为合法运维，不加引用锁）。
func TestUpdate_AllowedWhenReferencedByBackup(t *testing.T) {
	db := newStorageTestDB(t)
	svc := NewBackupStorageService(db)
	st, err := svc.Create(&model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b",
		AccessKeyEnv: "${AK}", SecretKeyEnv: "${SK}",
	})
	require.NoError(t, err)
	require.NoError(t, db.Create(&model.Backup{InstanceID: 1, Name: "bk", StorageID: &st.ID, Status: model.BackupStatusCompleted}).Error)

	updated, err := svc.Update(st.ID, model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3-rotated.local", Bucket: "b",
		AccessKeyEnv: "${AK_ROTATED}", SecretKeyEnv: "${SK_ROTATED}",
	})
	require.NoError(t, err)
	require.Equal(t, "s3-rotated.local", updated.Endpoint)
	require.Equal(t, "${AK_ROTATED}", updated.AccessKeyEnv)
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

// TestListWithStats_AggregatesCompletedBackups 列表聚合已完成备份份数与容量。
func TestListWithStats_AggregatesCompletedBackups(t *testing.T) {
	db := newStorageTestDB(t)
	svc := NewBackupStorageService(db)
	st, err := svc.Create(&model.BackupStorage{Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b"})
	require.NoError(t, err)

	require.NoError(t, db.Create(&model.Backup{InstanceID: 1, Name: "a", StorageID: &st.ID, Status: model.BackupStatusCompleted, FileSizeMB: 1.5}).Error)
	require.NoError(t, db.Create(&model.Backup{InstanceID: 1, Name: "b", StorageID: &st.ID, Status: model.BackupStatusFailed, FileSizeMB: 9}).Error)
	require.NoError(t, db.Create(&model.Backup{InstanceID: 2, Name: "c", Status: model.BackupStatusCompleted, FileSizeMB: 4}).Error)

	storages, err := svc.ListWithStats()
	require.NoError(t, err)
	require.Len(t, storages, 1)
	require.Equal(t, int64(1), storages[0].BackupCount)
	require.Equal(t, int64(1572864), storages[0].UsedBytes)
}

func TestLocalStats_ScansDataRootBackups(t *testing.T) {
	db := newStorageTestDB(t)
	root, err := dataroot.Init(t.TempDir())
	require.NoError(t, err)
	svc := NewBackupStorageService(db)
	svc.SetDataRoot(root)

	backupDir := root.Abs(filepath.FromSlash("var/backups/inst-1"))
	require.NoError(t, os.MkdirAll(backupDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(backupDir, "bk.tar.gz"), []byte("archive"), 0o644))
	require.NoError(t, db.Create(&model.Backup{InstanceID: 1, Name: "local", Status: model.BackupStatusCompleted, FileSizeMB: 99}).Error)

	stats, err := svc.LocalStats()
	require.NoError(t, err)
	require.Equal(t, int64(1), stats.BackupCount)
	require.Equal(t, int64(7), stats.UsedBytes)
}

// TestTestConnection_NoWorkerReturnsTypedFailure 无在线 Worker 时返回可展示失败结果。
func TestTestConnection_NoWorkerReturnsTypedFailure(t *testing.T) {
	svc := NewBackupStorageService(newStorageTestDB(t))
	st, err := svc.Create(&model.BackupStorage{Name: "dav", Type: model.BackupStorageWebDAV, Endpoint: "https://dav.local"})
	require.NoError(t, err)

	result, err := svc.TestConnection(st.ID)
	require.NoError(t, err)
	require.False(t, result.OK)
	require.Equal(t, "NO_WORKER", result.ErrorCode)
}

// TestTestConnection_DelegatesToWorker 探测请求会解析凭证并转发给在线 Worker。
func TestTestConnection_DelegatesToWorker(t *testing.T) {
	t.Setenv("JM_TEST_BK_AK", "ak-secret")
	t.Setenv("JM_TEST_BK_SK", "sk-secret")
	db := newStorageTestDB(t)
	pool := cpgrpc.NewClientPool()
	worker := &fakeStorageTestWorker{resp: &workerpb.TestStorageBackendResponse{Success: true, LatencyMs: 7}}
	pool.SetWorkerClientForTest("node-a", worker)
	svc := NewBackupStorageService(db, pool)
	st, err := svc.Create(&model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b",
		AccessKeyEnv: "${JM_TEST_BK_AK}", SecretKeyEnv: "${JM_TEST_BK_SK}", UseSSL: false,
	})
	require.NoError(t, err)

	result, err := svc.TestConnection(st.ID)
	require.NoError(t, err)
	require.True(t, result.OK)
	require.Equal(t, int64(7), result.LatencyMs)
	require.Equal(t, "node-a", result.NodeUUID)
	require.Equal(t, "ak-secret", worker.got.Storage.AccessKey)
	require.Equal(t, "sk-secret", worker.got.Storage.SecretKey)
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

func TestList_IncludesUsageAndLastTest(t *testing.T) {
	db := newStorageTestDB(t)
	svc := NewBackupStorageService(db)
	st, err := svc.Create(&model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: "s3.local", Bucket: "b",
		AccessKeyEnv: "${AK}", SecretKeyEnv: "${SK}",
	})
	require.NoError(t, err)

	now := time.Now().UTC()
	require.NoError(t, db.Model(&model.BackupStorage{}).Where("id = ?", st.ID).Updates(map[string]any{
		"last_test_at":      now,
		"last_test_ok":      true,
		"last_test_message": "连接正常",
	}).Error)
	require.NoError(t, db.Create(&model.Backup{InstanceID: 1, Name: "bk1", StorageID: &st.ID, FileSizeMB: 1.5, Status: model.BackupStatusCompleted}).Error)
	require.NoError(t, db.Create(&model.Backup{InstanceID: 1, Name: "bk2", StorageID: &st.ID, FileSizeMB: 2, Status: model.BackupStatusCompleted}).Error)

	items, err := svc.ListWithStats()
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int64(2), items[0].BackupCount)
	require.Equal(t, int64(3670016), items[0].UsedBytes)
	require.True(t, items[0].LastTestOk)
	require.Equal(t, "连接正常", items[0].LastTestMessage)
	require.NotNil(t, items[0].LastTestAt)
}

func TestTestSaved_UpdatesLastTestWithoutLeakingSecret(t *testing.T) {
	t.Setenv("JM_TEST_BK_AK", "ak-secret")
	t.Setenv("JM_TEST_BK_SK", "sk-secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodHead, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	db := newStorageTestDB(t)
	svc := NewBackupStorageService(db)
	st, err := svc.Create(&model.BackupStorage{
		Name: "s3", Type: model.BackupStorageS3, Endpoint: server.URL, Bucket: "b",
		AccessKeyEnv: "${JM_TEST_BK_AK}", SecretKeyEnv: "${JM_TEST_BK_SK}",
	})
	require.NoError(t, err)

	result, err := svc.TestSaved(st.ID)
	require.NoError(t, err)
	require.True(t, result.Ok)
	require.GreaterOrEqual(t, result.LatencyMs, int64(0))
	require.NotContains(t, result.Message, "ak-secret")
	require.NotContains(t, result.Message, "sk-secret")

	updated, err := svc.GetByID(st.ID)
	require.NoError(t, err)
	require.True(t, updated.LastTestOk)
	require.NotNil(t, updated.LastTestAt)
}

func TestTestCandidate_ProbesS3Endpoint(t *testing.T) {
	t.Setenv("JM_TEST_BK_AK", "ak")
	t.Setenv("JM_TEST_BK_SK", "sk")
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodHead, r.Method)
		require.Equal(t, "/backups", r.URL.Path)
		require.Contains(t, r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=ak/")
		called.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	svc := NewBackupStorageService(newStorageTestDB(t))
	result := svc.TestCandidate(model.BackupStorage{
		Type: model.BackupStorageS3, Endpoint: server.URL, Bucket: "backups",
		AccessKeyEnv: "${JM_TEST_BK_AK}", SecretKeyEnv: "${JM_TEST_BK_SK}",
	})

	require.True(t, result.Ok, result.Message)
	require.True(t, called.Load(), "应实际探测 S3 endpoint")
}

func TestTestCandidate_ReportsS3ConnectionFailure(t *testing.T) {
	t.Setenv("JM_TEST_BK_AK", "ak")
	t.Setenv("JM_TEST_BK_SK", "sk")
	endpoint := unusedTCPAddr(t)

	svc := NewBackupStorageService(newStorageTestDB(t))
	result := svc.TestCandidate(model.BackupStorage{
		Type: model.BackupStorageS3, Endpoint: "http://" + endpoint, Bucket: "backups",
		AccessKeyEnv: "${JM_TEST_BK_AK}", SecretKeyEnv: "${JM_TEST_BK_SK}",
	})

	require.False(t, result.Ok)
	require.Contains(t, result.Message, "S3")
}

func TestTestCandidate_ProbesWebDAVEndpointWithBasicAuth(t *testing.T) {
	t.Setenv("JM_TEST_DAV_USER", "u")
	t.Setenv("JM_TEST_DAV_PASS", "p")
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodOptions, r.Method)
		user, pass, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "u", user)
		require.Equal(t, "p", pass)
		called.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	svc := NewBackupStorageService(newStorageTestDB(t))
	result := svc.TestCandidate(model.BackupStorage{
		Type: model.BackupStorageWebDAV, Endpoint: server.URL,
		AccessKeyEnv: "${JM_TEST_DAV_USER}", SecretKeyEnv: "${JM_TEST_DAV_PASS}",
	})

	require.True(t, result.Ok, result.Message)
	require.True(t, called.Load(), "应实际探测 WebDAV endpoint")
}

func TestTestCandidate_ProbesSFTPSSH(t *testing.T) {
	t.Setenv("JM_TEST_SFTP_USER", "jm")
	t.Setenv("JM_TEST_SFTP_PASS", "secret")
	endpoint, calls := startTestSSHServer(t, "jm", "secret")

	svc := NewBackupStorageService(newStorageTestDB(t))
	result := svc.TestCandidate(model.BackupStorage{
		Type: model.BackupStorageSFTP, Endpoint: endpoint,
		AccessKeyEnv: "${JM_TEST_SFTP_USER}", SecretKeyEnv: "${JM_TEST_SFTP_PASS}",
	})

	require.True(t, result.Ok, result.Message)
	require.GreaterOrEqual(t, calls.Load(), int64(1), "应实际建立 SSH 握手")
}

func unusedTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func startTestSSHServer(t *testing.T, user, pass string) (string, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	signer, err := ssh.NewSignerFromKey(testHostKey(t))
	require.NoError(t, err)
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() == user && string(password) == pass {
				return nil, nil
			}
			return nil, errors.New("认证失败")
		},
	}
	cfg.AddHostKey(signer)
	var calls atomic.Int64

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			go handleTestSSHConn(conn, cfg)
		}
	}()
	return ln.Addr().String(), &calls
}

func handleTestSSHConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	go func() {
		for ch := range chans {
			_ = ch.Reject(ssh.UnknownChannelType, "测试服务不接受会话")
		}
	}()
	_ = sshConn.Wait()
}

func testHostKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}
