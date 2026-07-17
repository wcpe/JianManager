// Package blobstore 提供制品库 blob 的存储后端抽象（FR-347，见 ADR-073）。
//
// 存储键 = CAS 相对路径 var/artifacts/<type>/<sha256 前 2 位>/<sha256><ext>
// （与 model.Asset.RelPath 同值）；S3 适配器在键前再挂渠道 Prefix。
// 本地适配器封装既有 CAS 落盘行为（零变化）；S3 适配器纯标准库 SigV4 +
// path-style 寻址（不引入 SDK），并支持 query 参数预签名短时效下载 URL。
//
// 依赖方向（ADR-073 决策 3）：CP 侧独立实现，不 import internal/worker/storage
// （进程边界语义）；SigV4 正确性以 AWS 官方签名向量对拍。
package blobstore

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	// ErrBlobNotFound 目标 blob 不存在。
	ErrBlobNotFound = errors.New("blob 不存在")
	// ErrPresignUnsupported 后端不支持预签名 URL（local 后端由 CP 直出，无需预签名）。
	ErrPresignUnsupported = errors.New("该存储后端不支持预签名 URL")
)

// 后端类型标识（写入 model.Asset.StorageBackend）。
const (
	// KindLocal 本地数据根 CAS。
	KindLocal = "local"
	// KindS3 S3 兼容对象存储（AWS S3 / MinIO / rustfs 等）。
	KindS3 = "s3"
)

// ObjectInfo 一个已存储 blob 的元数据。
type ObjectInfo struct {
	// Key 存储键（CAS 相对路径，不含渠道 Prefix）。
	Key string
	// Size 字节数。
	Size int64
	// ModTime 最后修改时间（S3 侧为对象 Last-Modified）。
	ModTime time.Time
}

// Store 制品 blob 存储后端（FR-347，见 ADR-073）。
//
// 写入口用 PutFile（收已写完的本地临时文件）而非泛化 Put(io.Reader)：
// Ingest 恒先落临时文件边写边算 hash，local 适配器得以用与主线逐字节相同的
// os.Rename 保证零行为变化；S3 适配器拿到可重读文件做流式 PUT。
type Store interface {
	// Kind 返回后端类型标识（KindLocal | KindS3）。
	Kind() string
	// PutFile 把 srcPath 指向的完整文件放入存储的 key 处。
	// local：MkdirAll + os.Rename（原子，与既有 Ingest 落盘等价）；
	// s3：流式 PUT 上传（UNSIGNED-PAYLOAD，不缓冲大文件），srcPath 原文件保留由调用方清理。
	PutFile(ctx context.Context, key, srcPath string, size int64) error
	// Open 打开 blob 内容读取流。缺失返回 ErrBlobNotFound。
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	// Stat 返回 blob 元数据。缺失返回 ErrBlobNotFound。
	Stat(ctx context.Context, key string) (*ObjectInfo, error)
	// Delete 幂等删除 blob（缺失不报错）。
	Delete(ctx context.Context, key string) error
	// List 枚举 prefix 下至多 limit 个 blob（连通探测等单页场景；limit<=0 取 1000）。
	List(ctx context.Context, prefix string, limit int) ([]ObjectInfo, error)
	// ListPage 分页枚举 prefix 下的 blob（FR-349 对账全量遍历用）。
	// token 传上一页返回的续传令牌（首页传空）；nextToken 非空表示还有后续页；limit<=0 取 1000。
	// 令牌对调用方不透明（s3=ListObjectsV2 continuation-token；local=游标键），大 bucket 循环至 nextToken 为空即全量。
	ListPage(ctx context.Context, prefix string, limit int, token string) (items []ObjectInfo, nextToken string, err error)
	// Presign 生成 blob 的短时效公开 GET URL（无需凭证即可下载，302 分发用）。
	// 纯本地签名计算无网络 IO。local 后端返回 ErrPresignUnsupported。
	Presign(key string, ttl time.Duration) (string, error)
}
