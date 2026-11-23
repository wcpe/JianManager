package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 配置分层覆盖服务。
//
// 职责边界：
//   - 只管「配置的定义」：配置文件元数据 + 各作用域的不可变版本链；
//   - 合并/校验是纯函数（config_merge.go / config_schema.go），本文件只做取数与编排；
// **不写实例落盘**——下发与生效属532，落盘核心属。

// 分组层相关错误。
var (
	// ErrConfigFileNotFound 配置文件不存在（含已软删）。
	ErrConfigFileNotFound = errors.New("配置文件不存在")
	// ErrConfigFileExists 同名配置文件已存在（软删的也占用名字，避免历史混淆）。
	ErrConfigFileExists = errors.New("同名配置文件已存在")
	// ErrConfigFormatImmutable 格式创建后不可改。
	ErrConfigFormatImmutable = errors.New("配置格式创建后不可修改")
	// ErrConfigNameImmutable 逻辑名创建后不可改（它是全局唯一标识）。
	ErrConfigNameImmutable = errors.New("配置文件名称创建后不可修改")
	// ErrConfigScopeInvalid 作用域级别非法。
	ErrConfigScopeInvalid = errors.New("作用域级别非法（只支持 group / instance）")
	// ErrConfigScopeRefNotFound 作用域引用的分组或实例不存在。
	ErrConfigScopeRefNotFound = errors.New("作用域引用的分组或实例不存在")
	// ErrConfigLayerNotFound 该作用域没有任何层版本。
	ErrConfigLayerNotFound = errors.New("该作用域还没有任何版本")
	// ErrConfigVersionNotFound 指定的层版本不存在或不属于该作用域。
	ErrConfigVersionNotFound = errors.New("层版本不存在")
	// ErrConfigInstanceNotInScope 实例不存在或不在调用者可见范围内。
	ErrConfigInstanceNotInScope = errors.New("实例不存在")
	// ErrConfigLayerInvalid 层链无法合并出可信配置（至少一层语法错误/不可解析）。
	//
	// 单独一个哨兵：调用方据此区分「层内容坏了」（需人工修复，重试无用）
	// 与「数据库/网络抖动」（可重试）。灰度交付遇到它应直接把目标判失败，而非反复重试。
	ErrConfigLayerInvalid = errors.New("配置层内容无效")
	// ErrConfigLayerConflict 层版本被其他写入者抢先追加（乐观并发失败）。
	//
	// 【为什么需要它】层版本是追加写，两个人同时编辑同一层时，后保存的那个若基于旧 head
	// 写作，就会在无提示的情况下把对方的修改整层覆盖掉（新版本成为 head，旧内容只在历史里）。
	// 前端会把看到的 head versionId 作为 expectedHeadVersionId 回传，服务端发现已变就拒写。
	// 与数据库唯一索引冲突（并发撞同一 version_no）不同：那是可重试的竞态，这是语义冲突。
	ErrConfigLayerConflict = errors.New("该层已被其他保存更新，请重新加载后再改")
)

// ConfigFileService 配置分层覆盖服务。
type ConfigFileService struct {
	db *gorm.DB
}

// NewConfigFileService 创建配置分层覆盖服务。
func NewConfigFileService(db *gorm.DB) *ConfigFileService {
	return &ConfigFileService{db: db}
}

// ConfigFileInput 创建/更新配置文件的入参。
type ConfigFileInput struct {
	Name        string `json:"name"`
	Format      string `json:"format"`
	Description string `json:"description"`
	SchemaJSON  string `json:"schemaJson"`
	// SensitivePaths 敏感键路径（点分或 properties 平铺键）列表。
	SensitivePaths []string `json:"sensitivePaths"`
}

// ConfigFileView 配置文件的对外视图。
type ConfigFileView struct {
	model.ConfigFile
	// LayerCount 当前已存在的层（作用域）数量，供列表页展示「覆盖了几层」。
	LayerCount int `json:"layerCount"`
}

// LayerHeadView 某作用域的当前 head 版本摘要（不含 content，避免列表过大）。
type LayerHeadView struct {
	ScopeLevel model.ConfigScopeLevel `json:"scopeLevel"`
	ScopeRefID uint                   `json:"scopeRefId"`
	// ScopeName 分组名/实例名，便于直接展示。
	ScopeName   string `json:"scopeName"`
	VersionID   uint   `json:"versionId"`
	VersionNo   int    `json:"versionNo"`
	ContentHash string `json:"contentHash"`
	Remark      string `json:"remark"`
	CreatedBy   uint   `json:"createdBy"`
	CreatedAt   int64  `json:"createdAt"`
	// ContentSize 原文字节数，供 UI 判断是否需要展开。
	ContentSize int `json:"contentSize"`
}

// LayerVersionView 层版本历史条目。
type LayerVersionView struct {
	ID               uint   `json:"id"`
	VersionNo        int    `json:"versionNo"`
	ContentHash      string `json:"contentHash"`
	Content          string `json:"content"`
	Remark           string `json:"remark"`
	CreatedBy        uint   `json:"createdBy"`
	CreatedAt        int64  `json:"createdAt"`
	BasedOnVersionID *uint  `json:"basedOnVersionId,omitempty"`
}

// EffectivePreview 有效值预览结果（§7.3）。
type EffectivePreview struct {
	InstanceID uint `json:"instanceId"`
	// ConfigFileID 对应配置文件。
	ConfigFileID uint `json:"configFileId"`
	// Format 便于前端选择高亮语法。
	Format string `json:"format"`
	// Content 合并后的完整文本。
	Content string `json:"content"`
	// EffectiveHash sha256(Content)。
	EffectiveHash string `json:"effectiveHash"`
	// Traced 每个键的来源层级/ID/版本号；被删除的键带 deleted=true。
	Traced map[string]TracedValue `json:"traced"`
	// Warnings 合并告警（解析失败 / 类型冲突）。
	Warnings []MergeWarning `json:"warnings"`
	// SchemaIssues 合并结果未通过 schema 的问题列表。
	SchemaIssues []SchemaIssue `json:"schemaIssues"`
	// Layers 实际参与合并的作用域链（从低到高），供 UI 展示「值可能来自哪几层」。
	Layers []LayerSource `json:"layers"`
}

// ---------------------------------------------------------------------------
// 配置文件 CRUD
// ---------------------------------------------------------------------------

// Create 创建配置文件。name 全局唯一、format 创建后不可改（§4.1）。
func (s *ConfigFileService) Create(in ConfigFileInput, authorID uint) (*model.ConfigFile, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("配置文件名不能为空")
	}
	format := model.ConfigFileFormat(strings.ToLower(strings.TrimSpace(in.Format)))
	if !model.ValidConfigFileFormat(format) {
		return nil, fmt.Errorf("配置格式非法: %q（只支持 yaml / json / properties）", in.Format)
	}
	// 唯一性：软删的行也算占用，否则同名重建会让历史层版本的归属产生歧义。
	var count int64
	if err := s.db.Unscoped().Model(&model.ConfigFile{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, fmt.Errorf("%w: %s", ErrConfigFileExists, name)
	}
	if err := validateSchemaJSONText(in.SchemaJSON); err != nil {
		return nil, err
	}
	sensitive, err := encodeSensitivePaths(in.SensitivePaths)
	if err != nil {
		return nil, err
	}
	row := model.ConfigFile{
		Name:           name,
		Format:         format,
		Description:    strings.TrimSpace(in.Description),
		SchemaJSON:     strings.TrimSpace(in.SchemaJSON),
		SensitivePaths: sensitive,
		CreatedBy:      authorID,
	}
	if err := s.db.Create(&row).Error; err != nil {
		return nil, fmt.Errorf("创建配置文件失败: %w", err)
	}
	return &row, nil
}

