package service

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/config"
	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/platform/httpclient"
)

// 平台设置可写白名单键（FR-063 / ADR-015）。
// 只有这些键允许经 PUT /settings 落库覆盖，其余键一律拒绝。
const (
	// SettingKeyLogLevel CP 日志级别（debug|info|warn|error）。落库即时生效（slog LevelVar）。
	SettingKeyLogLevel = "log.level"
	// SettingKeyJDKMirrorTemurin / Corretto / Zulu JDK 下载镜像源基址。
	// 安装 JDK 时 CP 取生效值经 InstallJDKRequest.mirror_base 下发 Worker，使配置真生效（FR-063）。
	SettingKeyJDKMirrorTemurin  = "jdk.mirror.temurin"
	SettingKeyJDKMirrorCorretto = "jdk.mirror.corretto"
	SettingKeyJDKMirrorZulu     = "jdk.mirror.zulu"
	// SettingKeyGracefulStopTimeout 优雅停止超时（Go duration 文本）。
	// 启动实例时 CP 取生效值经 CreateInstanceRequest 下发 Worker→wrapper，对其后新启动的实例生效（FR-063）。
	SettingKeyGracefulStopTimeout = "graceful_stop.timeout"
	// SettingKeyBackupRetentionDays 默认备份保留天数（整数）。CP 后台巡检据此裁剪超期备份（FR-063）。
	SettingKeyBackupRetentionDays = "backup.retention_days"
	// SettingKeyProxyURL CP 出站代理地址（network 类，FR-185/ADR-043）。敏感（脱敏展示）。
	// 落库即重建 CP 出站持有者（优先级 DB > control-plane.yaml > env）；同时作为各节点默认代理。
	SettingKeyProxyURL = "proxy.url"
	// SettingKeyProxyNoProxy CP/全局默认出站代理的免代理列表（逗号分隔，语义同 NO_PROXY，FR-185）。
	SettingKeyProxyNoProxy = "proxy.no_proxy"
)

var (
	// ErrSettingKeyNotWritable 键不在可写白名单内（启动固定/敏感项）。
	ErrSettingKeyNotWritable = errors.New("配置项不可运行时修改")
	// ErrSettingValueInvalid 写入值未通过该键的语义校验。
	ErrSettingValueInvalid = errors.New("配置值非法")
)

// SettingsReader 暴露「按键取生效值」给其它服务（JDK 安装、实例启动等），
// 使它们读取平台设置的覆盖而无需依赖整个 SettingsService。*SettingsService 实现该接口。
// 为 nil 时消费方须自行回退到各自的默认/本地配置（依赖注入可选）。
type SettingsReader interface {
	// EffectiveValue 返回某键当前生效值（DB 覆盖 > 基线默认）。
	EffectiveValue(key string) string
}

// SettingsService 平台配置服务（FR-063 / ADR-015）。
//
// 在 YAML+env 基线（cfg）之上叠加 platform_settings 的 DB 覆盖层，
// 解析「有效配置」、按白名单读写、并对可即时生效项接到真实读取点。
type SettingsService struct {
	db  *gorm.DB
	cfg *config.Config
	// proxyRebuilder 在 proxy.* 覆盖落库后被调用，令 CP 重建出站持有者使新代理即时生效（FR-185）。
	// 由 main 注入（传入重建 httpclient.Provider 的闭包）；为 nil 时不重建（仅落库，下次重启生效）。
	proxyRebuilder func(httpclient.Config)
}

// NewSettingsService 创建平台配置服务。
// 启动时把已落库的可即时生效覆盖项重放到运行时读取点（如日志级别），保证重启后覆盖仍生效。
func NewSettingsService(db *gorm.DB, cfg *config.Config) *SettingsService {
	s := &SettingsService{db: db, cfg: cfg}
	s.applyPersistedOverrides()
	return s
}

// SetProxyRebuilder 注入 CP 出站持有者重建回调（FR-185，见 ADR-043）。
// 保存 proxy.url/proxy.no_proxy 后以「当前生效代理」（DB 覆盖 > yaml > env）回调之，
// 令 CP 自身出站立即走新代理、免重启。不注入则代理覆盖仅落库、下次重启生效。
func (s *SettingsService) SetProxyRebuilder(fn func(httpclient.Config)) {
	s.proxyRebuilder = fn
}

