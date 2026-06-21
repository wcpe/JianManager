package service

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 平台设置可写白名单键（FR-063 / ADR-015）。
// 只有这些键允许经 PUT /settings 落库覆盖，其余键一律拒绝。
const (
	// SettingKeyLogLevel CP 日志级别（debug|info|warn|error）。落库即时生效（slog LevelVar）。
	SettingKeyLogLevel = "log.level"
	// SettingKeyJDKMirrorTemurin / Corretto / Zulu JDK 下载镜像源基址。
	// 实际消费点在 Worker（env JIANMANAGER_JDK_*_BASE），CP 负责存储 + 展示。
	SettingKeyJDKMirrorTemurin  = "jdk.mirror.temurin"
	SettingKeyJDKMirrorCorretto = "jdk.mirror.corretto"
	SettingKeyJDKMirrorZulu     = "jdk.mirror.zulu"
	// SettingKeyGracefulStopTimeout 优雅停止超时（Go duration 文本）。
	// 实际消费点在 Worker daemon（env JIANMANAGER_GRACEFUL_STOP_TIMEOUT），CP 负责存储 + 展示。
	SettingKeyGracefulStopTimeout = "graceful_stop.timeout"
	// SettingKeyBackupRetentionDays 默认备份保留天数（整数）。当前无裁剪消费者，CP 存储为默认值。
	SettingKeyBackupRetentionDays = "backup.retention_days"
)

var (
	// ErrSettingKeyNotWritable 键不在可写白名单内（启动固定/敏感项）。
	ErrSettingKeyNotWritable = errors.New("配置项不可运行时修改")
	// ErrSettingValueInvalid 写入值未通过该键的语义校验。
	ErrSettingValueInvalid = errors.New("配置值非法")
)

// SettingsService 平台配置服务（FR-063 / ADR-015）。
//
// 在 YAML+env 基线（cfg）之上叠加 platform_settings 的 DB 覆盖层，
// 解析「有效配置」、按白名单读写、并对可即时生效项接到真实读取点。
type SettingsService struct {
	db  *gorm.DB
	cfg *config.Config
}

// NewSettingsService 创建平台配置服务。
// 启动时把已落库的可即时生效覆盖项重放到运行时读取点（如日志级别），保证重启后覆盖仍生效。
func NewSettingsService(db *gorm.DB, cfg *config.Config) *SettingsService {
	s := &SettingsService{db: db, cfg: cfg}
	s.applyPersistedOverrides()
	return s
}

// SettingItem 单个配置项的对外表示。
type SettingItem struct {
	Key string `json:"key"`
	// Value 当前生效值（DB 覆盖 > env > YAML），敏感项已脱敏。
	Value string `json:"value"`
	// Editable 是否可经 PUT /settings 运行时修改。
	Editable bool `json:"editable"`
	// Sensitive 是否敏感项（值已脱敏，不返回明文）。
	Sensitive bool `json:"sensitive"`
	// Overridden 该项当前是否被 DB 覆盖（仅可编辑项有意义）。
	Overridden bool `json:"overridden"`
	// EffectiveImmediately 运行时修改是否在 CP 内即时生效（false 表示需改配置/重启或在 Worker 侧生效）。
	EffectiveImmediately bool `json:"effectiveImmediately"`
}

// SettingsView GET /settings 的响应：可编辑项与只读项分区。
type SettingsView struct {
	Editable []SettingItem `json:"editable"`
	ReadOnly []SettingItem `json:"readOnly"`
}

// Get 返回当前有效配置视图：可编辑项（含 DB 覆盖当前值）+ 只读项（启动固定值），敏感项脱敏。
func (s *SettingsService) Get() (*SettingsView, error) {
	overrides, err := s.loadOverrides()
	if err != nil {
		return nil, err
	}

	editable := []SettingItem{
		s.editableItem(SettingKeyLogLevel, s.cfg.Log.Level, overrides, true),
		s.editableItem(SettingKeyJDKMirrorTemurin, "https://api.adoptium.net", overrides, false),
		s.editableItem(SettingKeyJDKMirrorCorretto, "https://corretto.aws", overrides, false),
		s.editableItem(SettingKeyJDKMirrorZulu, "https://api.azul.com", overrides, false),
		s.editableItem(SettingKeyGracefulStopTimeout, "30s", overrides, false),
		s.editableItem(SettingKeyBackupRetentionDays, strconv.Itoa(s.cfg.LogStore.RetentionDays), overrides, false),
	}

	readOnly := []SettingItem{
		readOnlyItem("server.host", s.cfg.Server.Host, false),
		readOnlyItem("server.port", strconv.Itoa(s.cfg.Server.Port), false),
		readOnlyItem("grpc.port", strconv.Itoa(s.cfg.GRPC.Port), false),
		readOnlyItem("database.driver", s.cfg.Database.Driver, false),
		readOnlyItem("database.dsn", maskDSN(s.cfg.Database.DSN), true),
		readOnlyItem("jwt.secret", maskSecret(s.cfg.JWT.Secret), true),
		readOnlyItem("jwt.access_ttl", s.cfg.JWT.AccessTTL.String(), false),
		readOnlyItem("jwt.refresh_ttl", s.cfg.JWT.RefreshTTL.String(), false),
	}

	return &SettingsView{Editable: editable, ReadOnly: readOnly}, nil
}

