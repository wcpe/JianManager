package retention

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogLevel_GetReportsCurrent 验证 GET 回显当前等级。
func TestLogLevel_GetReportsCurrent(t *testing.T) {
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelWarn)
	h := NewLogLevelController(lv).Handler()

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/debug/log-level", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body logLevelResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "warn", body.Level)
}

// TestLogLevel_PostActuallyChangesLevel 验证 POST 真的改了 LevelVar。
//
// 这是本功能的唯一实质断言：接口返回 200 但 LevelVar 没变，等于没实现。
func TestLogLevel_PostActuallyChangesLevel(t *testing.T) {
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	h := NewLogLevelController(lv).Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/debug/log-level",
		strings.NewReader(`{"level":"debug"}`))
	h(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, slog.LevelDebug, lv.Level(), "LevelVar 必须真的被改成 debug")

	var body logLevelResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "debug", body.Level)
}

// TestLogLevel_PostRejectsInvalid 验证非法等级被拒且不改变现状。
func TestLogLevel_PostRejectsInvalid(t *testing.T) {
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelInfo)
	h := NewLogLevelController(lv).Handler()

	for _, bad := range []string{`{"level":"verbose"}`, `{"level":""}`, `{}`, `not-json`} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/debug/log-level", strings.NewReader(bad)))
		assert.Equal(t, http.StatusBadRequest, rec.Code, "载荷 %q 应被拒", bad)
		assert.Equal(t, slog.LevelInfo, lv.Level(), "被拒后等级不得变化")
	}
}

// TestLogLevel_RejectsUnsupportedMethod 验证只允许 GET/POST，且带 Allow 头。
func TestLogLevel_RejectsUnsupportedMethod(t *testing.T) {
	lv := new(slog.LevelVar)
	h := NewLogLevelController(lv).Handler()

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodDelete, "/debug/log-level", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "GET, POST", rec.Header().Get("Allow"))
}

// TestLogLevel_NilControllerIsSafe 验证未装配时返回 503 而非 panic。
func TestLogLevel_NilControllerIsSafe(t *testing.T) {
	var c *LogLevelController
	rec := httptest.NewRecorder()
	c.Handler()(rec, httptest.NewRequest(http.MethodGet, "/debug/log-level", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	rec2 := httptest.NewRecorder()
	NewLogLevelController(nil).Handler()(rec2, httptest.NewRequest(http.MethodGet, "/debug/log-level", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec2.Code)
}

// TestParseLogLevel_MatchesConfigVocabulary 守护运行期入参与 log.level 配置取值集合一致。
//
// 两处各有一份 switch（config.LogLevel 读配置字段、这里读运行期入参），
// 取值集合分叉会让「配置能写但接口不认」这类问题在现场才暴露，故在此钉住。
func TestParseLogLevel_MatchesConfigVocabulary(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug, "DEBUG": slog.LevelDebug,
		"info": slog.LevelInfo, "Info": slog.LevelInfo,
		"warn": slog.LevelWarn, "warning": slog.LevelWarn, "WARNING": slog.LevelWarn,
		"error": slog.LevelError, "ERROR": slog.LevelError,
		"  error  ": slog.LevelError,
	}
	for raw, want := range cases {
		got, ok := parseLogLevel(raw)
		assert.True(t, ok, "应接受 %q", raw)
		assert.Equal(t, want, got, "%q 应解析为 %v", raw, want)
	}
	for _, bad := range []string{"", "verbose", "trace", "fatal", "0"} {
		_, ok := parseLogLevel(bad)
		assert.False(t, ok, "应拒绝 %q", bad)
	}
}

// TestLevelName_RoundTrips 验证 levelName 与 parseLogLevel 互逆（四档）。
func TestLevelName_RoundTrips(t *testing.T) {
	for _, name := range []string{"debug", "info", "warn", "error"} {
		lvl, ok := parseLogLevel(name)
		require.True(t, ok)
		assert.Equal(t, name, levelName(lvl), "%q 往返后应一致", name)
	}
}