// ConfigFileUpdateInput 是**更新**专用的入参（与创建分开，因为语义不同）。
//
// 为什么不能复用 ConfigFileInput：创建时零值就是合法默认值（不带 description 即无描述），
// 而更新时「字段缺席」与「显式清空」是两件事。用 string 无法区分——空串既可能是
// 「没传」也可能是「要清空」，旧实现按「没传」处理，导致 description / schemaJson **永远清不掉**。
// 指针能表达三态：nil=不改、&""=清空、&"x"=改成 x。
type ConfigFileUpdateInput struct {
	// Name / Format 仅用于「传了就校验是否被改动」，实际不可改（§7.1）。
	Name   string `json:"name"`
	Format string `json:"format"`

	// Description / SchemaJSON 为 nil 表示不修改；指向空串表示清空。
	Description *string `json:"description"`
	SchemaJSON  *string `json:"schemaJson"`
}

// Update 修改元数据。name 与 format 不可改（§7.1）。
//
// 三态语义：ConfigFileUpdateInput 里指针为 nil 表示「不改」，指向空串表示「清空」。
// SensitivePaths 同样用指针区分「不改」与「清空」。
func (s *ConfigFileService) Update(id uint, in ConfigFileUpdateInput, sensitivePaths *[]string) (*model.ConfigFile, error) {
	row, err := s.mustGet(id)
	if err != nil {
		return nil, err
	}
	if name := strings.TrimSpace(in.Name); name != "" && name != row.Name {
		return nil, ErrConfigNameImmutable
	}
	if format := strings.ToLower(strings.TrimSpace(in.Format)); format != "" && format != string(row.Format) {
		return nil, ErrConfigFormatImmutable
	}
	updates := map[string]any{}
	// 指针非 nil 即「显式给了这个字段」，空串按清空处理。
	if in.Description != nil {
		updates["description"] = strings.TrimSpace(*in.Description)
	}
	if in.SchemaJSON != nil {
		trimmed := strings.TrimSpace(*in.SchemaJSON)
		// 空串 = 清空 schema（等价「不做 schema 校验」），不必再校验合法性。
		if trimmed != "" {
			if err := validateSchemaJSONText(trimmed); err != nil {
				return nil, err
			}
		}
		updates["schema_json"] = trimmed
	}
	if sensitivePaths != nil {
		encoded, err := encodeSensitivePaths(*sensitivePaths)
		if err != nil {
			return nil, err
		}
		updates["sensitive_paths"] = encoded
	}
	if len(updates) > 0 {
		if err := s.db.Model(&model.ConfigFile{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return nil, fmt.Errorf("更新配置文件失败: %w", err)
		}
	}
	return s.mustGet(id)
}

// Delete 软删配置文件（回收站语义）。层版本保留，便于恢复。
func (s *ConfigFileService) Delete(id uint) error {
	if _, err := s.mustGet(id); err != nil {
		return err
	}
	if err := s.db.Delete(&model.ConfigFile{}, id).Error; err != nil {
		return fmt.Errorf("删除配置文件失败: %w", err)
	}
	return nil
}

// Get 读取配置文件详情（含层数量）。
func (s *ConfigFileService) Get(id uint) (*ConfigFileView, error) {
	row, err := s.mustGet(id)
	if err != nil {
		return nil, err
	}
	view := &ConfigFileView{ConfigFile: *row}
	count, err := s.layerCount(row.ID)
	if err != nil {
		return nil, err
	}
	view.LayerCount = count
	return view, nil
}

// List 分页列出配置文件（不含已软删）。
func (s *ConfigFileService) List(keyword string, page, pageSize int) ([]ConfigFileView, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	q := s.db.Model(&model.ConfigFile{})
	if kw := strings.TrimSpace(keyword); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("name LIKE ? OR description LIKE ?", like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.ConfigFile
	if err := q.Order("id asc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]ConfigFileView, 0, len(rows))
	for _, r := range rows {
		count, err := s.layerCount(r.ID)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, ConfigFileView{ConfigFile: r, LayerCount: count})
	}
	return out, total, nil
}

// mustGet 读取未软删的配置文件。
func (s *ConfigFileService) mustGet(id uint) (*model.ConfigFile, error) {
	var row model.ConfigFile
	if err := s.db.First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: #%d", ErrConfigFileNotFound, id)
		}
		return nil, err
	}
	return &row, nil
}

// layerCount 统计该配置文件下已有层（作用域）数量。
func (s *ConfigFileService) layerCount(configFileID uint) (int, error) {
	var n int64
	err := s.db.Model(&model.ConfigLayerVersion{}).
		Where("config_file_id = ?", configFileID).
		Distinct("scope_level", "scope_ref_id").Count(&n).Error
	return int(n), err
}

// ---------------------------------------------------------------------------
// 层版本
// ---------------------------------------------------------------------------

// scopeKey 是作用域的复合键（级别 + 引用 ID）。
type scopeKey struct {
	Level model.ConfigScopeLevel
	RefID uint
}

// ListLayerHeads 列出各作用域的当前 head（每个作用域取最新版本）。
func (s *ConfigFileService) ListLayerHeads(configFileID uint) ([]LayerHeadView, error) {
	if _, err := s.mustGet(configFileID); err != nil {
		return nil, err
	}
	var rows []model.ConfigLayerVersion
	if err := s.db.Where("config_file_id = ?", configFileID).
		Order("scope_level asc, scope_ref_id asc, version_no desc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := map[scopeKey]struct{}{}
	heads := make([]model.ConfigLayerVersion, 0, 8)
	for _, r := range rows {
		k := scopeKey{Level: r.ScopeLevel, RefID: r.ScopeRefID}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		heads = append(heads, r)
	}
	names, err := s.scopeNames(heads)
	if err != nil {
		return nil, err
	}
	out := make([]LayerHeadView, 0, len(heads))
	for _, h := range heads {
		out = append(out, LayerHeadView{
			ScopeLevel:  h.ScopeLevel,
			ScopeRefID:  h.ScopeRefID,
			ScopeName:   names[scopeKey{Level: h.ScopeLevel, RefID: h.ScopeRefID}],
			VersionID:   h.ID,
			VersionNo:   h.VersionNo,
			ContentHash: h.ContentHash,
			Remark:      h.Remark,
			CreatedBy:   h.CreatedBy,
			CreatedAt:   h.CreatedAt.Unix(),
			ContentSize: len(h.Content),
		})
	}
	return out, nil
}

// scopeNames 批量解析作用域名称（分组名 / 实例名），避免逐条查询。
func (s *ConfigFileService) scopeNames(rows []model.ConfigLayerVersion) (map[scopeKey]string, error) {
	out := map[scopeKey]string{}
	groupIDs := make([]uint, 0, len(rows))
	instanceIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		switch r.ScopeLevel {
		case model.ConfigScopeGroup:
			groupIDs = append(groupIDs, r.ScopeRefID)
		case model.ConfigScopeInstance:
			instanceIDs = append(instanceIDs, r.ScopeRefID)
		}
	}
	if len(groupIDs) > 0 {
		var groups []model.InstanceGroupNode
		if err := s.db.Where("id IN ?", groupIDs).Find(&groups).Error; err != nil {
			return nil, err
		}
		for _, g := range groups {
			out[scopeKey{Level: model.ConfigScopeGroup, RefID: g.ID}] = g.Name
		}
	}
	if len(instanceIDs) > 0 {
		var insts []model.Instance
		if err := s.db.Where("id IN ?", instanceIDs).Find(&insts).Error; err != nil {
			return nil, err
		}
		for _, in := range insts {
			out[scopeKey{Level: model.ConfigScopeInstance, RefID: in.ID}] = in.Name
		}
	}
	return out, nil
}

