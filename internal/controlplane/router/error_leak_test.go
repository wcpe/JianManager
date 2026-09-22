package router

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service"
)

// TestWriteBinaryVersionError_DoesNotLeakInternalError M-1（edge 报告）：500 分支
// **不得**把 err.Error() 拼进响应体。
//
// 缺陷现场：`default` 分支把 `fallbackMsg + ": " + err.Error()` 直接回给浏览器。
// service 层错误含归档路径、worker 侧文件错误（formatPermError 会把 os 错误连路径
// 一起带出）等实现细节——向已授权用户暴露服务端文件系统绝对路径与内部结构。
func TestWriteBinaryVersionError_DoesNotLeakInternalError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/instances/1/binary-upgrade", nil)

	secret := "/var/lib/jianmanager/var/servers/paper-a-8b4cb747/binaries/server.jar: permission denied"
	writeBinaryVersionError(c, errors.New(secret), "升级失败")

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	body := rec.Body.String()
	require.NotContains(t, body, "permission denied", "不得回传底层 os 错误")
	require.NotContains(t, body, "/var/lib/jianmanager", "不得回传服务端绝对路径")

	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, "INTERNAL_ERROR", payload.Error)
	require.Contains(t, payload.Message, "升级失败", "仍要给出可操作的前缀文案")
	require.Contains(t, payload.Message, "服务端日志", "引导调用方去查日志（详情在那里）")
}

// TestWriteBinaryVersionError_KeepsClassifiedMessages 已分类错误仍回具体原因
// （加固不得把有用的 4xx 文案一起抹掉）。
func TestWriteBinaryVersionError_KeepsClassifiedMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"无回滚点", service.ErrNoPreviousBinaryVersion, http.StatusBadRequest, "NO_PREVIOUS_VERSION"},
		{"目标非法", service.ErrBinaryVersionTargetInvalid, http.StatusBadRequest, "INVALID_TARGET"},
		{"实例不存在", service.ErrInstanceNotFound, http.StatusNotFound, "NOT_FOUND"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			writeBinaryVersionError(c, tc.err, "回滚失败")
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Contains(t, rec.Body.String(), tc.wantCode)
		})
	}
}

// TestSnapshotCreate_InternalErrorIsNotLeaked M-1：快照创建的 500 分支只回固定文案。
//
// 触发方式：删掉 `instance_snapshots` 表，使 `Create` 在「登记快照」这一步拿到一个
// 未分类的 DB 错误（`no such table: instance_snapshots`）——这正是缺陷现场回传给
// 浏览器的那类内部信息。断言响应体只有固定中文文案，不含任何 SQL/表名细节。
func TestSnapshotCreate_InternalErrorIsNotLeaked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r, db, _, instanceID, _ := newSnapshotBinaryGateFixture(t)
	// fixture 内部已经走过 setup 创建管理员，此处复用同一账号登录（不能再 setup 一次）。
	adminTok := loginTestUser(t, r, "admin", "password123")

	require.NoError(t, db.Migrator().DropTable(&model.InstanceSnapshot{}))

	w := makeRequest(r, http.MethodPost, "/api/v1/instances/"+itoa(instanceID)+"/snapshots",
		map[string]any{"name": "x"}, adminTok)
	require.Equal(t, http.StatusInternalServerError, w.Code,
		"删表后的 DB 错误不是已分类错误 → 应落 500 default 分支")
	body := w.Body.String()
	require.NotContains(t, body, "no such table", "不得回传底层 SQL 错误: %s", body)
	require.NotContains(t, body, "instance_snapshots", "不得回传内部表名: %s", body)
	require.True(t, strings.Contains(body, "创建快照失败"), "仍要给出固定文案: %s", body)
}
