package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 配置分层覆盖服务（FR-529）。
//
// 职责边界：
//   - 只管「配置的定义」：配置文件元数据 + 各作用域的不可变版本链；
//   - 合并/校验是纯函数（config_merge.go / config_schema.go），本文件只做取数与编排；
//   - **不写实例落盘**——下发与生效属 FR-531/532，落盘核心属 FR-530。

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
)

// ConfigFileService 配置分层覆盖服务（FR-529）。
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
func (s *ConfigFileService) LayerHistory(configFileID uint, level model.ConfigScopeLevel, scopeID uint) ([]LayerVersionView, error) {
	if _, err := s.mustGet(configFileID); err != nil {
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
	out := make([]LayerVersionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, LayerVersionView{
			ID:               r.ID,
			VersionNo:        r.VersionNo,
			ContentHash:      r.ContentHash,
			Content:          r.Content,
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
	// 该层原文的语法与 schema 校验。
	if issues := s.validateLayerContent(file, in.Content); len(issues) > 0 {
		return nil, &ConfigLayerValidationError{Issues: issues}
	}
	return s.appendLayer(file, level, in.ScopeID, in.Content, in.Remark, nil, authorID)
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
// 不存在的引用会让「配了却没人收到」的错觉发生，故 fail-closed（与 FR-458 同口径）。
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

// EffectiveContent 只取合并后的内容与 hash（供 FR-530/531 下发链路复用，不做脱敏）。
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
	return merged.Content, merged.EffectiveHash, nil
}

// applySensitiveMask 把敏感键的值脱敏为 ***（§8）。
// 内容 hash 基于明文计算，故脱敏只作用于展示字段（traced 的值与 content 里对应行）。
func applySensitiveMask(sensitiveJSON string, preview *EffectivePreview) {
	paths := decodeSensitivePaths(sensitiveJSON)
	if len(paths) == 0 || preview == nil {
		return
	}
	for _, p := range paths {
		if tv, ok := preview.Traced[p]; ok {
			tv.Value = "***"
			preview.Traced[p] = tv
		}
	}
	preview.Content = maskSensitiveInContent(preview.Format, preview.Content, paths)
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
		// yaml/json：按「末段键名」匹配形如 `key: value` / `"key": value` 的行。
		// 这是展示层的启发式，不做结构解析——宁可少脱敏也不能改坏展示。
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			sep := strings.Index(trimmed, ":")
			if sep < 0 {
				continue
			}
			key := strings.Trim(strings.TrimSpace(trimmed[:sep]), `"'`)
			if _, ok := want[key]; !ok {
				continue
			}
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + trimmed[:sep] + ": \"***\""
		}
		return strings.Join(lines, "\n")
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
