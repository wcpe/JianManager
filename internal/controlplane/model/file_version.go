package model

import "time"

// FileVersion 通用文件版本快照记录（FR-051）。
//
// 与 InstanceConfigVersion（FR-031，仅限配置文件）共用「改前快照 + 列表 + diff + 回滚」
// 的版本机制，但适用于实例工作目录下的任意文件（含上传覆盖的二进制文件）。
// 二者刻意分表：配置版本带 schema/校验语义，通用文件版本只关心字节内容，
// 避免把二进制内容混入按文本语义设计的 instance_config_versions 表。
//
// 版本事实源在 Control Plane 数据库（架构不变量：Worker 不碰 DB），
// 文件本体仍归 Worker；快照内容随写入时机由 CP 先读旧内容后落库。
//
// Content 以 base64 编码存储，保证二进制文件（jar/zip/图片等）也能无损快照与回滚；
// 不直接存原始字节是为了兼容 SQLite/MySQL 的文本列与现有 GORM string 字段约定。
// RollbackOfVersionID 记录本次写入是否由回滚触发，指向被回滚的源版本，便于审计回滚链。
type FileVersion struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	InstanceID          uint      `gorm:"not null;index:idx_file_version_instance_path" json:"instanceId"`
	FilePath            string    `gorm:"type:varchar(512);not null;index:idx_file_version_instance_path" json:"filePath"`
	ContentHash         string    `gorm:"type:char(64);not null;index" json:"contentHash"`
	Content             string    `gorm:"type:longtext;not null" json:"-"`
	Size                int64     `gorm:"not null" json:"size"`
	AuthorID            uint      `gorm:"index" json:"authorId"`
	RollbackOfVersionID *uint     `gorm:"index" json:"rollbackOfVersionId,omitempty"`
	CreatedAt           time.Time `json:"createdAt"`
}
