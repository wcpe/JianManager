package model

import "time"

// InstanceBinaryBinding 实例 ↔ 二进制版本的绑定（FR-468 §2.1）。
//
// 版本真源是制品库 Asset（内容寻址，SHA256 去重；ADR-011），本表只记录
// 「该实例当前跑的是哪个 asset」这一事实，避免再建一套版本表形成双真源。
//
// 关键决策（FR-468 §2.2）：重建（rebuildBinaryInstance）从「重新按优先级解析制品」
// 改为「读本绑定」——旧行为会在制品库出现新版本时把运行中的实例静默升级/降级，
// 运维无法预期。新的「吃到新版本」由受控升级（BinaryVersionService.Upgrade）显式承担。
// 语义修订见 ADR-090 补记。
type InstanceBinaryBinding struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// InstanceID 所属实例（一实例一条绑定）。
	InstanceID uint `gorm:"not null;uniqueIndex" json:"instanceId"`
	// CurrentAssetID 实例当前生效的二进制制品 ID；0 = 无制品库版本
	// （kind=url / node_file 来源，仅记录落盘文件名与摘要，展示为「未知来源」）。
	CurrentAssetID uint `gorm:"not null;default:0;index" json:"currentAssetId"`
	// PreviousAssetID 上一次升级前的制品 ID，供「回滚到上一版本」（0=无可回滚）。
	PreviousAssetID uint `gorm:"not null;default:0" json:"previousAssetId"`
	// CurrentFilename 当前落盘文件名（工作目录内），供启动命令派生与展示。
	CurrentFilename string `gorm:"type:varchar(255)" json:"currentFilename"`
	// CurrentSHA256 当前二进制内容摘要（冗余快照，便于列表展示与漂移检测）。
	CurrentSHA256 string `gorm:"type:char(64)" json:"currentSha256"`
	// CurrentVersion 当前版本标记（取自 Asset.Version；url/node_file 来源为空）。
	CurrentVersion string `gorm:"type:varchar(128)" json:"currentVersion"`
	// PreviousFilename / PreviousSHA256 回滚点的落盘名与摘要（回滚需恢复旧文件名与旧启动命令）。
	PreviousFilename string `gorm:"type:varchar(255)" json:"previousFilename"`
	PreviousSHA256   string `gorm:"type:char(64)" json:"previousSha256"`
	// StartCommandAtBinding 绑定时的启动命令快照；回滚时用于判断是否需要恢复旧命令。
	StartCommandAtBinding string    `gorm:"type:varchar(1024)" json:"startCommandAtBinding"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

// HasRollbackTarget 报告是否存在可回滚的上一版本。
func (b *InstanceBinaryBinding) HasRollbackTarget() bool { return b.PreviousAssetID != 0 }