// LayerHistory 返回某作用域的版本历史（新→旧）。
//
// 返回的 Content 默认按文件声明的 sensitivePaths **脱敏**：历史版本存的是原始全文，
// 若原样下发，只持 file.read 的角色就能从任一层历史里读到密码/Token——
// 那等于绕过了 Effective 预览已有的脱敏。
//
// 内部下发链路要原文（写盘不能写 ***），走 LayerHistoryUnmasked。
func (s *ConfigFileService) LayerHistory(configFileID uint, level model.ConfigScopeLevel, scopeID uint) ([]LayerVersionView, error) {
	return s.layerHistory(configFileID, level, scopeID, true)
}

// LayerHistoryUnmasked 返回未脱敏的层历史。**仅供服务端内部下发/回放链路调用**，
// 不得直接暴露给 HTTP 接口（会把敏感字段原文泄露给只有读权限的角色）。
func (s *ConfigFileService) LayerHistoryUnmasked(configFileID uint, level model.ConfigScopeLevel, scopeID uint) ([]LayerVersionView, error) {
	return s.layerHistory(configFileID, level, scopeID, false)
}

// LayerVersionViewOf 把一条层版本行转成对外展示视图，并按配置文件声明的敏感路径脱敏。
//
// 【为什么写接口也要走这里】SaveLayer / RollbackLayer 原本直接 `c.JSON(200, ver)`
// 返回 model.ConfigLayerVersion，而它带 `Content` 原文——于是「保存一个含密钥的层」
// 这个写操作的响应体就把明文吐回给了调用方，与 GET /layers/.../history 的脱敏口径
// 不一致。对外展示必须统一。
//
// 内部链路（下发/回放）需要原文时走 LayerHistoryUnmasked，不经本函数。
func (s *ConfigFileService) LayerVersionViewOf(configFileID uint, ver *model.ConfigLayerVersion) (*LayerVersionView, error) {
	if ver == nil {
		return nil, nil
	}
	file, err := s.mustGet(configFileID)
	if err != nil {
		return nil, err
	}
	content := ver.Content
	if sensitive := decodeSensitivePaths(file.SensitivePaths); len(sensitive) > 0 {
		content = maskSensitiveInContent(string(file.Format), content, sensitive)
	}
	return &LayerVersionView{
		ID:               ver.ID,
		VersionNo:        ver.VersionNo,
		ContentHash:      ver.ContentHash,
		Content:          content,
		Remark:           ver.Remark,
		CreatedBy:        ver.CreatedBy,
		CreatedAt:        ver.CreatedAt.Unix(),
		BasedOnVersionID: ver.BasedOnVersionID,
	}, nil
}

func (s *ConfigFileService) layerHistory(configFileID uint, level model.ConfigScopeLevel, scopeID uint, mask bool) ([]LayerVersionView, error) {
	file, err := s.mustGet(configFileID)
	if err != nil {
		return nil, err
	}
	if !model.ValidConfigScopeLevel(level) {
		return nil, ErrConfigScopeInvalid
	}
	var rows []model.ConfigLayerVersion
	if err := s.db.Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?", configFileID, level, scopeID).
		Order("version_no desc").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s:%d", ErrConfigLayerNotFound, level, scopeID)
	}
	var sensitive []string
	if mask {
		sensitive = decodeSensitivePaths(file.SensitivePaths)
	}
	out := make([]LayerVersionView, 0, len(rows))
	for _, r := range rows {
		content := r.Content
		if len(sensitive) > 0 {
			content = maskSensitiveInContent(string(file.Format), content, sensitive)
		}
		out = append(out, LayerVersionView{
			ID:               r.ID,
			VersionNo:        r.VersionNo,
			ContentHash:      r.ContentHash,
			Content:          content,
			Remark:           r.Remark,
			CreatedBy:        r.CreatedBy,
			CreatedAt:        r.CreatedAt.Unix(),
			BasedOnVersionID: r.BasedOnVersionID,
		})
	}
	return out, nil
}

// SaveLayerInput 保存新层版本的入参。
type SaveLayerInput struct {
	ScopeLevel string `json:"scopeType"`
	ScopeID    uint   `json:"scopeId"`
	Content    string `json:"content"`
	Remark     string `json:"remark"`
	// ExpectedHeadVersionID 乐观并发：调用方基于哪个 head 版本编辑的。
	//
	// 0 / 缺省 = 不做检查（新建该作用域 v1、或内部调用方）；非 0 时服务端要求它仍是当前 head，
	// 否则返回 ErrConfigLayerConflict——避免静默覆盖别人刚保存的内容。
	ExpectedHeadVersionID uint `json:"expectedHeadVersionId,omitempty"`
}

// SaveLayer 保存某作用域的新版本（追加，不改历史）。
//
// 校验时机（§6.1）：保存时只校验**该层原文**，不做合并校验——
// 某一层本身可能不满足完整 schema（它只是一份覆盖片段），合并后才该满足。
func (s *ConfigFileService) SaveLayer(configFileID uint, in SaveLayerInput, authorID uint) (*model.ConfigLayerVersion, error) {
	file, err := s.mustGet(configFileID)
	if err != nil {
		return nil, err
	}
	level := model.ConfigScopeLevel(strings.ToLower(strings.TrimSpace(in.ScopeLevel)))
	if !model.ValidConfigScopeLevel(level) {
		return nil, ErrConfigScopeInvalid
	}
	if err := s.ensureScopeExists(level, in.ScopeID); err != nil {
		return nil, err
	}
	// 乐观并发：调用方声明「我是基于哪一版 head 改的」。已变则拒写——
	// 否则会把别人刚保存的整层内容静默覆盖（新版本成为 head，对方的改动只在历史里）。
	if in.ExpectedHeadVersionID != 0 {
		head, err := s.currentLayerHeadID(file.ID, level, in.ScopeID)
		if err != nil {
			return nil, err
		}
		if head != in.ExpectedHeadVersionID {
			return nil, ErrConfigLayerConflict
		}
	}
	// 敏感字段占位符还原：历史与 head 接口默认把敏感值脱敏为 ***，
	// 用户基于遮罩后的文本编辑时，未动过的敏感行仍然是 ***——直接存会把
	// 真实密钥写成星号。这里把「值仍是占位符」的行改回当前 head 的原文；
	// 用户真改了值（不再是 ***）则保留用户输入。
	content := s.restoreMaskedSecrets(file, level, in.ScopeID, in.Content)
	// 该层原文的语法与 schema 校验。
	if issues := s.validateLayerContent(file, content); len(issues) > 0 {
		return nil, &ConfigLayerValidationError{Issues: issues}
	}
	return s.appendLayer(file, level, in.ScopeID, content, in.Remark, nil, authorID)
}

