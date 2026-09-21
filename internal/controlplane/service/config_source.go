package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/internal/controlplane/service/schema"
)

// 受管配置项 ItemKey（FR-451）。稳定标识，分三类前缀：
//   - startup. 启动项（写 Instance.StartCommand / LaunchSpec）
//   - props.   server.properties 项（写文件对应键）
//   - flags.   预留（未来 JVM 参数）
const (
	ConfigItemStartupCommand    = "startup.command"
	ConfigItemStartupLaunchSpec = "startup.launchSpec"
	// ConfigItemStartupPrefix 启动项前缀（startup.*）。
	ConfigItemStartupPrefix = "startup."
	ConfigItemPropsPrefix   = "props."
	ConfigItemFlagsPrefix   = "flags."

	// defaultServerPropertiesPath 是内联 props 项的固定落点（切回内联后平台只写标准路径，避免写出两个文件）。
	defaultServerPropertiesPath = "server.properties"

	// maxStartupFilePreviewLen 整文件引用生效值预览的截断长度（FR-451 nit：避免整份文件刷屏）。
	maxStartupFilePreviewLen = 512
)

// surfacePropKeys 是明面化的 server.properties 关键项（顺序即渲染顺序）。
// 复用既有 schema 注册表（schema.ServerPropertiesModel）的 Group/Description/Choices 渲染，不另建真源。
var surfacePropKeys = []string{
	"server-port",
	"enable-query",
	"query.port",
	"view-distance",
	"max-players",
	"motd",
	"online-mode",
}

// forbiddenPortProps 是需要并入跨实例端口唯一性校验的 props 键。
var forbiddenPortProps = []string{"server-port", "query.port"}

// SurfacePropKeys 返回受管的 server.properties 关键项键集合（副本，供路由侧冲突检测）。
func SurfacePropKeys() []string {
	return append([]string(nil), surfacePropKeys...)
}

// managedPropSet 受管 props 键集合。
var managedPropSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(surfacePropKeys))
	for _, k := range surfacePropKeys {
		m[k] = struct{}{}
	}
	return m
}()

// SurfaceItem 是受管配置项清单中的一行（FR-451）。
type SurfaceItem struct {
	ItemKey string                 `json:"itemKey"`
	Source  model.ConfigSourceKind `json:"source"`
	// Registered 是否已在登记表显式声明；false 表示旧实例的隐含默认（启动项内联 / props 文件隐含）。
	Registered      bool     `json:"registered"`
	InlineValue     string   `json:"inlineValue,omitempty"`
	FilePath        string   `json:"filePath,omitempty"`
	FileKey         string   `json:"fileKey,omitempty"`
	EffectiveValue  string   `json:"effectiveValue"`
	EffectiveSource string   `json:"effectiveSource"` // inline | file
	Editable        bool     `json:"editable"`
	PreviewError    string   `json:"previewError,omitempty"`
	Group           string   `json:"group,omitempty"`
	Description     string   `json:"description,omitempty"`
	Type            string   `json:"type,omitempty"`
	Choices         []string `json:"choices,omitempty"`
}

// SurfaceUpdateItem 是按项更新来源的请求体。
type SurfaceUpdateItem struct {
	ItemKey     string                 `json:"itemKey"`
	Source      model.ConfigSourceKind `json:"source"`
	InlineValue *string                `json:"inlineValue"`
	FilePath    string                 `json:"filePath"`
	FileKey     string                 `json:"fileKey"`
	Message     string                 `json:"message"`
}

// ConfigSourceService 实现受管配置项登记表 CRUD、二态互斥与迁移语义、生效值预览、冲突防护（FR-451）。
type ConfigSourceService struct {
	db       *gorm.DB
	instance *InstanceService
	config   *ConfigService
	// previewFn 生效值预览口子，默认为 nil（走 config.Read）；测试可注入以绕过 Worker。
	previewFn func(inst *model.Instance, filePath string) (string, map[string]string, error)
}

// NewConfigSourceService 创建配置源服务。
func NewConfigSourceService(db *gorm.DB, instance *InstanceService, config *ConfigService) *ConfigSourceService {
	return &ConfigSourceService{db: db, instance: instance, config: config}
}

