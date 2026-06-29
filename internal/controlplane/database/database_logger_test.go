package database

import (
	"bytes"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/JianManager/internal/controlplane/config"
)

// TestGormLogger_FollowsDebugToggle 验证 FIX-7：GORM SQL 日志跟随 FR-225 调试开关——
// 级别映射正确，且非调试静默 / 调试打印 SQL、运行时切换即时生效。
//
// 对旧实现（硬编码 logger.Info）此测试会失败：非调试模式仍会打印 SELECT。
func TestGormLogger_FollowsDebugToggle(t *testing.T) {
	t.Cleanup(func() { config.SetLogLevel("info") })

	// 级别映射：debug→Info（打印 SQL），info/warn→Warn（静默 SQL）。
	config.SetLogLevel("debug")
	require.Equal(t, logger.Info, effectiveGormLevel())
	config.SetLogLevel("info")
	require.Equal(t, logger.Warn, effectiveGormLevel())
	config.SetLogLevel("warn")
	require.Equal(t, logger.Warn, effectiveGormLevel())

	var buf bytes.Buffer
	db, err := gorm.Open(sqlite.Open("file:fix1_logger?mode=memory&cache=shared"), &gorm.Config{
		Logger: newGormLogger(&buf),
	})
	require.NoError(t, err)

	type widget struct{ ID uint }
	require.NoError(t, db.AutoMigrate(&widget{}))

	// 非调试：查询不应打印 SQL。
	config.SetLogLevel("info")
	buf.Reset()
	require.NoError(t, db.Find(&[]widget{}).Error)
	require.NotContains(t, buf.String(), "SELECT", "非调试模式不应打印 SQL")

	// 调试：同样查询应打印 SQL（运行时切换即时生效）。
	config.SetLogLevel("debug")
	buf.Reset()
	require.NoError(t, db.Find(&[]widget{}).Error)
	require.Contains(t, buf.String(), "SELECT", "调试模式应打印 SQL")
}