// currentLayerHeadID 返回某作用域当前 head 的版本 id；无任何版本时返回 0。
//
// 不返回 ErrConfigLayerNotFound：对乐观并发而言「还没有版本」与「head=0」等价，
// 调用方拿 0 去比对即可（新建层的场景 expectedHeadVersionId 本就是 0，不会走到这里）。
func (s *ConfigFileService) currentLayerHeadID(configFileID uint, level model.ConfigScopeLevel, scopeID uint) (uint, error) {
	var head model.ConfigLayerVersion
	err := s.db.Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?", configFileID, level, scopeID).
		Order("version_no desc").Limit(1).Find(&head).Error
	if err != nil {
		return 0, err
	}
	return head.ID, nil
}

// maskedSecretPlaceholder 是脱敏后展示的占位值。
const maskedSecretPlaceholder = "***"

// restoreMaskedSecrets 把内容中仍为占位符的敏感值替换成当前 head 中的真实值。
//
// 只在「文件声明了 sensitivePaths 且该作用域已有 head」时生效；
// 任一步骤失败都返回原文（宁可不还原，也不把用户内容改成别的）。
func (s *ConfigFileService) restoreMaskedSecrets(file *model.ConfigFile, level model.ConfigScopeLevel, scopeID uint, content string) string {
	paths := decodeSensitivePaths(file.SensitivePaths)
	if len(paths) == 0 || !strings.Contains(content, maskedSecretPlaceholder) {
		return content
	}
	// 取当前 head 原文（内部链路，不过遮罩）。
	history, err := s.LayerHistoryUnmasked(file.ID, level, scopeID)
	if err != nil || len(history) == 0 {
		return content
	}
	return restoreMaskedInContent(string(file.Format), content, history[0].Content, paths)
}

// restoreMaskedInContent 把内容中仍是占位符的敏感值还原为 head 中的真实值。
//
// 【与 mask 侧必须严格对称】maskStructuredSensitive 是**按结构路径**遮的：
// `database.password` 只遮 database 下的那个 password，不会碰 cache.password。
// 因此还原也**必须按结构路径**做，否则：
//   - 紧凑 JSON（无换行）下，行级匹配拿到的“整行”是整个文件，永远匹配不上；
//   - 同名叶键（database.password 与 cache.password）会被一并改掉；
//   - 数组下标路径（servers.0.pass）无法表达。
//
// 以上三种都会让真实密码被 `***` 静默覆盖，属数据损坏，故这里不再用行级启发式。
//
// 分两条路：
//   - properties：键本身就是点分形式（`rcon.password=xxx`），仍按行处理（保持原格式与注释）；
//   - yaml/json：解析成结构树 → 沿完整路径比对占位符 → 按原格式回写。
//
// 解析不了时**原样返回**（宁可不还原，也不把用户内容改成别的；与 mask 侧回退启发式
// 的方向相反——mask 宁可不脱敏也不能漏，restore 宁可漏还原也不能改坏）。
func restoreMaskedInContent(format, content, headContent string, paths []string) string {
	if headContent == "" || len(paths) == 0 || content == "" {
		return content
	}
	if format == string(model.ConfigFormatProperties) {
		return restoreMaskedProperties(content, headContent, paths)
	}
	return restoreMaskedStructured(format, content, headContent, paths)
}

// restoreMaskedProperties 按行还原 properties 里的占位符。
//
// properties 的键就是完整的点分路径，与 mask 侧同口径，故这里保持行级实现：
// 它能精确保留注释、空行与键序（重写成 Properties 会丢掉这些）。
func restoreMaskedProperties(content, headContent string, paths []string) string {
	want := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p != "" {
			want[p] = struct{}{}
		}
	}
	headValues := propertiesValuesByKey(headContent, want)
	if len(headValues) == 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		key, sep, _ := sensitiveKeyOfLine(string(model.ConfigFormatProperties), line)
		if sep < 0 {
			continue
		}
		if _, ok := want[key]; !ok {
			continue
		}
		real, ok := headValues[key]
		if !ok {
			continue
		}
		// 【只认**完整值**占位符】原来用 strings.Contains(trimmed, "***"):
		// 用户把密码改成 `new***value` 时整行包含星号，就会被错误地回滚成旧值
		// （静默丢掉用户刚设的新密码）。必须在“值部分”精确等于占位符才算被遮过。
		//
		// 【必须从 line 取而不是 trimmed】sep 是 sensitiveKeyOfLine 用
		// strings.IndexAny(line, ...) 算出来的**原始行下标**，而 trimmed 是
		// strings.TrimSpace(line) 的结果。行有前导缩进时两者偏移不一致：
		// 2 空格时判定永远不成立（无法还原），≥ 分隔符偏移时会 slice bounds panic。
		if strings.TrimSpace(line[sep+1:]) != maskedSecretPlaceholder {
			continue
		}
		lines[i] = line[:sep+1] + real
	}
	return strings.Join(lines, "\n")
}

// propertiesValuesByKey 从 head 原文里抽出指定键的真实值（保持单行）。
func propertiesValuesByKey(headContent string, want map[string]struct{}) map[string]string {
	out := make(map[string]string, len(want))
	for _, line := range strings.Split(headContent, "\n") {
		key, sep, _ := sensitiveKeyOfLine(string(model.ConfigFormatProperties), line)
		if sep < 0 {
			continue
		}
		if _, ok := want[key]; !ok {
			continue
		}
		// 【必须从 line 取而不是 trimmed】sep 是基于原始 line（strings.IndexAny(line, ...)）
		// 算出的下标；trimmed 是 TrimSpace(line) 的结果。行有前导缩进时两者偏移不一致：
		// 用 trimmed[sep+1:] 会把值的前几个字符切掉（实测 `real-secret` → `al-secret`）。
		val := strings.TrimSpace(line[sep+1:])
		if val == "" || val == maskedSecretPlaceholder {
			continue
		}
		out[key] = val
	}
	return out
}