// SetPreviewForTest 注入生效值预览实现（仅供测试）。
func (s *ConfigSourceService) SetPreviewForTest(fn func(inst *model.Instance, filePath string) (string, map[string]string, error)) {
	s.previewFn = fn
}

// previewResult 是单文件的生效值预览缓存。
type previewResult struct {
	content string
	values  map[string]string
	err     error
}

// Surface 返回实例的受管配置项清单（FR-451）。
// 旧实例登记表为空时：startup.command 视作 inline（取 Instance.StartCommand），关键 props 视作文件隐含，
// 保证平滑过渡、不报错。file 项的 effectiveValue 为解析预览（只读）。
//
// 能力画像过滤（FR-451 验收 #8 / ADR-091）：startup.* 与 props.* 均为 MC 语义配置项，仅对具备
// MC 配置语义的实例（minecraft_java 且非配套服务角色）呈现；generic / beacon 返回空清单，
// 使其走各自画像，不出现 MC 专有配置项。
func (s *ConfigSourceService) Surface(instanceID uint) ([]SurfaceItem, error) {
	inst, err := s.getInstance(instanceID)
	if err != nil {
		return nil, err
	}
	if !mcSemanticConfigSurface(inst) {
		return []SurfaceItem{}, nil
	}
	rows, err := s.loadSources(instanceID)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]model.InstanceConfigSource, len(rows))
	for _, r := range rows {
		byKey[r.ItemKey] = r
	}

	// 预览缓存：同一文件只读一次，避免多 props 项重复拉取。
	cache := map[string]previewResult{}
	preview := func(path string) previewResult {
		if pr, ok := cache[path]; ok {
			return pr
		}
		c, v, e := s.readFilePreview(inst, path)
		pr := previewResult{content: c, values: v, err: e}
		cache[path] = pr
		return pr
	}

	items := make([]SurfaceItem, 0, len(surfacePropKeys)+2)
	items = append(items, s.surfaceStartupItem(ConfigItemStartupCommand, inst, byKey, preview))
	items = append(items, s.surfaceStartupItem(ConfigItemStartupLaunchSpec, inst, byKey, preview))
	for _, pk := range surfacePropKeys {
		items = append(items, s.surfacePropItem(pk, byKey, preview))
	}
	return items, nil
}

func (s *ConfigSourceService) surfaceStartupItem(itemKey string, inst *model.Instance, byKey map[string]model.InstanceConfigSource, preview func(string) previewResult) SurfaceItem {
	item := SurfaceItem{ItemKey: itemKey, Group: "启动参数", Type: "string"}
	switch itemKey {
	case ConfigItemStartupCommand:
		item.Description = "服务端启动命令（对下次启动生效）"
	case ConfigItemStartupLaunchSpec:
		item.Description = "MC 结构化启动规格（JSON，对下次启动生效）"
	}
	row, ok := byKey[itemKey]
	if !ok || row.Source == "" {
		item.Source = model.ConfigSourceInline
		item.InlineValue = startupInstanceValue(inst, itemKey)
		item.EffectiveValue = item.InlineValue
		item.EffectiveSource = string(model.ConfigSourceInline)
		item.Editable = true
		return item
	}
	item.Registered = true
	return s.resolveSourceItem(item, row, preview, func() string { return startupInstanceValue(inst, itemKey) })
}

func (s *ConfigSourceService) surfacePropItem(propKey string, byKey map[string]model.InstanceConfigSource, preview func(string) previewResult) SurfaceItem {
	meta := schema.ServerPropertiesModel().Fields[propKey]
	item := SurfaceItem{
		ItemKey:     ConfigItemPropsPrefix + propKey,
		Group:       meta.Group,
		Description: meta.Description,
		Type:        meta.Type,
		Choices:     meta.Choices,
	}
	row, ok := byKey[item.ItemKey]
	if !ok || row.Source == "" {
		// 未登记：文件隐含（server.properties 为真源），只读预览。
		item.Source = model.ConfigSourceFile
		item.FilePath = defaultServerPropertiesPath
		item.FileKey = propKey
		item.EffectiveSource = "file"
		item.Editable = false
		pr := preview(defaultServerPropertiesPath)
		if pr.err != nil {
			item.PreviewError = pr.err.Error()
		} else {
			item.EffectiveValue = pr.values[propKey]
		}
		return item
	}
	item.Registered = true
	return s.resolveSourceItem(item, row, preview, nil)
}

