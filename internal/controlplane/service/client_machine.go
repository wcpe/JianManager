package service

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// ClientMachineService 客户端机器码登记（FR-092，见 ADR-023）。
//
// 弱一致、best-effort：失败仅返回错误供调用方忽略，**绝不阻断玩家拉取**。机器码不可信，仅统计 + 辅助限流。
type ClientMachineService struct {
	db *gorm.DB
}

// NewClientMachineService 创建机器码登记服务。
func NewClientMachineService(db *gorm.DB) *ClientMachineService {
	return &ClientMachineService{db: db}
}

// machineIDMaxLen 机器码登记最大长度（应为 64 hex；超长即可疑，截断登记防滥用撑表）。
const machineIDMaxLen = 128

// Record 登记一次机器码命中（upsert：存在则 hit_count+1 并更新 last_seen，否则插入 hit_count=1）。
// channelID 或 machineID 为空直接返回（不登记）。SQLite/MySQL 同构（GORM OnConflict）。
func (s *ClientMachineService) Record(channelID, machineID string) error {
	if channelID == "" || machineID == "" {
		return nil
	}
	if len(machineID) > machineIDMaxLen {
		machineID = machineID[:machineIDMaxLen]
	}
	now := time.Now()
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "channel_id"}, {Name: "machine_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"hit_count": gorm.Expr("hit_count + 1"),
			"last_seen": now,
		}),
	}).Create(&model.ClientMachine{
		ChannelID: channelID,
		MachineID: machineID,
		HitCount:  1,
		FirstSeen: now,
		LastSeen:  now,
	}).Error
}
