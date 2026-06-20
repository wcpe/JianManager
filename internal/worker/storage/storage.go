// Package storage 提供备份远程存储后端的传输抽象（FR-057）。
//
// Worker Node 在本机数据根创建备份归档后，按本包的 Backend 接口把归档上传到
// 远程后端（S3 兼容 / SFTP / WebDAV），恢复时再拉回本地。凭证由 Control Plane 侧
// 从 ${ENV_VAR} 解析后经 gRPC 下发（CP 拥有配置/DB），本包仅消费已解析的明文凭证，
// 自身不读环境变量、不碰数据库。参见 .claude/rules/config-files.md 与架构不变量。
package storage

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// 后端类型常量。local 表示不外置（归档仅留在节点本地数据根）。
const (
	TypeLocal  = "local"
	TypeS3     = "s3"
	TypeSFTP   = "sftp"
	TypeWebDAV = "webdav"
)

// Config 是后端无关的存储配置，凭证字段为已解析的明文（非 ${ENV_VAR} 引用）。
type Config struct {
	Type      string // local | s3 | sftp | webdav
	Endpoint  string // S3 endpoint / WebDAV base URL / SFTP host[:port]
	Bucket    string // S3 bucket
	Region    string // S3 region（SigV4 用，缺省 us-east-1）
	Prefix    string // 对象键/远程目录前缀
	AccessKey string // S3 access key / SFTP 用户名 / WebDAV 用户名
	SecretKey string // S3 secret key / SFTP 密码 / WebDAV 密码
	UseSSL    bool   // S3 是否启用 TLS
}

// Backend 是一个对象式远程存储后端：以 key 为单位上传/下载/删除归档。
type Backend interface {
	// Upload 把 size 字节的内容以 key 写入后端（流式）。
	Upload(ctx context.Context, key string, r io.Reader, size int64) error
	// Download 按 key 返回可读流，调用方负责关闭。
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete 删除 key 对应对象；对象不存在视为成功（幂等）。
	Delete(ctx context.Context, key string) error
}

// New 按 Config.Type 构造后端。local 无远程传输，返回错误以提示调用方无需上传。
func New(cfg Config) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case TypeS3:
		return newS3Backend(cfg)
	case TypeWebDAV:
		return newWebDAVBackend(cfg)
	case TypeSFTP:
		return newSFTPBackend(cfg)
	case TypeLocal, "":
		return nil, fmt.Errorf("本地存储无需远程传输")
	default:
		return nil, fmt.Errorf("不支持的存储后端类型: %s", cfg.Type)
	}
}

// ObjectKey 组合远程对象键：<prefix>/<instanceUUID>/<backupUUID>.tar.gz。
// prefix 两端的「/」被规整，空 prefix 不产生前导「/」。
func ObjectKey(prefix, instanceUUID, backupUUID string) string {
	parts := []string{}
	if p := strings.Trim(strings.TrimSpace(prefix), "/"); p != "" {
		parts = append(parts, p)
	}
	parts = append(parts, instanceUUID, backupUUID+".tar.gz")
	return strings.Join(parts, "/")
}