// restoreMaskedStructured 对 yaml/json 按结构路径还原占位符，并按**原格式**回写。
//
// 【关键：必须保留原类型】占位符在展示上是字符串 `***`，但 head 里的真值可能是
// 数字（otp=123456）、空串、甚至整个对象（敏感路径声明为父级 `database`）。
// 早期版本只认非空字符串，导致这三类真值被永久写成 `"***"`：
//   - 数字 → 变字符串，类型都变了；
//   - 空串 → 被“非空”检查跳过，永远回不来；
//   - 父对象 → 整体被掩成 "***"，子字段全丢。
//
// 现在只要求「编辑后该路径的值含占位符」，然后**原样回填 head 里的任意类型**。
//
// 回写格式与 maskStructuredSensitive 一致：JSON 输入回 JSON，其余回 YAML——
// 保持「展示时是什么格式，保存后还是什么格式」这个闭环。
func restoreMaskedStructured(format, content, headContent string, paths []string) string {
	editedDoc, editedJSON, err := parseStructuredDoc(content)
	if err != nil {
		return content
	}
	headDoc, _, herr := parseStructuredDoc(headContent)
	if herr != nil {
		return content
	}

	changed := false
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if restorePathIfMasked(editedDoc, headDoc, strings.Split(p, ".")) {
			changed = true
		}
	}
	if !changed {
		return content
	}

	// 按原格式回写。JSON 尽量紧凑/缩进二选一：原内容是紧凑的（无换行）就回紧凑，
	// 否则缩进——避免把用户的单行 JSON 拆成多行（那会让 diff 看起来像被重写了）。
	useJSON := editedJSON || format == string(model.ConfigFormatJSON)
	if useJSON {
		if !strings.Contains(strings.TrimSpace(content), "\n") {
			if b, err := json.Marshal(editedDoc); err == nil {
				return string(b)
			}
			return content
		}
		if b, err := json.MarshalIndent(editedDoc, "", "  "); err == nil {
			return string(b)
		}
		return content
	}
	if b, err := yaml.Marshal(editedDoc); err == nil {
		return string(b)
	}
	return content
}

// isMaskedPlaceholder 判断一个值是否**恰好就是**占位符。
//
// 必须是“完整值相等”而不是“包含 ***”：用户可能真的写 `new***value`，
// 那不是占位符，是用户的新密码，绝不能被他旧值覆盖。
func isMaskedPlaceholder(v any) bool {
	s, isStr := v.(string)
	if !isStr {
		return false
	}
	return s != "" && strings.TrimSpace(s) == maskedSecretPlaceholder
}

// deepContainsPlaceholder 判断一个值内部是否含占位符。
//
// 父对象遮罩会把整个子树换成字符串占位符；若上游只遮了末原子，
// 子树上就会出现混合（部分占位符、部分真值）。两种都算“被遮过”。
func deepContainsPlaceholder(v any) bool {
	switch cur := v.(type) {
	case string:
		return strings.Contains(cur, maskedSecretPlaceholder)
	case map[string]any:
		for _, child := range cur {
			if deepContainsPlaceholder(child) {
				return true
			}
		}
		return false
	case []any:
		for _, child := range cur {
			if deepContainsPlaceholder(child) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// restorePathIfMasked 在 edited 的 segs 路径上，若值仍含占位符，
// 就把 head 在同路径下的真值原样写回（保持任意类型）。返回是否发生了回填。
func restorePathIfMasked(editedDoc, headDoc map[string]any, segs []string) bool {
	if len(segs) == 0 {
		return false
	}
	editedVal, ok := lookupPathInMap(editedDoc, segs)
	if !ok || !deepContainsPlaceholder(editedVal) {
		return false
	}
	realVal, ok := lookupPathInMap(headDoc, segs)
	if !ok {
		return false
	}
	// head 里本身就是占位符（重复保存）时无计可施，不覆盖。
	//
	// 【不能再判 `realVal == nil`】lookupPathInMap 的 ok 已经区分了“路径缺失”，
	// 而 JSON 的 null 是**真实存在的值**（secret: null）。早期版本多判一次
	// realVal==nil，导致 null 被当成缺失 → 用户只改 motd，null 却变成 "***"。
	if isMaskedPlaceholder(realVal) {
		return false
	}
	return setPathInMap(editedDoc, segs, realVal)
}

// parseStructuredDoc 把内容解析成 map[string]any；第二返回值表示是否按 JSON 解析成功。
// 与 maskStructuredSensitive 的解析顺序保持一致（先严格 JSON，再宽松 YAML）。
//
// 【必须用 UseNumber】json.Unmarshal 到 any 会把所有数字解成 float64，
// 超过 2^53 的整数（如雪花 ID、accountId=9007199254740993）会静默滑到 ...992。
// 对配置系统而言这是**与敏感字段无关的旁路损坏**：用户只改了一行 motd，
// 无关的长整型字段就被改掉了。UseNumber 把数字保留为 json.Number（字符串承载），
// 序列化时原样输出。
func parseStructuredDoc(content string) (map[string]any, bool, error) {
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err == nil {
		if m, ok := doc.(map[string]any); ok {
			return m, true, nil
		}
		return nil, true, fmt.Errorf("顶层不是对象")
	}
	var y any
	if err := yaml.Unmarshal([]byte(content), &y); err != nil {
		return nil, false, err
	}
	m, ok := y.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("顶层不是映射")
	}
	return m, false, nil
}

// lookupPathInMap 沿点分路径取值；数组段用十进制下标。
func lookupPathInMap(node any, segs []string) (any, bool) {
	if len(segs) == 0 {
		return node, true
	}
	switch cur := node.(type) {
	case map[string]any:
		child, ok := cur[segs[0]]
		if !ok {
			return nil, false
		}
		return lookupPathInMap(child, segs[1:])
	case []any:
		idx, err := strconv.Atoi(segs[0])
		if err != nil || idx < 0 || idx >= len(cur) {
			return nil, false
		}
		return lookupPathInMap(cur[idx], segs[1:])
	default:
		return nil, false
	}
}

// setPathInMap 沿点分路径写值（路径必须已存在，避免凭空造出字段）；返回是否写入。
func setPathInMap(node any, segs []string, val any) bool {
	if len(segs) == 0 {
		return false
	}
	switch cur := node.(type) {
	case map[string]any:
		if len(segs) == 1 {
			if _, exists := cur[segs[0]]; !exists {
				return false
			}
			cur[segs[0]] = val
			return true
		}
		child, ok := cur[segs[0]]
		if !ok {
			return false
		}
		return setPathInMap(child, segs[1:], val)
	case []any:
		idx, err := strconv.Atoi(segs[0])
		if err != nil || idx < 0 || idx >= len(cur) {
			return false
		}
		if len(segs) == 1 {
			cur[idx] = val
			return true
		}
		return setPathInMap(cur[idx], segs[1:], val)
	default:
		return false
	}
}

// sensitiveKeyOfLine 提取一行里的键与分隔符位置。sep<0 表示该行不含键值。
// 仅用于 properties（键即完整点分路径）；yaml/json 走结构化路径，不再用行级匹配。
func sensitiveKeyOfLine(format, line string) (key string, sep int, trimmed string) {
	trimmed = strings.TrimSpace(line)
	if format == string(model.ConfigFormatProperties) {
		idx := strings.IndexAny(line, "=:")
		if idx < 0 {
			return "", -1, trimmed
		}
		return strings.TrimSpace(line[:idx]), idx, trimmed
	}
	idx := strings.Index(trimmed, ":")
	if idx < 0 {
		return "", -1, trimmed
	}
	return strings.Trim(strings.TrimSpace(trimmed[:idx]), `"'`), idx, trimmed
}

// RollbackLayer 回退到指定版本：**追加一个新版本**，内容复制自源版本（§4.2）。
// 历史行永不修改，因此回退本身也可再回退。
func (s *ConfigFileService) RollbackLayer(configFileID uint, level model.ConfigScopeLevel, scopeID, versionID, authorID uint, remark string) (*model.ConfigLayerVersion, error) {
	if _, err := s.mustGet(configFileID); err != nil {
		return nil, err
	}
	if !model.ValidConfigScopeLevel(level) {
		return nil, ErrConfigScopeInvalid
	}
	var src model.ConfigLayerVersion
	if err := s.db.Where("id = ? AND config_file_id = ? AND scope_level = ? AND scope_ref_id = ?",
		versionID, configFileID, level, scopeID).First(&src).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: #%d", ErrConfigVersionNotFound, versionID)
		}
		return nil, err
	}
	file, err := s.mustGet(configFileID)
	if err != nil {
		return nil, err
	}
	if remark == "" {
		remark = fmt.Sprintf("回退到 v%d", src.VersionNo)
	}
	return s.appendLayer(file, level, scopeID, src.Content, remark, &src.ID, authorID)
}