// resolveSourceItem 按登记来源解析单项的生效值与可编辑性。
func (s *ConfigSourceService) resolveSourceItem(item SurfaceItem, row model.InstanceConfigSource, preview func(string) previewResult, inlineFallback func() string) SurfaceItem {
	item.Source = row.Source
	item.FilePath = row.FilePath
	item.FileKey = row.FileKey
	switch row.Source {
	case model.ConfigSourceFile:
		path := row.FilePath
		if path == "" {
			path = defaultServerPropertiesPath
		}
		item.FilePath = path
		item.EffectiveSource = "file"
		item.Editable = false
		pr := preview(path)
		if pr.err != nil {
			item.PreviewError = pr.err.Error()
			return item
		}
		if row.FileKey != "" {
			item.EffectiveValue = pr.values[row.FileKey]
		} else {
			// 整文件引用（当前仅 props 全文件引用可达；startup 引用已被拒）：
			// effectiveValue 展示被引用文件正文的截断预览，避免整份文件刷屏。
			item.EffectiveValue = truncatePreview(strings.TrimSpace(pr.content), maxStartupFilePreviewLen)
		}
		return item
	default: // inline
		v := row.InlineValue
		if v == "" && inlineFallback != nil {
			v = inlineFallback()
		}
		item.InlineValue = v
		item.EffectiveValue = v
		item.EffectiveSource = "inline"
		item.Editable = true
		return item
	}
}

// UpdateSource 按项更新配置源（FR-451）。返回刷新后的清单。
//   - source=inline 且 startup.* → 经 InstanceService.Update 写 StartCommand/LaunchSpec。
//   - source=inline 且 props.*  → 经 ConfigService.WriteFields 写 server.properties（保留注释/顺序、生成版本）。
//   - source=file               → 仅写登记表（+ 预览校验文件/键，离线时降级为警告不阻断）。
//
// 迁移语义：file→inline 以文件当前生效值为初值；inline→file 不删除文件中原值。
func (s *ConfigSourceService) UpdateSource(instanceID uint, items []SurfaceUpdateItem, authorID uint) ([]SurfaceItem, error) {
	inst, err := s.getInstance(instanceID)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if err := s.applySourceUpdate(inst, it, authorID); err != nil {
			return nil, err
		}
	}
	return s.Surface(instanceID)
}

func (s *ConfigSourceService) applySourceUpdate(inst *model.Instance, it SurfaceUpdateItem, authorID uint) error {
	if !isManagedItemKey(it.ItemKey) {
		return fmt.Errorf("未知受管配置项: %s", it.ItemKey)
	}
	// 画像门控（FR-451 验收 #8）：无 MC 配置语义的实例（generic / beacon）不接受 MC 专有受管项写入。
	if !mcSemanticConfigSurface(inst) {
		return fmt.Errorf("实例 %d（type=%s role=%s）无 MC 配置语义，不支持受管项 %s", inst.ID, inst.Type, inst.Role, it.ItemKey)
	}
	if !model.ValidConfigSourceKind(it.Source) {
		return fmt.Errorf("非法配置来源: %s", it.Source)
	}
	var prev model.InstanceConfigSource
	prevErr := s.db.Where("instance_id = ? AND item_key = ?", inst.ID, it.ItemKey).First(&prev).Error
	hasPrev := prevErr == nil
	if prevErr != nil && !errors.Is(prevErr, gorm.ErrRecordNotFound) {
		return prevErr
	}

	row := model.InstanceConfigSource{InstanceID: inst.ID, ItemKey: it.ItemKey, Source: it.Source}
	switch it.Source {
	case model.ConfigSourceInline:
		value, err := s.resolveInlineValue(inst, it, prev, hasPrev)
		if err != nil {
			return err
		}
		if err := s.applyInline(inst, it.ItemKey, value, authorID); err != nil {
			return err
		}
		row.InlineValue = value
	case model.ConfigSourceFile:
		path, fileKey, err := s.resolveFileRef(it)
		if err != nil {
			return err
		}
		// 预览校验文件/键存在；离线（Worker 未连）时降级为不阻断，登记照常落库。
		s.validateFileRef(inst, path, fileKey)
		row.FilePath = path
		row.FileKey = fileKey
	}
	return s.upsertSource(row)
}

