package router

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func TestBackup_List_IncludesChecksumFields(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	instance := &model.Instance{
		UUID:         "inst-backup-checksum",
		NodeID:       node.ID,
		Name:         "survival",
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDirect,
		Status:       model.InstanceStatusStopped,
		StartCommand: "java -jar server.jar",
	}
	require.NoError(t, db.Create(instance).Error)
	backup := &model.Backup{
		InstanceID:   instance.ID,
		Name:         "full-checksum",
		FilePath:     "var/backups/full.tar.gz",
		FileSizeMB:   12.5,
		Status:       model.BackupStatusCompleted,
		Checksum:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ChecksumAlgo: "sha256",
	}
	require.NoError(t, db.Create(backup).Error)

	w := makeRequest(r, http.MethodGet, "/api/v1/instances/"+itoa(instance.ID)+"/backups", nil, token)
	require.Equal(t, http.StatusOK, w.Code)
	rows := parseJSONArray(t, w)
	require.Len(t, rows, 1)
	row, ok := rows[0].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, backup.Checksum, row["checksum"])
	require.Equal(t, backup.ChecksumAlgo, row["checksumAlgo"])
}

// TestBackup_Restore_RejectedWhenInstanceRunning 实例运行中恢复备份回 409（FR-013 真机验收缺陷回归）：
// 恢复会解包覆盖工作目录，运行中的服务器下次自动存档会把恢复结果覆盖回去（静默失效），须先停止实例。
func TestBackup_Restore_RejectedWhenInstanceRunning(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	instance := &model.Instance{
		UUID:         "inst-backup-restore-running",
		NodeID:       node.ID,
		Name:         "survival",
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDirect,
		Status:       model.InstanceStatusRunning,
		StartCommand: "java -jar server.jar",
	}
	require.NoError(t, db.Create(instance).Error)
	backup := &model.Backup{
		InstanceID: instance.ID,
		Name:       "full-restore-guard",
		Status:     model.BackupStatusCompleted,
		FilePath:   "var/backups/full.tar.gz",
	}
	require.NoError(t, db.Create(backup).Error)

	w := makeRequest(r, http.MethodPost, "/api/v1/backups/"+itoa(backup.ID)+"/restore", nil, token)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	body := parseJSON(t, w)
	require.Equal(t, "INSTANCE_NOT_STOPPED", body["error"])
	require.Contains(t, body["message"], "请先停止实例")
}

func TestBackup_Delete_ReferencedParentReturnsBusinessError(t *testing.T) {
	db := setupTestDB(t)
	r := setupTestRouter(db)
	token := getAdminToken(t, r)
	node := createTestNode(t, db)
	instance := &model.Instance{
		UUID:         "inst-backup-delete-chain",
		NodeID:       node.ID,
		Name:         "survival",
		Type:         model.InstanceTypeMinecraftJava,
		Role:         model.InstanceRoleBackend,
		ProcessType:  model.ProcessTypeDirect,
		Status:       model.InstanceStatusStopped,
		StartCommand: "java -jar server.jar",
	}
	require.NoError(t, db.Create(instance).Error)
	full := &model.Backup{
		InstanceID: instance.ID,
		Name:       "full-parent",
		Mode:       model.BackupModeFull,
		Status:     model.BackupStatusCompleted,
		FilePath:   "var/backups/full.tar.gz",
	}
	require.NoError(t, db.Create(full).Error)
	inc := &model.Backup{
		InstanceID: instance.ID,
		Name:       "inc-child",
		Mode:       model.BackupModeIncremental,
		ParentID:   &full.ID,
		Status:     model.BackupStatusCompleted,
		FilePath:   "var/backups/inc.tar.gz",
	}
	require.NoError(t, db.Create(inc).Error)

	w := makeRequest(r, http.MethodDelete, "/api/v1/backups/"+itoa(full.ID), nil, token)
	require.Equal(t, http.StatusUnprocessableEntity, w.Code)
	body := parseJSON(t, w)
	require.Equal(t, "BUSINESS_ERROR", body["error"])
	require.Contains(t, body["message"], "增量备份依赖")
}