// DeleteLayer 删除某作用域的全部版本（含历史）。
// 这是「撤销该层覆盖」的语义，故是硬删——留软删会让 head 解析复杂化而无收益。
func (s *ConfigFileService) DeleteLayer(configFileID uint, level model.ConfigScopeLevel, scopeID uint) error {
	if _, err := s.mustGet(configFileID); err != nil {
		return err
	}
	if !model.ValidConfigScopeLevel(level) {
		return ErrConfigScopeInvalid
	}
	res := s.db.Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?", configFileID, level, scopeID).
		Delete(&model.ConfigLayerVersion{})
	if res.Error != nil {
		return fmt.Errorf("删除层版本失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: %s:%d", ErrConfigLayerNotFound, level, scopeID)
	}
	return nil
}

// appendLayer 追加一个层版本：version_no = 该作用域当前最大值 + 1。
func (s *ConfigFileService) appendLayer(file *model.ConfigFile, level model.ConfigScopeLevel, scopeID uint, content, remark string, basedOn *uint, authorID uint) (*model.ConfigLayerVersion, error) {
	var maxNo int
	row := s.db.Model(&model.ConfigLayerVersion{}).
		Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?", file.ID, level, scopeID).
		Select("COALESCE(MAX(version_no), 0)").Scan(&maxNo)
	if row.Error != nil {
		return nil, row.Error
	}
	ver := model.ConfigLayerVersion{
		ConfigFileID:     file.ID,
		ScopeLevel:       level,
		ScopeRefID:       scopeID,
		VersionNo:        maxNo + 1,
		Content:          content,
		ContentHash:      hashContent(content),
		BasedOnVersionID: basedOn,
		Remark:           remark,
		CreatedBy:        authorID,
	}
	if err := s.db.Create(&ver).Error; err != nil {
		// 唯一索引冲突说明并发追加撞上了同一 version_no；报错而不是静默重试，
		// 让调用方重试一次（重试会读到新的 max_no）。
		return nil, fmt.Errorf("保存层版本失败: %w", err)
	}
	return &ver, nil
}

// ensureScopeExists 校验作用域引用的分组/实例确实存在。
// 不存在的引用会让「配了却没人收到」的错觉发生，故 fail-closed（与同口径）。
func (s *ConfigFileService) ensureScopeExists(level model.ConfigScopeLevel, scopeID uint) error {
	var count int64
	switch level {
	case model.ConfigScopeGroup:
		if err := s.db.Model(&model.InstanceGroupNode{}).Where("id = ?", scopeID).Count(&count).Error; err != nil {
			return err
		}
	case model.ConfigScopeInstance:
		if err := s.db.Model(&model.Instance{}).Where("id = ?", scopeID).Count(&count).Error; err != nil {
			return err
		}
	default:
		return ErrConfigScopeInvalid
	}
	if count == 0 {
		return fmt.Errorf("%w: %s:%d", ErrConfigScopeRefNotFound, level, scopeID)
	}
	return nil
}

// ConfigLayerValidationError 携带逐条问题的层校验错误，供 router 返回结构化 validation。
type ConfigLayerValidationError struct {
	Issues []SchemaIssue
}

func (e *ConfigLayerValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "配置校验失败"
	}
	parts := make([]string, 0, len(e.Issues))
	for _, it := range e.Issues {
		if it.Path != "" {
			parts = append(parts, it.Path+": "+it.Message)
			continue
		}
		parts = append(parts, it.Message)
	}
	return "配置校验失败: " + strings.Join(parts, "; ")
}

// validateLayerContent 校验该层原文：先语法，后 schema（只在 schema 存在时）。
func (s *ConfigFileService) validateLayerContent(file *model.ConfigFile, content string) []SchemaIssue {
	values, err := ParseConfigContent(file.Format, content)
	if err != nil {
		return []SchemaIssue{{Level: "error", Message: err.Error()}}
	}
	return ValidateConfigAgainstSchema(file.Format, file.SchemaJSON, values)
}

// ---------------------------------------------------------------------------
// 有效值预览
// ---------------------------------------------------------------------------

// ScopeChain 是解析出的作用域链（从低到高优先级）及其命中版本。
type ScopeChain struct {
	// InstanceID 目标实例。
	InstanceID uint
	// Layers 按优先级从低到高排列的参与层。
	Layers []MergeLayer
	// Chain 作用域链的可读描述（供 UI 展示，即使某层没配版本也会出现在这里）。
	Chain []LayerSource
}

// resolveScopeChain 解析实例的作用域链：分组树自根到叶 + 实例自身（§5.1）。
//
// 一个实例可能属于多个分组（InstanceGroupMember 是 M:N）。此时每个分组各自形成一条链，
// 本方法取「最深的一条」作为主链——深层覆盖更具体，符合覆盖直觉；
// 同深度时按分组 ID 升序取第一条，保证结果确定（不随 DB 返回顺序漂移）。
func (s *ConfigFileService) resolveScopeChain(instanceID uint) (*ScopeChain, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: #%d", ErrConfigInstanceNotInScope, instanceID)
		}
		return nil, err
	}
	// 读全量分组树，一次内存遍历（树规模是人为组织的量级，远小于实例数）。
	var nodes []model.InstanceGroupNode
	if err := s.db.Find(&nodes).Error; err != nil {
		return nil, err
	}
	var members []model.InstanceGroupMember
	if err := s.db.Where("instance_id = ?", instanceID).Find(&members).Error; err != nil {
		return nil, err
	}
	groupIDs := collectMemberGroupIDs(members)
	chain := pickDeepestGroupChain(nodes, groupIDs)
	sc := &ScopeChain{InstanceID: instanceID}
	for _, g := range chain {
		sc.Chain = append(sc.Chain, LayerSource{Level: string(model.ConfigScopeGroup), ID: g.ID, Name: g.Name})
	}
	// 实例层恒在最后（优先级最高）。
	sc.Chain = append(sc.Chain, LayerSource{Level: string(model.ConfigScopeInstance), ID: inst.ID, Name: inst.Name})
	return sc, nil
}

// collectMemberGroupIDs 取出成员关系里的分组 ID 列表。
//
// 注意不能复用 collectInstanceIDs（那是反方向：取成员里的 InstanceID），
// 两者语义相反且都是 []uint，混用不会有编译错误，只会静默拿到错的 id 空间——
// 这是本文件最容易被复制粘贴写错的一处，故单独命名。
func collectMemberGroupIDs(members []model.InstanceGroupMember) []uint {
	out := make([]uint, 0, len(members))
	for _, m := range members {
		out = append(out, m.GroupID)
	}
	return out
}