// resolveInlineValue 计算 inline 模式应写入的值：
//   - 显式传入 inlineValue → 用之；
//   - 上一态为 file → 以文件当前生效值为初值（迁移基线）；
//   - 其余 → 启动项取实例现值、props 项沿用已有内联值。
func (s *ConfigSourceService) resolveInlineValue(inst *model.Instance, it SurfaceUpdateItem, prev model.InstanceConfigSource, hasPrev bool) (string, error) {
	if it.InlineValue != nil {
		return *it.InlineValue, nil
	}
	if hasPrev && prev.Source == model.ConfigSourceFile {
		return s.previewFileValue(inst, prev.FilePath, prev.FileKey), nil
	}
	if strings.HasPrefix(it.ItemKey, ConfigItemPropsPrefix) {
		if hasPrev {
			return prev.InlineValue, nil
		}
		return "", nil
	}
	return startupInstanceValue(inst, it.ItemKey), nil
}

// resolveFileRef 归一化 file 引用的路径与键。
func (s *ConfigSourceService) resolveFileRef(it SurfaceUpdateItem) (string, string, error) {
	// 启动项不接受文件引用来源（FR-451 审计）：startup.* 登记为 file 后既不读文件也无消费路径，
	// 启动仍用 Instance.StartCommand。故显式拒绝，UI 侧同步隐藏启动项的「引用文件」切换。
	if strings.HasPrefix(it.ItemKey, ConfigItemStartupPrefix) {
		return "", "", fmt.Errorf("启动项 %s 不支持文件引用来源：启动命令须由平台持有（对下次启动生效）", it.ItemKey)
	}
	isProps := strings.HasPrefix(it.ItemKey, ConfigItemPropsPrefix)
	path := strings.TrimSpace(it.FilePath)
	if path == "" {
		if !isProps {
			return "", "", fmt.Errorf("启动项的文件引用需指定 filePath")
		}
		path = defaultServerPropertiesPath
	}
	fileKey := strings.TrimSpace(it.FileKey)
	if fileKey == "" && isProps {
		fileKey = strings.TrimPrefix(it.ItemKey, ConfigItemPropsPrefix)
	}
	if isProps && fileKey == "" {
		return "", "", fmt.Errorf("props 文件引用需指定 fileKey")
	}
	return path, fileKey, nil
}

// validateFileRef 预览校验文件/键存在，仅记日志不阻断（Worker 离线时无法预览属正常）。
func (s *ConfigSourceService) validateFileRef(inst *model.Instance, path, fileKey string) {
	_, vals, err := s.readFilePreview(inst, path)
	if err != nil {
		slog.Warn("配置源文件引用预览失败（登记照常落库）", "instanceId", inst.ID, "path", path, "error", err)
		return
	}
	if fileKey != "" {
		if _, ok := vals[fileKey]; !ok {
			slog.Warn("配置源文件引用键在文件中不存在", "instanceId", inst.ID, "path", path, "fileKey", fileKey)
		}
	}
}