// EffectiveProxy 返回 CP 当前生效的出站代理配置（FR-185，见 ADR-043）。
// 优先级：settings DB 覆盖（全局） > control-plane.yaml proxy > 环境变量（空配回退）。
// 同时作为各节点的「全局默认代理」（节点 inherit 时下发此值）。
func (s *SettingsService) EffectiveProxy() httpclient.Config {
	url := s.cfg.Proxy.URL
	noProxy := s.cfg.Proxy.NoProxy
	if overrides, err := s.loadOverrides(); err == nil {
		if v, ok := overrides[SettingKeyProxyURL]; ok {
			url = v
		}
		if v, ok := overrides[SettingKeyProxyNoProxy]; ok {
			noProxy = v
		}
	}
	return httpclient.Config{URL: url, NoProxy: noProxy}
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
		s.editableItem(SettingKeyLogLevel, s.defaultValue(SettingKeyLogLevel), overrides, true),
		s.editableItem(SettingKeyJDKMirrorTemurin, s.defaultValue(SettingKeyJDKMirrorTemurin), overrides, false),
		s.editableItem(SettingKeyJDKMirrorCorretto, s.defaultValue(SettingKeyJDKMirrorCorretto), overrides, false),
		s.editableItem(SettingKeyJDKMirrorZulu, s.defaultValue(SettingKeyJDKMirrorZulu), overrides, false),
		s.editableItem(SettingKeyGracefulStopTimeout, s.defaultValue(SettingKeyGracefulStopTimeout), overrides, false),
		s.editableItem(SettingKeyBackupRetentionDays, s.defaultValue(SettingKeyBackupRetentionDays), overrides, false),
		// 出站代理（network 类，FR-185/ADR-043）：保存即在 CP 内重建出站持有者（即时生效）。
		// proxy.url 标 sensitive：含凭据时回显脱敏（仅展示 scheme://host:port），不外泄明文密码。
		s.proxyURLItem(overrides),
		s.editableItem(SettingKeyProxyNoProxy, s.defaultValue(SettingKeyProxyNoProxy), overrides, true),
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

	// 本批涉及出站代理键 → 以当前生效代理（DB 覆盖 > yaml > env）重建 CP 出站持有者（FR-185/ADR-043）。
	// 单独于 applyOverride：重建不依赖单个键值、而依赖「全部覆盖叠加后的生效代理」，故落库后整体算一次。
	if _, ok := values[SettingKeyProxyURL]; ok {
		s.rebuildProxy()
	} else if _, ok := values[SettingKeyProxyNoProxy]; ok {
		s.rebuildProxy()
	}
	return nil
}

// rebuildProxy 以当前生效代理重建 CP 出站持有者（FR-185）。rebuilder 未注入时静默跳过（仅落库）。
func (s *SettingsService) rebuildProxy() {
	if s.proxyRebuilder == nil {
		return
	}
	s.proxyRebuilder(s.EffectiveProxy())
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

// proxyURLItem 组装出站代理地址项（FR-185）：可编辑 + sensitive，回显时脱敏含凭据的 URL，
// 保存在 CP 内即时生效（重建出站持有者）。
func (s *SettingsService) proxyURLItem(overrides map[string]string) SettingItem {
	val, overridden := overrides[SettingKeyProxyURL]
	if !overridden {
		val = s.defaultValue(SettingKeyProxyURL)
	}
	return SettingItem{
		Key:                  SettingKeyProxyURL,
		Value:                httpclient.Sanitize(val), // 脱敏：不回显明文 user:pass
		Editable:             true,
		Sensitive:            true,
		Overridden:           overridden,
		EffectiveImmediately: true,
	}
}

func readOnlyItem(key, value string, sensitive bool) SettingItem {
	return SettingItem{Key: key, Value: value, Editable: false, Sensitive: sensitive}
}

// defaultValue 返回某键的基线默认值（DB 无覆盖时的生效值）。
// 单点定义，供 Get（展示）与 EffectiveValue（消费）共享，避免默认值在两处漂移。
func (s *SettingsService) defaultValue(key string) string {
	switch key {
	case SettingKeyLogLevel:
		return s.cfg.Log.Level
	case SettingKeyJDKMirrorTemurin:
		return "https://api.adoptium.net"
	case SettingKeyJDKMirrorCorretto:
		return "https://corretto.aws"
	case SettingKeyJDKMirrorZulu:
		return "https://api.azul.com"
	case SettingKeyGracefulStopTimeout:
		return "30s"
	case SettingKeyBackupRetentionDays:
		return strconv.Itoa(s.cfg.LogStore.RetentionDays)
	case SettingKeyProxyURL:
		return s.cfg.Proxy.URL
	case SettingKeyProxyNoProxy:
		return s.cfg.Proxy.NoProxy
	}
	return ""
}

// EffectiveValue 返回某键当前生效值（DB 覆盖 > 基线默认）。
// CP 各消费点（JDK 安装、备份裁剪、实例启动）据此读取单项设置，使覆盖真生效。
// 查询失败或键无默认时回退默认值，保证消费方始终拿到可用值（不因 DB 故障卡死）。
func (s *SettingsService) EffectiveValue(key string) string {
	if overrides, err := s.loadOverrides(); err == nil {
		if v, ok := overrides[key]; ok {
			return v
		}
	}
	return s.defaultValue(key)
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
// 仅日志级别在 CP 内即时生效（EffectiveImmediately）；其余项在各自动作发生时按需读取生效值：
// JDK 镜像源随安装下发、优雅停止超时随启动下发、备份保留天数由后台巡检读取（均经 EffectiveValue）。
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
		SettingKeyGracefulStopTimeout, SettingKeyBackupRetentionDays,
		SettingKeyProxyURL, SettingKeyProxyNoProxy:
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
	case SettingKeyProxyURL:
		// 复用 httpclient 的 URL/scheme 校验：空=清除代理覆盖（合法，回退 yaml/env）；
		// 非空但非法（不支持 scheme / 不可解析）则拒绝，不静默直连（FR-185/ADR-043）。
		if val != "" {
			if _, err := httpclient.New(httpclient.Config{URL: val}); err != nil {
				return fmt.Errorf("%w: 代理地址非法（%v）", ErrSettingValueInvalid, err)
			}
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