// pickDeepestGroupChain 从候选分组中挑出最深的一条祖先链（root → ... → leaf）。
// 无候选时返回空链（实例不属于任何分组）。
func pickDeepestGroupChain(nodes []model.InstanceGroupNode, candidates []uint) []model.InstanceGroupNode {
	if len(candidates) == 0 {
		return nil
	}
	byID := make(map[uint]model.InstanceGroupNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	var best []model.InstanceGroupNode
	// 候选排序保证同深度时结果确定。
	sorted := append([]uint(nil), candidates...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, id := range sorted {
		if _, ok := byID[id]; !ok {
			continue
		}
		chain := ancestorChain(byID, id)
		if len(chain) > len(best) {
			best = chain
		}
	}
	return best
}

// ancestorChain 返回 root → node 的完整祖先链（含 node 自身）。
// 用 visited 防御脏数据造成的环，避免死循环。
func ancestorChain(byID map[uint]model.InstanceGroupNode, nodeID uint) []model.InstanceGroupNode {
	chain := make([]model.InstanceGroupNode, 0, 4)
	visited := map[uint]struct{}{}
	cur := nodeID
	for {
		if _, seen := visited[cur]; seen {
			break
		}
		n, ok := byID[cur]
		if !ok {
			break
		}
		visited[cur] = struct{}{}
		chain = append(chain, n)
		if n.ParentID == nil {
			break
		}
		cur = *n.ParentID
	}
	// 反转成 root → leaf。
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// loadLayerContents 按作用域链取各层当前 head 的内容，填充进 MergeLayer。
// 未配版本的作用域不进入合并链（等同于该层无覆盖）。
//
// 用 Find 而非 First 取 head：First 在无记录时返回 ErrRecordNotFound 并打一条 error 级日志，
// 而「这一层没配」是完全正常的状态（每个实例的链上大部分层通常都没配），
// 用 First 会让日志被 record not found 淹没，真正的异常反而看不见。
func (s *ConfigFileService) loadLayerContents(configFileID uint, sc *ScopeChain) ([]MergeLayer, error) {
	layers := make([]MergeLayer, 0, len(sc.Chain))
	for _, ref := range sc.Chain {
		level := model.ConfigScopeLevel(ref.Level)
		var heads []model.ConfigLayerVersion
		if err := s.db.Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?",
			configFileID, level, ref.ID).
			Order("version_no desc").Limit(1).Find(&heads).Error; err != nil {
			return nil, err
		}
		if len(heads) == 0 {
			continue
		}
		head := heads[0]
		layers = append(layers, MergeLayer{
			Level:      level,
			ScopeRefID: ref.ID,
			Name:       ref.Name,
			VersionNo:  head.VersionNo,
			Content:    head.Content,
		})
	}
	return layers, nil
}

// Effective 计算某实例在某配置文件上的有效值（§7.3）。
func (s *ConfigFileService) Effective(configFileID, instanceID uint) (*EffectivePreview, error) {
	file, err := s.mustGet(configFileID)
	if err != nil {
		return nil, err
	}
	sc, err := s.resolveScopeChain(instanceID)
	if err != nil {
		return nil, err
	}
	layers, err := s.loadLayerContents(configFileID, sc)
	if err != nil {
		return nil, err
	}
	merged, err := MergeConfigLayers(file.Format, layers)
	if err != nil {
		return nil, err
	}
	// 预览时校验合并结果（§6.1）。
	values, perr := ParseConfigContent(file.Format, merged.Content)
	var schemaIssues []SchemaIssue
	if perr != nil {
		schemaIssues = []SchemaIssue{{Level: "error", Message: perr.Error()}}
	} else {
		schemaIssues = ValidateConfigAgainstSchema(file.Format, file.SchemaJSON, values)
	}
	preview := &EffectivePreview{
		InstanceID:    instanceID,
		ConfigFileID:  file.ID,
		Format:        string(file.Format),
		Content:       merged.Content,
		EffectiveHash: merged.EffectiveHash,
		Traced:        merged.Traced,
		Warnings:      merged.Warnings,
		SchemaIssues:  schemaIssues,
		Layers:        sc.Chain,
	}
	applySensitiveMask(file.SensitivePaths, preview)
	return preview, nil
}

// EffectiveContent 只取合并后的内容与 hash（供531 下发链路复用，不做脱敏）。
func (s *ConfigFileService) EffectiveContent(configFileID, instanceID uint) (content, effectiveHash string, err error) {
	file, err := s.mustGet(configFileID)
	if err != nil {
		return "", "", err
	}
	sc, err := s.resolveScopeChain(instanceID)
	if err != nil {
		return "", "", err
	}
	layers, err := s.loadLayerContents(configFileID, sc)
	if err != nil {
		return "", "", err
	}
	merged, err := MergeConfigLayers(file.Format, layers)
	if err != nil {
		return "", "", err
	}
	// **不可信结果不得下发**：只要有一层解析失败，merged.Content 就是“少了整层覆盖”的
	// 合并结果。它看起来合法、服务也能起来，只是丢了所有更高优先级的覆盖——
	// 这是灰度通道里最难被发现的一类失真。
	//
	// 修复前 Valid 是个没人读的字段：置了 false 照样落盘，
	// 而 config_delivery 里专为它准备的 TargetCodeMergeFailed 分支永远走不到。
	if !merged.Valid {
		return "", "", fmt.Errorf("%w: 至少一层解析失败，合并结果不可信", ErrConfigLayerInvalid)
	}
	return merged.Content, merged.EffectiveHash, nil
}

// applySensitiveMask 把敏感键的值脱敏为 ***（§8）。
// 内容 hash 基于明文计算，故脱敏只作用于展示字段（traced 的值与 content 里对应行）。
//
// 【为什么不能只做精确键匹配】升级前这里只写了 `preview.Traced[p].Value = "***"`，
// 而合并时**数组是整体替换**（config_merge.go：`标量 / 数组：整体替换（数组不元素合并）`），
// 所以 Traced 里根本不存在 `servers.0.pass` 这个键——只有 `servers`，而它的 Value
// 是包含每个元素 pass 的**整个数组**。于是 content 被遮了、traced 里的原文却完整透出。
// 同理，声明 `a.b.secret` 时若 `a` 整个是对象，Traced 也只有 `a` 这个键。
//
// 修法：对每条敏感路径，沿路径从根往下找**最长的已存在 traced 前缀**，
// 在它那个 Value 里按剩余段继续下钻并遮掉目标叶——既不漏遮，也不误杀兄弟字段。
func applySensitiveMask(sensitiveJSON string, preview *EffectivePreview) {
	paths := decodeSensitivePaths(sensitiveJSON)
	if len(paths) == 0 || preview == nil {
		return
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		maskTracedPath(preview.Traced, p)
	}
	preview.Content = maskSensitiveInContent(preview.Format, preview.Content, paths)
}

// maskTracedPath 在 traced 映射里遮掉完整路径 path 对应的值。
//
// 两种形态都要处理：
//   - **展开的后代**（path 是父对象，但 traced 里把子键展开了）：
//     如 path=`database`，traced 里有 `database.password` / `database.host`。
//     必须把所有以 `database.` 开头的条目也遮掉，否则父对象遮了、子键仍明文；
//   - **祖先条目**（path 是子键，但 traced 只有父条目）：
//     如 path=`servers.0.pass`，traced 只有 `servers`（数组整体替换）。
//     沿路径找最长的已存在前缀，在它的 Value 里下钻。
func maskTracedPath(traced map[string]TracedValue, path string) {
	if len(traced) == 0 {
		return
	}
	// 第一步：清除**展开的后代**（path 本身或其子路径为键的条目）。
	// 先收集再删，避免边遍历边修改 map。
	prefix := path + "."
	var descendantKeys []string
	for k := range traced {
		if strings.HasPrefix(k, prefix) {
			descendantKeys = append(descendantKeys, k)
		}
	}
	for _, k := range descendantKeys {
		tv := traced[k]
		tv.Value = maskedSecretPlaceholder
		traced[k] = tv
	}

	// 第二步：处理「path 本身或它的祖先作为单个条目」的情形。
	segs := strings.Split(path, ".")
	for k := len(segs); k >= 1; k-- {
		key := strings.Join(segs[:k], ".")
		tv, ok := traced[key]
		if !ok {
			continue
		}
		if k == len(segs) {
			tv.Value = maskedSecretPlaceholder
			traced[key] = tv
			return
		}
		// 命中祖先条目（数组/对象被整体替换）：在它的 Value 里下钻。
		if maskPathInMap(tv.Value, segs[k:]) {
			traced[key] = tv
		}
		return
	}
}

// maskSensitiveInContent 在合并后的文本里把敏感键的值替换为 ***。
// properties 按键行替换；yaml/json 用文本行级处理（重解析再序列化会破坏格式稳定性，
// 而这里只服务于展示）。
func maskSensitiveInContent(format, content string, paths []string) string {
	if len(paths) == 0 || content == "" {
		return content
	}
	want := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		want[p] = struct{}{}
	}
	switch format {
	case string(model.ConfigFormatProperties):
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			sep := strings.IndexAny(line, "=:")
			if sep < 0 {
				continue
			}
			key := strings.TrimSpace(line[:sep])
			if _, ok := want[key]; !ok {
				continue
			}
			lines[i] = line[:sep+1] + "***"
		}
		return strings.Join(lines, "\n")
	default:
		// yaml/json：**先做结构化按路径遮罩**，解析失败才回退行级启发式。
		//
		// 为什么必须结构化：SensitivePaths 支持点分路径（如 database.password），
		// 而原实现只把「行里的末段键名」与整条路径比较——`database.password` 配
		// `password: xxx` 永远对不上，于是嵌套凭据在 LayerHistory / Effective 里明文返回。
		// 这属于「配置了脱敏却漏掉」而非展示瑕疵。
		masked, ok := maskStructuredSensitive(format, content, paths)
		if ok {
			return masked
		}
		// 回退：按「末段键名」匹配（解析不了的脏内容仍要有兜底，不能因此不脱敏）。
		lastSegment := make(map[string]struct{}, len(paths))
		for _, p := range paths {
			if idx := strings.LastIndex(p, "."); idx >= 0 {
				lastSegment[p[idx+1:]] = struct{}{}
			} else {
				lastSegment[p] = struct{}{}
			}
		}
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			sep := strings.Index(trimmed, ":")
			if sep < 0 {
				continue
			}
			key := strings.Trim(strings.TrimSpace(trimmed[:sep]), `"'`)
			if _, ok := lastSegment[key]; !ok {
				continue
			}
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + trimmed[:sep] + ": \"***\""
		}
		return strings.Join(lines, "\n")
	}
}