// applyInline 把内联值写到底层真源（启动项经 Update、props 项经 WriteFields）。
func (s *ConfigSourceService) applyInline(inst *model.Instance, itemKey, value string, authorID uint) error {
	switch itemKey {
	case ConfigItemStartupCommand:
		if s.instance == nil {
			return errors.New("实例服务未注入")
		}
		_, err := s.instance.Update(inst.ID, UpdateInstanceFields{StartCommand: &value})
		return err
	case ConfigItemStartupLaunchSpec:
		if s.instance == nil {
			return errors.New("实例服务未注入")
		}
		// launchSpec 以纯字符串暴露，写入前做 JSON 校验，避免落库非法 JSON（FR-451 nit）。
		if trimmed := strings.TrimSpace(value); trimmed != "" && !json.Valid([]byte(trimmed)) {
			return fmt.Errorf("startup.launchSpec 必须是合法 JSON")
		}
		_, err := s.instance.Update(inst.ID, UpdateInstanceFields{LaunchSpec: &value})
		return err
	}
	if strings.HasPrefix(itemKey, ConfigItemPropsPrefix) {
		if s.config == nil {
			return errors.New("配置服务未注入")
		}
		key := strings.TrimPrefix(itemKey, ConfigItemPropsPrefix)
		_, _, err := s.config.WriteFields(inst.ID, defaultServerPropertiesPath,
			map[string]string{key: value}, fmt.Sprintf("配置源内联写入 %s", itemKey), authorID)
		return err
	}
	return fmt.Errorf("未知受管配置项: %s", itemKey)
}

// upsertSource 落库登记表（存在则更新，不存在则新建）。
func (s *ConfigSourceService) upsertSource(row model.InstanceConfigSource) error {
	var existing model.InstanceConfigSource
	err := s.db.Where("instance_id = ? AND item_key = ?", row.InstanceID, row.ItemKey).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.Create(&row).Error
	}
	if err != nil {
		return err
	}
	existing.Source = row.Source
	existing.InlineValue = row.InlineValue
	existing.FilePath = row.FilePath
	existing.FileKey = row.FileKey
	return s.db.Save(&existing).Error
}

// InlinePropValues 返回实例上以 inline 登记的属性值（键→值），供跨实例端口校验并入（FR-451 §2.4）。
func (s *ConfigSourceService) InlinePropValues(instanceID uint, keys []string) map[string]string {
	out := map[string]string{}
	if len(keys) == 0 || s.db == nil {
		return out
	}
	itemKeys := make([]string, 0, len(keys))
	for _, k := range keys {
		itemKeys = append(itemKeys, ConfigItemPropsPrefix+k)
	}
	var rows []model.InstanceConfigSource
	if err := s.db.Where("instance_id = ? AND source = ? AND item_key IN ?",
		instanceID, model.ConfigSourceInline, itemKeys).Find(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[strings.TrimPrefix(r.ItemKey, ConfigItemPropsPrefix)] = r.InlineValue
	}
	return out
}

// ConflictWarnings 返回与受管 file 项冲突的写入警告（FR-451 §2.2）。
// 命中受管 file 项时不静默覆盖，仅提示「平台编辑器改动可能与生效值不一致」，与 CrossCheck 同响应风格。
func (s *ConfigSourceService) ConflictWarnings(instanceID uint, filePath string, keys []string) []map[string]any {
	out := []map[string]any{}
	if len(keys) == 0 || s.db == nil {
		return out
	}
	itemKeys := make([]string, 0, len(keys))
	for _, k := range keys {
		itemKeys = append(itemKeys, ConfigItemPropsPrefix+k)
	}
	var rows []model.InstanceConfigSource
	if err := s.db.Where("instance_id = ? AND source = ? AND item_key IN ?",
		instanceID, model.ConfigSourceFile, itemKeys).Find(&rows).Error; err != nil {
		return out
	}
	base := strings.ToLower(filepath.Base(filePath))
	for _, r := range rows {
		if rp := strings.ToLower(filepath.Base(r.FilePath)); rp != "" && rp != base {
			continue // 引用的是别的文件，不算冲突
		}
		key := strings.TrimPrefix(r.ItemKey, ConfigItemPropsPrefix)
		out = append(out, map[string]any{
			"level":   "warning",
			"key":     key,
			"message": fmt.Sprintf("配置项 %s 已声明由文件 %s 引用，平台编辑器改动可能与生效值不一致", r.ItemKey, r.FilePath),
		})
	}
	return out
}

