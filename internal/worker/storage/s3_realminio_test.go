package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestS3_RealMinIO 用真实 MinIO（SigV4 强校验）端到端验证 storage 传输后端的
// 上传/下载/删除与 Probe 探测，覆盖 FR-152 盲区：此前 s3 测试均用 httptest 假服务器，
// 仅断言 Authorization 前缀而不做签名校验，无法证明手写 SigV4 能被真实 S3 接受。
//
// 由环境变量门控，不影响常规 CI：
//
//	JM_ACC_MINIO_ENDPOINT  host:port（如 127.0.0.1:19000）
//	JM_ACC_MINIO_BUCKET    已存在的 bucket
//	JM_ACC_MINIO_AK/SK     凭证
func TestS3_RealMinIO(t *testing.T) {
	endpoint := os.Getenv("JM_ACC_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("未设置 JM_ACC_MINIO_ENDPOINT，跳过真实 MinIO 集成测试")
	}
	cfg := Config{
		Type:      TypeS3,
		Endpoint:  endpoint,
		Bucket:    envOr("JM_ACC_MINIO_BUCKET", "jm-backups"),
		Region:    "us-east-1",
		AccessKey: envOr("JM_ACC_MINIO_AK", "minioadmin"),
		SecretKey: envOr("JM_ACC_MINIO_SK", "minioadmin"),
		UseSSL:    false,
	}

	b, err := New(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	key := ObjectKey("acc", "inst-real", "bk-real")
	payload := []byte("jianmanager-real-minio-sigv4-roundtrip")

	// 真实 SigV4 PUT（UNSIGNED-PAYLOAD）→ MinIO 会拒绝签名不合法的请求。
	require.NoError(t, b.Upload(ctx, key, bytes.NewReader(payload), int64(len(payload))), "真实 MinIO 拒绝了上传，SigV4 签名不被接受")

	rc, err := b.Download(ctx, key)
	require.NoError(t, err, "真实 MinIO 拒绝了下载")
	got, readErr := io.ReadAll(rc)
	require.NoError(t, rc.Close())
	require.NoError(t, readErr)
	require.Equal(t, payload, got, "真实 MinIO 取回内容与写入不一致")

	require.NoError(t, b.Delete(ctx, key), "真实 MinIO 拒绝了删除")

	// 删除后再下载应失败（对象已不存在）。
	_, err = b.Download(ctx, key)
	require.Error(t, err, "删除后仍能下载，删除未生效")

	// Probe（上传→校验→删除全链路）也必须在真实 MinIO 上通过。
	require.NoError(t, Probe(ctx, cfg), "Probe 在真实 MinIO 上失败")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