// maskStructuredSensitive 解析 JSON/YAML，按点分路径就地遮罩，再序列化回原格式。
//
// 返回 ok=false 表示「解析不了」，调用方应回退到行级启发式——绝不能因为
// 内容不规整就当「没有敏感项」（那等于把脱敏悄悄关掉）。
func maskStructuredSensitive(format, content string, paths []string) (string, bool) {
	var doc any
	isJSON := false
	// 先试 JSON（严格）：ConfigFormatJSON 的内容一定是 JSON。
	// **UseNumber**：避免 float64 把 >2^53 的长整型（雪花 ID / accountId）静默改值。
	// 这也是「用户只改了一行 motd，无关字段却变了」那类脏改的根源。
	dec := json.NewDecoder(strings.NewReader(content))
	dec.UseNumber()
	if err := dec.Decode(&doc); err == nil {
		isJSON = true
	} else {
		// 再试 YAML（宽松，能解析大部分 JSON 但不是全部）。
		var y any
		if yerr := yaml.Unmarshal([]byte(content), &y); yerr != nil {
			return "", false
		}
		doc = y
	}
	nodes, ok := doc.(map[string]any)
	if !ok {
		return "", false
	}
	changed := false
	for _, path := range paths {
		if maskPathInMap(nodes, strings.Split(path, ".")) {
			changed = true
		}
	}
	if !changed {
		// 路径一个都没命中：不重排内容（重序列化会改变缩进/注释/键序，
		// 而展示层要求稳定）。返回 true 表示「已按结构确认无命中」，不必再走启发式。
		return content, true
	}
	// **按原格式回写**：把 YAML 配置返回成 JSON 会让「展示」变成「换了个文件」，
	// 用户对照时不认识自己的文件（既有测试也锁住了这一点）。
	if isJSON || format == string(model.ConfigFormatJSON) {
		if b, err := json.MarshalIndent(nodes, "", "  "); err == nil {
			return string(b), true
		}
		return "", false
	}
	if b, err := yaml.Marshal(nodes); err == nil {
		return string(b), true
	}
	return "", false
}

// maskPathInMap 沿点分路径定位并遮罩；数组段用十进制下标。
func maskPathInMap(node any, segs []string) bool {
	if len(segs) == 0 {
		return false
	}
	switch cur := node.(type) {
	case map[string]any:
		key := segs[0]
		if len(segs) == 1 {
			if _, exists := cur[key]; !exists {
				return false
			}
			cur[key] = "***"
			return true
		}
		child, exists := cur[key]
		if !exists {
			return false
		}
		return maskPathInMap(child, segs[1:])
	case []any:
		idx, err := strconv.Atoi(segs[0])
		if err != nil || idx < 0 || idx >= len(cur) {
			return false
		}
		if len(segs) == 1 {
			cur[idx] = "***"
			return true
		}
		return maskPathInMap(cur[idx], segs[1:])
	default:
		return false
	}
}

// encodeSensitivePaths 把敏感路径列表序列化为 JSON 文本（空列表存空串）。
func encodeSensitivePaths(paths []string) (string, error) {
	clean := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return "", nil
	}
	sort.Strings(clean)
	b, err := json.Marshal(clean)
	if err != nil {
		return "", fmt.Errorf("敏感路径序列化失败: %w", err)
	}
	return string(b), nil
}

// decodeSensitivePaths 解析敏感路径 JSON 文本；非法内容返回空（不阻断展示）。
func decodeSensitivePaths(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// validateSchemaJSONText 校验 schema_json 是否为合法 JSON 对象。
func validateSchemaJSONText(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return fmt.Errorf("schemaJson 不是合法 JSON 对象: %w", err)
	}
	return nil
}