// readFilePreview 读取文件正文与键值映射（供生效值预览）。
func (s *ConfigSourceService) readFilePreview(inst *model.Instance, filePath string) (string, map[string]string, error) {
	if s.previewFn != nil {
		return s.previewFn(inst, filePath)
	}
	if s.config == nil {
		return "", nil, errors.New("配置服务未注入")
	}
	res, err := s.config.Read(inst.ID, filePath)
	if err != nil {
		return "", nil, err
	}
	fields := schema.BuildFields(res.Format, res.Content)
	if m := schema.MatchPath(filePath); m != nil {
		fields = schema.ApplyTypes(fields, m)
	}
	vals := make(map[string]string, len(fields))
	for _, f := range fields {
		vals[f.Key] = f.Value
	}
	return res.Content, vals, nil
}

// previewFileValue 读取文件引用项的当前生效值（file→inline 迁移初值）。
func (s *ConfigSourceService) previewFileValue(inst *model.Instance, filePath, fileKey string) string {
	if filePath == "" {
		filePath = defaultServerPropertiesPath
	}
	_, vals, err := s.readFilePreview(inst, filePath)
	if err != nil {
		return ""
	}
	return vals[fileKey]
}

func (s *ConfigSourceService) getInstance(instanceID uint) (*model.Instance, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, fmt.Errorf("查询实例失败: %w", err)
	}
	return &inst, nil
}

func (s *ConfigSourceService) loadSources(instanceID uint) ([]model.InstanceConfigSource, error) {
	var rows []model.InstanceConfigSource
	if err := s.db.Where("instance_id = ?", instanceID).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// startupInstanceValue 取启动项在实例上的当前值。
func startupInstanceValue(inst *model.Instance, itemKey string) string {
	switch itemKey {
	case ConfigItemStartupCommand:
		return inst.StartCommand
	case ConfigItemStartupLaunchSpec:
		return inst.LaunchSpec
	}
	return ""
}

// isManagedItemKey 判定 ItemKey 是否在受管集合内。
func isManagedItemKey(key string) bool {
	switch key {
	case ConfigItemStartupCommand, ConfigItemStartupLaunchSpec:
		return true
	}
	if strings.HasPrefix(key, ConfigItemPropsPrefix) {
		_, ok := managedPropSet[strings.TrimPrefix(key, ConfigItemPropsPrefix)]
		return ok
	}
	return false
}

// mcSemanticConfigSurface 判定实例是否具备 MC 配置语义（FR-451 验收 #8 / ADR-091 能力画像）。
// 语义对齐 FR-445 能力画像的 `MCSemantics`：仅「运行 Minecraft 服务端（server.properties）」的实例
// 呈现 startup.* / props.* 受管项。明文排除：
//   - proxy：BungeeCord/Waterfall/Velocity 使用 config.yml，无 server.properties 语义（W6 画像 MCSemantics=false）；
//   - beacon：配套服务角色（FR-450），走各自画像；
//   - generic 类型：通用二进制。
//
// 取舍：W6 画像把未知组合 `(minecraft_java, universal)` 归入「兜底 universal（MCSemantics=false）」，
// 但本 FR 保留该组合为 MC 语义——`minecraft_java` 且未显式设角色（GORM 默认 universal）是
// 手动/MCP `instance_create` 创建 MC 服务端的现实路径（provision/proxy/import/clone 才显式落 backend/proxy），
// 若一并排除会把这些存量实例的关键配置面板静默清空。故此处按「排除 proxy/beacon/generic」收敛，
// 与 FR-451 spec（`props.*`/`startup.*` 对 `minecraft_java` 呈现）一致。
func mcSemanticConfigSurface(inst *model.Instance) bool {
	if inst == nil {
		return false
	}
	return instanceMCSemantics(inst.Type, inst.Role)
}

// instanceMCSemantics 是 FR-445 能力画像 `MCSemantics` 在本 FR 的最小实现（以 (type, role) 为键）。
func instanceMCSemantics(t model.InstanceType, r model.InstanceRole) bool {
	if t != model.InstanceTypeMinecraftJava {
		return false
	}
	switch r {
	case model.InstanceRoleProxy, model.InstanceRoleBeacon:
		return false
	}
	return true
}
// truncatePreview 截断过长的预览文本（按 rune 计数，避免截断 UTF-8）。
func truncatePreview(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
