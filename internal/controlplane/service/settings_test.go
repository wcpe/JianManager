package service

import (
	"log/slog"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

func newSettingsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.PlatformSetting{}))
	return db
}

// testConfig 返回一份带可识别基线值的配置，便于断言「有效配置 = 基线」与「覆盖叠加」。
func testConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Server.Host = "0.0.0.0"
	cfg.Server.Port = 8080
	cfg.GRPC.Port = 9100
	cfg.Database.Driver = "sqlite"
	cfg.Database.DSN = "data/jianmanager.db"
	cfg.JWT.Secret = "dev-secret-change-me"
	cfg.JWT.AccessTTL = 15 * time.Minute
	cfg.JWT.RefreshTTL = 168 * time.Hour
	cfg.Log.Level = "info"
	cfg.LogStore.RetentionDays = 14
	return cfg
}

func findItem(items []SettingItem, key string) (SettingItem, bool) {
	for _, it := range items {
		if it.Key == key {
			return it, true
		}
	}
	return SettingItem{}, false
}

// TestGet_BaselineWhenNoOverride 无 DB 覆盖时，有效配置等于 YAML/env 基线。
func TestGet_BaselineWhenNoOverride(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())
	view, err := svc.Get()
	require.NoError(t, err)

	logLevel, ok := findItem(view.Editable, SettingKeyLogLevel)
	require.True(t, ok)
	require.Equal(t, "info", logLevel.Value)
	require.True(t, logLevel.Editable)
	require.False(t, logLevel.Overridden)
	require.True(t, logLevel.EffectiveImmediately)

	retention, ok := findItem(view.Editable, SettingKeyBackupRetentionDays)
	require.True(t, ok)
	require.Equal(t, "14", retention.Value)
}

// TestGet_DBOverrideWins DB 覆盖存在时，有效值取覆盖值（DB > env > YAML）。
func TestGet_DBOverrideWins(t *testing.T) {
	db := newSettingsTestDB(t)
	require.NoError(t, db.Save(&model.PlatformSetting{Key: SettingKeyLogLevel, Value: "debug"}).Error)

	svc := NewSettingsService(db, testConfig())
	view, err := svc.Get()
	require.NoError(t, err)

	logLevel, ok := findItem(view.Editable, SettingKeyLogLevel)
	require.True(t, ok)
	require.Equal(t, "debug", logLevel.Value) // 覆盖值压过基线 info
	require.True(t, logLevel.Overridden)
}

// TestGet_SensitiveMasked 敏感只读项（jwt secret / db dsn）脱敏，不返回明文。
func TestGet_SensitiveMasked(t *testing.T) {
	cfg := testConfig()
	cfg.JWT.Secret = "dev-secret-change-me"
	cfg.Database.DSN = "root:supersecret@tcp(127.0.0.1:3306)/jm"
	svc := NewSettingsService(newSettingsTestDB(t), cfg)
	view, err := svc.Get()
	require.NoError(t, err)

	secret, ok := findItem(view.ReadOnly, "jwt.secret")
	require.True(t, ok)
	require.True(t, secret.Sensitive)
	require.NotContains(t, secret.Value, "secret-change") // 明文不外泄
	require.NotEqual(t, "dev-secret-change-me", secret.Value)

	dsn, ok := findItem(view.ReadOnly, "database.dsn")
	require.True(t, ok)
	require.NotContains(t, dsn.Value, "supersecret") // 口令段被打码
}

// TestUpdate_PersistsAndOverrides 写入白名单键后落库，再读有效配置取到覆盖值。
func TestUpdate_PersistsAndOverrides(t *testing.T) {
	db := newSettingsTestDB(t)
	svc := NewSettingsService(db, testConfig())

	require.NoError(t, svc.Update(map[string]string{
		SettingKeyBackupRetentionDays: "30",
		SettingKeyGracefulStopTimeout: "45s",
	}))

	view, err := svc.Get()
	require.NoError(t, err)
	retention, _ := findItem(view.Editable, SettingKeyBackupRetentionDays)
	require.Equal(t, "30", retention.Value)
	require.True(t, retention.Overridden)
	timeout, _ := findItem(view.Editable, SettingKeyGracefulStopTimeout)
	require.Equal(t, "45s", timeout.Value)

	// 确认确实落库。
	var cnt int64
	require.NoError(t, db.Model(&model.PlatformSetting{}).Count(&cnt).Error)
	require.Equal(t, int64(2), cnt)
}

