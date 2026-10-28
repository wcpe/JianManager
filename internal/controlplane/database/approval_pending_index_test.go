package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

/**
 * FR-490 审批票据幂等索引的迁移验收。
 *
 * 两个必须验证的点，缺一个这条迁移就是空壳：
 *  1. **索引真的能拦住**同条件的第二张 pending（谓词写错、建成非唯一索引都会让「幂等」失守，
 *     而那在应用层看不见——两个并发请求都成功插入了）；
 *  2. **重复迁移幂等**：CP 每次启动都会跑，任何非幂等行为都会在第二次启动时炸成启动失败。
 */

// pendingTicket 造一张 pending 票（同条件）。
func pendingTicket(callID string) *model.ApprovalTicket {
	return &model.ApprovalTicket{
		ApplicantUserID: 1, ApplicantKind: model.ApprovalApplicantAgent,
		Action: callID, TargetType: "instance", TargetID: "7",
		PayloadHash: "h", PayloadFrozen: "{}", Reason: "测试",
		Status: model.ApprovalStatusPending, ExpiresAt: time.Now().Add(time.Hour),
	}
}

func TestApprovalPendingIndex_BlocksSecondPending(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "a.db")),
		&gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ApprovalTicket{}))
	require.NoError(t, migrateApprovalPendingUniqueIndex(db))

	// 同 (申请人, action, 目标, payload) 第二张 pending 必须被拒。
	require.NoError(t, db.Create(pendingTicket("mcp.instance_stop")).Error)
	require.Error(t, db.Create(pendingTicket("mcp.instance_stop")).Error,
		"同条件第二张 pending 必须被数据库拒绝")

	// 反向校验：不同 action 应允许并存（别把索引写成「一个申请人只能一张票」）。
	require.NoError(t, db.Create(pendingTicket("mcp.file_issue_transfer_ticket")).Error)

	// 终态不受约束：被驳回后同条件可再申请（正常流程：改好理由再来一次）。
	require.NoError(t, db.Model(&model.ApprovalTicket{}).
		Where("action = ?", "mcp.instance_stop").
		Update("status", model.ApprovalStatusRejected).Error)
	require.NoError(t, db.Create(pendingTicket("mcp.instance_stop")).Error,
		"上一张已驳回后，同条件应可再次申请")
}

func TestApprovalPendingIndex_IdempotentMigration(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "b.db")),
		&gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.ApprovalTicket{}))

	require.NoError(t, migrateApprovalPendingUniqueIndex(db))
	require.NoError(t, migrateApprovalPendingUniqueIndex(db), "重复迁移必须幂等")

	// 建完后约束确实生效。
	require.NoError(t, db.Create(pendingTicket("a")).Error)
	require.Error(t, db.Create(pendingTicket("a")).Error)
}

func TestApprovalPendingIndex_SkipsWhenTableMissing(t *testing.T) {
	// 表还没建时迁移应直接返回，而不是报错——否则首启顺序一旦调整就变成启动失败。
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "c.db")),
		&gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, migrateApprovalPendingUniqueIndex(db))
}