// Update 按白名单写入一批配置覆盖，校验每个键的值语义；可即时生效项写库后立即应用。
// 任一键非法（不在白名单 / 值不合法）则整体拒绝、不落库（避免半应用）。
func (s *SettingsService) Update(values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	// 先全量校验，再统一落库 + 应用，保证原子性。
	for key, val := range values {
		if !isWritableSettingKey(key) {
			return fmt.Errorf("%w: %s", ErrSettingKeyNotWritable, key)
		}
		if err := validateSettingValue(key, val); err != nil {
			return err
		}
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		for key, val := range values {
			rec := model.PlatformSetting{Key: key, Value: val, UpdatedAt: time.Now()}
			if err := tx.Save(&rec).Error; err != nil {
				return fmt.Errorf("保存配置项失败: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// 落库成功后应用可即时生效项（CP 内读取点）。
	for key, val := range values {
		s.applyOverride(key, val)
	}
	return nil
}

// editableItem 组装可编辑项：DB 覆盖存在则用覆盖值，否则用基线默认值（base）。
func (s *SettingsService) editableItem(key, base string, overrides map[string]string, immediate bool) SettingItem {
	val := base
	_, overridden := overrides[key]
	if overridden {
		val = overrides[key]
	}
	return SettingItem{
		Key:                  key,
		Value:                val,
		Editable:             true,
		Overridden:           overridden,
		EffectiveImmediately: immediate,
	}
}

func readOnlyItem(key, value string, sensitive bool) SettingItem {
	return SettingItem{Key: key, Value: value, Editable: false, Sensitive: sensitive}
}

// loadOverrides 读取全部 DB 覆盖为 map。
func (s *SettingsService) loadOverrides() (map[string]string, error) {
	var rows []model.PlatformSetting
	if err := s.db.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询平台配置失败: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out, nil
}

// applyPersistedOverrides 启动时把已落库的可即时生效项重放到运行时读取点。
func (s *SettingsService) applyPersistedOverrides() {
	overrides, err := s.loadOverrides()
	if err != nil {
		return // 启动期容忍：查询失败则沿用 YAML/env 基线。
	}
	for key, val := range overrides {
		s.applyOverride(key, val)
	}
}

// applyOverride 把单个覆盖项应用到 CP 内的运行时读取点。
// 仅日志级别在 CP 内即时生效；其余项的消费点在 Worker（env）或暂无消费者，仅存储。
func (s *SettingsService) applyOverride(key, val string) {
	switch key {
	case SettingKeyLogLevel:
		config.SetLogLevel(val)
	}
}

// isWritableSettingKey 报告键是否在可写白名单内。
func isWritableSettingKey(key string) bool {
	switch key {
	case SettingKeyLogLevel,
		SettingKeyJDKMirrorTemurin, SettingKeyJDKMirrorCorretto, SettingKeyJDKMirrorZulu,
		SettingKeyGracefulStopTimeout, SettingKeyBackupRetentionDays:
		return true
	}
	return false
}

// validateSettingValue 按键的语义校验写入值。
func validateSettingValue(key, val string) error {
	switch key {
	case SettingKeyLogLevel:
		if !config.ValidLogLevel(val) {
			return fmt.Errorf("%w: 日志级别须为 debug|info|warn|error", ErrSettingValueInvalid)
		}
	case SettingKeyGracefulStopTimeout:
		d, err := time.ParseDuration(val)
		if err != nil || d <= 0 {
			return fmt.Errorf("%w: 优雅停止超时须为正的 Go duration（如 30s）", ErrSettingValueInvalid)
		}
	case SettingKeyBackupRetentionDays:
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 {
			return fmt.Errorf("%w: 备份保留天数须为非负整数", ErrSettingValueInvalid)
		}
	case SettingKeyJDKMirrorTemurin, SettingKeyJDKMirrorCorretto, SettingKeyJDKMirrorZulu:
		if val == "" {
			return fmt.Errorf("%w: 镜像源不能为空", ErrSettingValueInvalid)
		}
	}
	return nil
}

// maskSecret 对密钥类敏感值脱敏：保留首尾各 3 字符，中间以 *** 代替；过短则全部打码。
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 6 {
		return "******"
	}
	return s[:3] + "***" + s[len(s)-3:]
}

// maskDSN 对数据库 DSN 脱敏：sqlite 路径无凭证可原样返回；含 user:pass@ 时打掉口令段。
func maskDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	// 形如 user:pass@tcp(host)/db 的 MySQL DSN：打掉 ":pass@" 中的口令。
	at := indexByte(dsn, '@')
	colon := indexByte(dsn, ':')
	if at > 0 && colon >= 0 && colon < at {
		return dsn[:colon+1] + "***" + dsn[at:]
	}
	// sqlite 文件路径等无凭证 DSN：原样返回（不含敏感信息）。
	return dsn
}

// indexByte 返回 b 在 s 中首次出现的下标，未找到返回 -1。
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
