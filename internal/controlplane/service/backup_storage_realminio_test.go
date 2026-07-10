package service

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// TestBackupStorage_TestCandidate_RealMinIO 用真实 MinIO 端到端验证 CP 侧
// 「测试连接」全链路：TestCandidate → ${ENV_VAR} 凭证解析 → probeS3Storage 的
// HEAD-bucket + signS3Probe（手写 SigV4）。覆盖 FR-152 盲区：此前该路径仅有针对
// 假服务器的单测，从未证明 CP 的 SigV4 探测能被真实 S3 接受。
//
// 由 JM_ACC_MINIO_ENDPOINT 门控，不影响常规 CI。
func TestBackupStorage_TestCandidate_RealMinIO(t *testing.T) {
	endpoint := os.Getenv("JM_ACC_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("未设置 JM_ACC_MINIO_ENDPOINT，跳过真实 MinIO 集成测试")
	}
	bucket := os.Getenv("JM_ACC_MINIO_BUCKET")
	if bucket == "" {
		bucket = "jm-backups"
	}
	t.Setenv("JM_ACC_MINIO_AK", envOrDefault("JM_ACC_MINIO_AK", "minioadmin"))
	t.Setenv("JM_ACC_MINIO_SK", envOrDefault("JM_ACC_MINIO_SK", "minioadmin"))

	svc := &BackupStorageService{}

	t.Run("正确凭证连通", func(t *testing.T) {
		res := svc.TestCandidate(model.BackupStorage{
			Type:         model.BackupStorageS3,
			Endpoint:     endpoint,
			Bucket:       bucket,
			Region:       "us-east-1",
			AccessKeyEnv: "${JM_ACC_MINIO_AK}",
			SecretKeyEnv: "${JM_ACC_MINIO_SK}",
			UseSSL:       false,
		})
		require.True(t, res.Ok, "真实 MinIO 上正确凭证探测失败: %s", res.Message)
		require.Equal(t, "连接正常", res.Message)
	})

	t.Run("桶不存在应报错", func(t *testing.T) {
		res := svc.TestCandidate(model.BackupStorage{
			Type:         model.BackupStorageS3,
			Endpoint:     endpoint,
			Bucket:       "no-such-bucket-xyz",
			Region:       "us-east-1",
			AccessKeyEnv: "${JM_ACC_MINIO_AK}",
			SecretKeyEnv: "${JM_ACC_MINIO_SK}",
			UseSSL:       false,
		})
		require.False(t, res.Ok, "不存在的桶不应通过探测")
	})
}

func envOrDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
