package model

import "time"

// ClientCoreVersion updater-core 集中版本注册记录（FR-193，见 ADR-045）。
//
// 一条记录 = 运营登记的一个 updater-core 版本：版本号 + 该版本对应的 core jar 制品（内容寻址）。
// Version 在平台内全局单调递增（与频道无关——一份 core jar 可供所有频道复用，ADR-021「一份 jar 三平台通用」）。
// 频道经 ClientChannel.PinnedCoreVersion 选定要分发的 core 版本；manifest 生成时把本记录的制品
// fan-out 填进 agent.core.platforms 的 windows/macos/linux 三键（同 sha256/size/codec）。
//
// 「回退」坏 core 不靠降版本号（客户端 core 只升不降，ADR-045 决策 3/4）：以更高 Version 重发旧
// core 字节（同一 ArtifactSHA256 内容寻址、制品库去重）为新记录，pin 指向它，客户端照常 promote
// 到更高版本号、跑到旧内容。故 Version 始终单调递增。
type ClientCoreVersion struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Version updater-core 自身版本号，平台内全局唯一且单调递增；即 manifest agent.core.version。
	Version int `gorm:"uniqueIndex;not null" json:"version"`
	// ArtifactSHA256 core jar 制品（压缩后）自身 sha256 = 下载寻址 key = client-core 资产的 sha256
	// = manifest agent.core.platforms[os].artifact.sha256。
	ArtifactSHA256 string `gorm:"column:artifact_sha256;type:char(64);not null" json:"artifactSha256"`
	// ArtifactSize core jar 制品（压缩后）字节数。
	ArtifactSize int64 `gorm:"not null" json:"artifactSize"`
	// Codec core jar 制品压缩算法（zstd|none）。
	Codec string `gorm:"type:varchar(16);not null" json:"codec"`
	// Note 登记备注（运营可读，信息性；回退时记「回退至 core vN」）。
	Note string `gorm:"type:varchar(512)" json:"note"`
	// SourceVersion 回退来源版本号（>0 表示本记录是某历史版本的「重发」，0=直接上传登记）。仅信息性/审计。
	SourceVersion int `gorm:"default:0;not null" json:"sourceVersion"`
	// CreatedBy 登记者用户 ID（审计辅助，0=未知）。
	CreatedBy uint      `gorm:"default:0;not null" json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}
