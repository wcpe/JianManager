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
		InstanceID:    instance.ID,
		Name:          "full-checksum",
		FilePath:      "var/backups/full.tar.gz",
		FileSizeMB:    12.5,
		Status:        model.BackupStatusCompleted,
		Checksum:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ChecksumAlgo:  "sha256",
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