// TestUpdate_LogLevelTakesEffectImmediately 写入 log.level 后 CP 日志级别即时切换（接到 LevelVar 读取点）。
func TestUpdate_LogLevelTakesEffectImmediately(t *testing.T) {
	// 先复位为 info，避免其他用例污染。
	config.SetLogLevel("info")
	require.False(t, config.LogLevelVar.Level() == slog.LevelDebug)

	svc := NewSettingsService(newSettingsTestDB(t), testConfig())
	require.NoError(t, svc.Update(map[string]string{SettingKeyLogLevel: "debug"}))
	require.Equal(t, slog.LevelDebug, config.LogLevelVar.Level()) // 运行时已生效

	config.SetLogLevel("info") // 复位，避免影响后续用例
}

// TestUpdate_RejectsNonWhitelistKey 非白名单键（只读/敏感项）被拒，且不落库。
func TestUpdate_RejectsNonWhitelistKey(t *testing.T) {
	db := newSettingsTestDB(t)
	svc := NewSettingsService(db, testConfig())

	err := svc.Update(map[string]string{"server.port": "9999"})
	require.ErrorIs(t, err, ErrSettingKeyNotWritable)

	var cnt int64
	require.NoError(t, db.Model(&model.PlatformSetting{}).Count(&cnt).Error)
	require.Equal(t, int64(0), cnt) // 拒绝即不落库
}

// TestUpdate_RejectsInvalidValue 值不合法（日志级别非枚举、超时非正、保留天数为负）被拒。
func TestUpdate_RejectsInvalidValue(t *testing.T) {
	svc := NewSettingsService(newSettingsTestDB(t), testConfig())

	require.ErrorIs(t, svc.Update(map[string]string{SettingKeyLogLevel: "verbose"}), ErrSettingValueInvalid)
	require.ErrorIs(t, svc.Update(map[string]string{SettingKeyGracefulStopTimeout: "-5s"}), ErrSettingValueInvalid)
	require.ErrorIs(t, svc.Update(map[string]string{SettingKeyGracefulStopTimeout: "notaduration"}), ErrSettingValueInvalid)
	require.ErrorIs(t, svc.Update(map[string]string{SettingKeyBackupRetentionDays: "-1"}), ErrSettingValueInvalid)
}

// TestUpdate_AtomicOnPartialInvalid 一批中有非法键时整体拒绝、合法键也不落库（原子性）。
func TestUpdate_AtomicOnPartialInvalid(t *testing.T) {
	db := newSettingsTestDB(t)
	svc := NewSettingsService(db, testConfig())

	err := svc.Update(map[string]string{
		SettingKeyBackupRetentionDays: "30",        // 合法
		"jwt.secret":                  "leaked",    // 非法（只读/敏感）
	})
	require.Error(t, err)

	var cnt int64
	require.NoError(t, db.Model(&model.PlatformSetting{}).Count(&cnt).Error)
	require.Equal(t, int64(0), cnt) // 合法项也没落库
}

// TestNewService_ReplaysPersistedLogLevel 启动时把已落库的 log.level 覆盖重放到读取点（重启后仍生效）。
func TestNewService_ReplaysPersistedLogLevel(t *testing.T) {
	config.SetLogLevel("info")
	db := newSettingsTestDB(t)
	require.NoError(t, db.Save(&model.PlatformSetting{Key: SettingKeyLogLevel, Value: "warn"}).Error)

	_ = NewSettingsService(db, testConfig()) // 构造即重放
	require.Equal(t, slog.LevelWarn, config.LogLevelVar.Level())

	config.SetLogLevel("info") // 复位
}
