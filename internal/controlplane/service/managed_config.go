package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 托管探针配置的三层存储与合并。
//
// ## 与上游地基的关系（本文件不重复实现任何分级判断）
// 键的档位真源是上游的 66 键表（`ManagedConfigTierOf` / `managedConfigTiers`）。
// 本文件在**写入前**调它们做预检：这样做不是为了替代探针的校档（探针的拒绝才是最终事实），
// 而是为了在运维点保存的这一刻就把「哪个键越界」说清楚——探针那边的错误文案是笼统的整包拒绝。
//
// ## 为什么是「增量」而不是「全量快照」
// 每层只存自己写过的键。若层存全量，全局层之后新增一个键，节点层的旧快照会把它盖掉，
// 且没人能看出这是「节点有意覆盖」还是「节点从没听说过这个键」。增量让这两件事天然可区分。
//
// ## 合并语义（与既有 config_merge.go 同口径，不共用实现）
// 后者合并的是**配置文件的原文**（YAML/JSON/properties 三种格式、要序列化回文本、
// 要保留数字字面量）。这里的输入是已经解析好的点分键值对（协议 §3.4.1 的 `values` 形态），
// 输出同样是点分键值对。硬套那边的实现要为「点分键 → 嵌套对象 → 点分键」来回转换三次，
// 反而更容易出错，故按同一套规则独立实现，并由单测逐条断言两边的共同语义。
//
// 合并规则（低 → 高）：
//   - 对象递归合并；
//   - 标量覆盖；
//   - **数组整体替换**（不做元素级合并——半新半旧的数组没有任何可解释的语义）；
//   - **显式 null 删键**（删掉低层设的值，并在逐键来源里留痕）。

// 托管层相关的错误。
var (
	// ErrManagedScopeInvalid 作用域非法。
	ErrManagedScopeInvalid = errors.New("托管配置作用域非法（只支持 global / node / instance）")
	// ErrManagedScopeRefInvalid 作用域引用与作用域级别不匹配（global 必须 0，其它必须 >0）。
	ErrManagedScopeRefInvalid = errors.New("托管配置作用域引用与作用域级别不匹配")
	// ErrManagedConfigForbidden 增量里含禁止远程写入的键（或未登记的键）。
	ErrManagedConfigForbidden = errors.New("托管配置含禁止远程写入的键")
	// ErrManagedConfigInvalid 增量内容不是合法 JSON 对象。
	ErrManagedConfigInvalid = errors.New("托管配置必须是 JSON 对象")
	// ErrManagedConfigLayerEmpty 合并结果为空（没有任何键可下发）。
	ErrManagedConfigLayerEmpty = errors.New("合并结果为空，没有可下发的配置键")
	// ErrManagedConfigScopeRefNotFound 作用域引用的节点/实例不存在。
	ErrManagedConfigScopeRefNotFound = errors.New("作用域引用的节点或实例不存在")
)

// ManagedLayerInput 是一次「保存某层」的请求体。
type ManagedLayerInput struct {
	Scope      model.ManagedConfigScope `json:"scope"`
	ScopeRefID uint                     `json:"scopeRefId"`
	// Config 增量配置（嵌套对象 JSON 文本；显式 null 表示删键）。
	Config string `json:"config"`
	// ExpectedVersion 乐观并发基线：调用方看到的当前版本；0 表示「我认为这层还不存在」。
	//
	// 与探针的 baseVersion 不是一回事（那是探针自己的版本序号），但对账口径一致：
	// 两者都是「我改的是我以为的那一版」，不符即拒，避免静默覆盖别人的修改。
	ExpectedVersion int `json:"expectedVersion"`
	// ExpectedLayerIdentity 层身份令牌（层 JSON 的 identity）：版本说明「第几代」，
	// 令牌说明「同一个层」。删层重建会让版本撞回 v1，但新层令牌不同，基于旧层的
	// 草稿写入被拒。空串仅当 ExpectedVersion 为 0（认为层尚不存在）时使用。
	ExpectedLayerIdentity string `json:"expectedLayerIdentity"`
	Remark                string `json:"remark"`
}

// ManagedTracedKey 是一个键的最终值与其来源。
type ManagedTracedKey struct {
	// Key config.yml 的点分路径。
	Key string `json:"key"`
	// Value 最终值（标量/数组/对象）；被删除的键为 null。
	Value any `json:"value"`
	// Scope/ScopeRefID/ScopeName/Version 该值来自哪一层、哪一版。
	Scope      model.ManagedConfigScope `json:"scope"`
	ScopeRefID uint                     `json:"scopeRefId"`
	ScopeName  string                   `json:"scopeName,omitempty"`
	Version    int                      `json:"version"`
	// Deleted true 表示该键被更高层的显式 null 删除。
	Deleted bool `json:"deleted,omitempty"`
	// Tier 该键的档位（取上游 ManagedConfigTierOf，未登记按禁止）。
	Tier       string `json:"tier"`
	TierKnown  bool   `json:"tierKnown"`
	TierForbid bool   `json:"tierForbidden,omitempty"`
}

// ManagedMergeResult 是一次三层合并的产物。
type ManagedMergeResult struct {
	// Values 合并后的点分键值（可直接填进上游 ManagedConfigPush.Values）。
	Values map[string]any `json:"values"`
	// Removals 需要从探针侧显式删除的路径（合并后值为 null 的键仍在 Values 里，
	// 这里给出纯删除清单，便于界面与对账解释）。
	Removals []string `json:"removals"`
	// Traced 逐键来源（对账矩阵的下钻用）。
	Traced []ManagedTracedKey `json:"traced"`
	// GuardedKeys 本次合并结果里触及 GUARDED 档的键（影响预估与界面提示用）。
	GuardedKeys []string `json:"guardedKeys"`
	// ForbiddenKeys 越界的键（非空即不可下发）。
	ForbiddenKeys []string `json:"forbiddenKeys"`
}

// ManagedConfigService 托管配置分层存储与合并。
type ManagedConfigService struct {
	db *gorm.DB
}

// NewManagedConfigService 创建托管配置服务。
func NewManagedConfigService(db *gorm.DB) *ManagedConfigService {
	return &ManagedConfigService{db: db}
}

// ---------------------------------------------------------------------------
// 存储层
// ---------------------------------------------------------------------------

// Layers 列出全部（或某作用域下的）配置层。
//
// scope 为空时返回三层全覆盖的清单，供界面一次看全。
func (s *ManagedConfigService) Layers(scope model.ManagedConfigScope) ([]model.ManagedConfigLayer, error) {
	q := s.db.Order("scope ASC, scope_ref_id ASC")
	if scope != "" {
		if !model.ValidManagedConfigScope(scope) {
			return nil, ErrManagedScopeInvalid
		}
		q = q.Where("scope = ?", scope)
	}
	var rows []model.ManagedConfigLayer
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询托管配置层失败: %w", err)
	}
	// 按优先级排序（global → node → instance），让界面与合并顺序一致。
	sort.SliceStable(rows, func(i, j int) bool {
		pi, pj := model.ManagedScopePriority(rows[i].Scope), model.ManagedScopePriority(rows[j].Scope)
		if pi != pj {
			return pi < pj
		}
		return rows[i].ScopeRefID < rows[j].ScopeRefID
	})
	return rows, nil
}

// GetLayer 读一层（不存在返回空记录 + found=false）。
func (s *ManagedConfigService) GetLayer(scope model.ManagedConfigScope, scopeRefID uint) (model.ManagedConfigLayer, bool, error) {
	if !model.ValidManagedConfigScope(scope) {
		return model.ManagedConfigLayer{}, false, ErrManagedScopeInvalid
	}
	var row model.ManagedConfigLayer
	err := s.db.Where("scope = ? AND scope_ref_id = ?", scope, scopeRefID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ManagedConfigLayer{}, false, nil
	}
	if err != nil {
		return model.ManagedConfigLayer{}, false, fmt.Errorf("查询托管配置层失败: %w", err)
	}
	return row, true, nil
}

// SaveLayer 保存一层（新建或版本自增）。
//
// 写入前做三道预检，任一不过即整体拒绝：
//  1. 增量必须是 JSON **对象**（数组/标量不是合法的配置载体）；
//  2. 每个键必须过上游 `ValidateConfigPush`（禁档 + 未登记 fail-close）；
//  3. 显式 null 视为「删键」，它同样不得指向禁档路径（删一个禁档键与写它同属越界）。
//
// 预检刻意在**保存**这一刻做，而不是等到下发：保存是运维的直接动作，此刻报错他能立刻改；
// 等到下发才拒，一层坏配置会一直躺在库里，且每次下发都失败一次。
func (s *ManagedConfigService) SaveLayer(in ManagedLayerInput, operatorID uint) (*model.ManagedConfigLayer, error) {
	if !model.ValidManagedConfigScope(in.Scope) {
		return nil, ErrManagedScopeInvalid
	}
	if err := validateManagedScopeRef(s.db, in.Scope, in.ScopeRefID); err != nil {
		return nil, err
	}
	flat, err := parseManagedConfigObject(in.Config)
	if err != nil {
		return nil, err
	}
	// 调上游校验：把本层增量交给 ManageConfigPush 的禁档/未登记校验一次报全。
	if err := validateManagedValues(flat); err != nil {
		return nil, err
	}
	normalized, err := marshalManagedConfig(flat)
	if err != nil {
		return nil, err
	}

	scopeName := resolveManagedScopeName(s.db, in.Scope, in.ScopeRefID)

	var saved model.ManagedConfigLayer
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var existing model.ManagedConfigLayer
		findErr := tx.Where("scope = ? AND scope_ref_id = ?", in.Scope, in.ScopeRefID).First(&existing).Error
		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			// 新建：期望版本必须为 0（调用方认为这层不存在）。否则是并发冲突。
			if in.ExpectedVersion != 0 {
				return fmt.Errorf("%w: 该层尚不存在，期望版本应为 0（实际 %d）", ErrManagedConfigConflict, in.ExpectedVersion)
			}
			// 带着身份令牌的请求说明草稿基于某个已存在的层——层没了即冲突，
			// 不能因为版本号恰好允许就往新层上写。
			if in.ExpectedLayerIdentity != "" {
				return fmt.Errorf("%w: 草稿所基于的层已被删除", ErrManagedConfigConflict)
			}
			saved = model.ManagedConfigLayer{
				Scope:      in.Scope,
				ScopeRefID: in.ScopeRefID,
				ScopeName:  scopeName,
				Config:     normalized,
				Version:    1,
				Identity:   newManagedLayerIdentity(),
				Remark:     strings.TrimSpace(in.Remark),
				CreatedBy:  operatorID,
			}
			if err := tx.Create(&saved).Error; err != nil {
				// 唯一索引撞车同样是并发冲突，不该报成 500。
				return fmt.Errorf("%w: 该层已被其他保存创建", ErrManagedConfigConflict)
			}
			return nil
		case findErr != nil:
			return fmt.Errorf("查询托管配置层失败: %w", findErr)
		}
		// 已存在：乐观并发校验。
		if in.ExpectedVersion != existing.Version {
			return fmt.Errorf("%w: 该层当前版本 %d，你基于 %d 保存",
				ErrManagedConfigConflict, existing.Version, in.ExpectedVersion)
		}
		// 身份令牌校验：删层重建后版本会撞回 v1，版本相等不代表是同一个层——
		// 令牌不符说明草稿基于的是已删的旧层，写入会静默盖掉新层内容。
		// 老数据（identity 为空）允许空令牌放行，同时在本事务里补上令牌。
		if existing.Identity != "" && in.ExpectedLayerIdentity != existing.Identity {
			return fmt.Errorf("%w: 该层已被删除重建，请重新加载后基于新层编辑", ErrManagedConfigConflict)
		}
		identity := existing.Identity
		if identity == "" {
			identity = newManagedLayerIdentity()
		}
		// 条件更新：把版本与身份令牌作为 WHERE 条件，避免两个并发请求都通过上面的读校验。
		res := tx.Model(&model.ManagedConfigLayer{}).
			Where("id = ? AND version = ? AND identity = ?", existing.ID, existing.Version, existing.Identity).
			Updates(map[string]any{
				"config":     normalized,
				"version":    existing.Version + 1,
				"identity":   identity,
				"scope_name": scopeName,
				"remark":     strings.TrimSpace(in.Remark),
			})
		if res.Error != nil {
			return fmt.Errorf("保存托管配置层失败: %w", res.Error)
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("%w: 该层已被其他保存更新", ErrManagedConfigConflict)
		}
		saved = existing
		saved.Config = normalized
		saved.Version = existing.Version + 1
		saved.Identity = identity
		saved.ScopeName = scopeName
		saved.Remark = strings.TrimSpace(in.Remark)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &saved, nil
}

// ErrManagedConfigConflict 层版本被其它写入者抢先更新（乐观并发失败）。
var ErrManagedConfigConflict = errors.New("该层已被其他保存更新，请重新加载后再改")

// newManagedLayerIdentity 生成层身份令牌：16 字节随机十六进制。
// 用独立令牌而非自增主键——SQLite 会复用被删行的 rowid，单靠 ID
// 无法区分「同一层」与「删层重建的版本撞车」。
func newManagedLayerIdentity() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败极端罕见；退化为纳秒时间戳也不复用。
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// DeleteLayer 删除一层（把该层的增量整体撤掉）。
func (s *ManagedConfigService) DeleteLayer(scope model.ManagedConfigScope, scopeRefID uint) error {
	if !model.ValidManagedConfigScope(scope) {
		return ErrManagedScopeInvalid
	}
	res := s.db.Where("scope = ? AND scope_ref_id = ?", scope, scopeRefID).Delete(&model.ManagedConfigLayer{})
	if res.Error != nil {
		return fmt.Errorf("删除托管配置层失败: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrManagedConfigNotFound
	}
	return nil
}

// ErrManagedConfigNotFound 托管配置层不存在。
var ErrManagedConfigNotFound = errors.New("托管配置层不存在")

// ---------------------------------------------------------------------------
// 合并
// ---------------------------------------------------------------------------

// layerPayload 是落库的增量配置（嵌套对象），merged 时按优先级依次叠加。
type layerPayload struct {
	layer model.ManagedConfigLayer
	doc   map[string]any
}

// MergeForInstance 合并 global → node → instance 三层，得到该实例的有效配置与逐键来源。
//
// 三层里缺哪层就跳哪层（空层等价于「没意见」，不是「清空」——清空要靠显式 null）。
func (s *ManagedConfigService) MergeForInstance(inst model.Instance) (*ManagedMergeResult, error) {
	layers, err := s.loadMergeChain(inst)
	if err != nil {
		return nil, err
	}
	return mergeManagedLayers(layers)
}

// loadMergeChain 取某实例的三层（按优先级升序）。
func (s *ManagedConfigService) loadMergeChain(inst model.Instance) ([]layerPayload, error) {
	var rows []model.ManagedConfigLayer
	err := s.db.Where(
		"(scope = ? AND scope_ref_id = 0) OR (scope = ? AND scope_ref_id = ?) OR (scope = ? AND scope_ref_id = ?)",
		model.ManagedScopeGlobal, model.ManagedScopeNode, inst.NodeID, model.ManagedScopeInstance, inst.ID,
	).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("查询托管配置层失败: %w", err)
	}
	out := make([]layerPayload, 0, len(rows))
	for _, row := range rows {
		doc, parseErr := parseManagedConfigObject(row.Config)
		if parseErr != nil {
			// 库里的历史脏数据不该让整次合并崩掉：跳过该层并报错由调用方决定。
			return nil, fmt.Errorf("托管配置层（%s/%d）内容不是合法 JSON 对象: %w", row.Scope, row.ScopeRefID, parseErr)
		}
		out = append(out, layerPayload{layer: row, doc: doc})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return model.ManagedScopePriority(out[i].layer.Scope) < model.ManagedScopePriority(out[j].layer.Scope)
	})
	return out, nil
}

// mergeManagedLayers 是合并的唯一实现（纯函数，便于穷举单测）。
//
// 命名空间口径：合并只在**扁平的 config.yml 点分键**上进行（与协议 §3.4.1 的 `values`
// 逐字一致）。因此每层落地时先把嵌套 JSON 展平成点分键——若允许「点分键」与「同名的
// 嵌套对象」同时存在于同一个 map 里，`aggregation` 与 `aggregation.window` 就会变成两个
// 互不相干的键，合并结果无法解释，也无法原样交给探针。
//
// 「对象递归合并」在展平后自然成立：两层各写 `aggregation.window` 时，高层覆盖低层；
// 两层各写 `alert.channels` 下的不同子键时，两边的子键同时保留。
func mergeManagedLayers(layers []layerPayload) (*ManagedMergeResult, error) {
	merged := map[string]any{}
	traced := map[string]ManagedTracedKey{}

	for _, lp := range layers {
		src := ManagedTracedKey{
			Scope:      lp.layer.Scope,
			ScopeRefID: lp.layer.ScopeRefID,
			ScopeName:  lp.layer.ScopeName,
			Version:    lp.layer.Version,
		}
		mergeManagedInto(merged, lp.doc, src, traced)
	}

	res := &ManagedMergeResult{
		Values:   make(map[string]any, len(merged)),
		Removals: []string{},
		Traced:   make([]ManagedTracedKey, 0, len(traced)),
	}
	for k, v := range merged {
		res.Values[k] = v
	}

	// 逐键来源按路径字典序输出，保证稳定（前端与测试都不该依赖 map 迭代顺序）。
	tracedKeys := make([]string, 0, len(traced))
	for k := range traced {
		tracedKeys = append(tracedKeys, k)
	}
	sort.Strings(tracedKeys)
	for _, k := range tracedKeys {
		entry := traced[k]
		tier, known := ManagedConfigTierOf(k)
		entry.Tier = tier.String()
		entry.TierKnown = known
		entry.TierForbid = IsForbiddenConfigPath(k)
		if entry.Deleted {
			entry.Value = nil
			res.Removals = append(res.Removals, k)
		}
		res.Traced = append(res.Traced, entry)
		if entry.TierForbid {
			res.ForbiddenKeys = append(res.ForbiddenKeys, k)
			continue
		}
		if tier == ConfigTierGuarded && !entry.Deleted {
			res.GuardedKeys = append(res.GuardedKeys, k)
		}
	}
	sort.Strings(res.Removals)
	sort.Strings(res.ForbiddenKeys)
	sort.Strings(res.GuardedKeys)
	return res, nil
}

// mergeManagedInto 把一层（已展平的点分键值）并入累积结果。
//
// 三类动作：
//   - 显式 null → 删键（连同其子树），并在 traced 留痕（谁在哪一版删的）；
//   - 标量 / 数组 → 整体覆盖（数组不做元素级合并）；
//   - 其余 → 直接覆盖该点分键。
//
// 父子冲突的处理：设置 `a.b` 会使标量 `a` 失效（同一个键不可能既是标量又是对象），
// 反之设置标量 `a` 也会清掉 `a.*`。取「后设者赢」而不做类型冲突告警：
// 在点分命名空间里这是唯一自洽的解释，且合并结果与探针能接收的形态一致。
func mergeManagedInto(dst map[string]any, src map[string]any, srcKey ManagedTracedKey, traced map[string]ManagedTracedKey) {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, path := range keys {
		v := src[path]

		// 显式 null：删键。删掉低层遗留值，并在 traced 里留痕。
		if v == nil {
			removeManagedSubtree(dst, traced, path)
			mark := srcKey
			mark.Key = path
			mark.Deleted = true
			mark.Value = nil
			traced[path] = mark
			continue
		}

		removeManagedSubtree(dst, traced, path)
		// 若本层写的是子键 `a.b`，低层把 `a` 当标量/数组设过的值必须一并清掉。
		removeManagedAncestorScalar(dst, traced, path)

		dst[path] = v
		mark := srcKey
		mark.Key = path
		mark.Deleted = false
		mark.Value = v
		traced[path] = mark
	}
}

// removeManagedSubtree 删除 path 及其子路径的既有值与溯源（被覆盖或删除时子树失效）。
func removeManagedSubtree(dst map[string]any, traced map[string]ManagedTracedKey, path string) {
	delete(dst, path)
	delete(traced, path)
	prefix := path + "."
	for k := range dst {
		if strings.HasPrefix(k, prefix) {
			delete(dst, k)
		}
	}
	for k := range traced {
		if strings.HasPrefix(k, prefix) {
			delete(traced, k)
		}
	}
}

// removeManagedAncestorScalar 清掉 path 的各级祖先上遗留的标量/数组值。
//
// 只在祖先**确实是标量/数组**时动手：祖先若是普通子键（`a` 与 `a.b` 并存本就合法），
// 不动它。这里判定的是「同一个键不能既是叶子又有子键」这一条硬约束。
func removeManagedAncestorScalar(dst map[string]any, traced map[string]ManagedTracedKey, path string) {
	for idx := strings.LastIndexByte(path, '.'); idx > 0; idx = strings.LastIndexByte(path[:idx], '.') {
		ancestor := path[:idx]
		if _, isMap := dst[ancestor].(map[string]any); isMap {
			continue
		}
		if _, exists := dst[ancestor]; exists {
			delete(dst, ancestor)
			delete(traced, ancestor)
		}
	}
}

// ---------------------------------------------------------------------------
// 预检与影响预估
// ---------------------------------------------------------------------------

// validateManagedValues 用上游分级表预检一组点分键值。
//
// 构造上游 `ManagedConfigPush` 再走等价预检（见 managed_tier_guard.go 的说明：
// 上游 `ValidateConfigPush` 的路径回溯会死循环，故遍历改为本地实现，**档位表仍取上游**）。
func validateManagedValues(values map[string]any) error {
	removals := make([]string, 0)
	payloadValues := make(map[string]any, len(values))
	for k, v := range values {
		if v == nil {
			// null 是「删键」：上游对 removals 有同样的禁档校验，走同一条路。
			removals = append(removals, k)
			continue
		}
		payloadValues[k] = v
	}
	v := ValidateConfigPush(ManagedConfigPush{Values: payloadValues, Removals: removals})
	if !v.OK() {
		return fmt.Errorf("%w: %s", ErrManagedConfigForbidden, strings.Join(v.Errors, "；"))
	}
	return nil
}

// Validate 对一组点分键值做下发前预检（不落库，供保存预览与下发前复校）。
func (s *ManagedConfigService) Validate(values map[string]any) ManagedConfigPushValidation {
	return ValidateConfigPush(ManagedConfigPush{Values: values})
}

// ManagedImpact 是保存/下发前的影响预估。
type ManagedImpact struct {
	// AffectedInstances 受影响的（探针适用）实例数。
	AffectedInstances int `json:"affectedInstances"`
	// TotalInstances 该作用域下的实例总数（含探针不适用者，便于解释差额）。
	TotalInstances int `json:"totalInstances"`
	// GuardedKeys 触及 GUARDED 档的键（这些键会改变数据量/资源占用，需人工确认）。
	GuardedKeys []string `json:"guardedKeys"`
	// ForbiddenKeys 越界键（非空时 affectedInstances 无意义，前端应直接拦下）。
	ForbiddenKeys []string `json:"forbiddenKeys"`
	// MetricMultiplier 指标量倍率（本次合并后的采集频率相对默认的倍数）。
	MetricMultiplier float64 `json:"metricMultiplier"`
	// DefaultCollectPeriodTicks / EffectiveCollectPeriodTicks 采集周期（tick）。
	DefaultCollectPeriodTicks   int `json:"defaultCollectPeriodTicks"`
	EffectiveCollectPeriodTicks int `json:"effectiveCollectPeriodTicks"`
	// Notes 人读的影响说明（前端直接展示）。
	Notes []string `json:"notes"`
}

// defaultCollectPeriodTicks 是探针默认采集周期（tick）。
//
// 与本包 key 目录里 collect-period-ticks 的 Default 对齐：100 tick（约 5 秒）。
// 曾写 20 并注释“与探针侧对齐”——界面目录却一直对用户说默认 100，两套口径并存会让
// 预估倍率在界面上比目录声称的基准放大 5 倍。
const (
	defaultCollectPeriodTicks = 100
	// minCollectPeriodTicks 是倍率计算的下限（1 tick 已经是最激进的采集，再小无意义）。
	minCollectPeriodTicks = 1
)

// EstimateImpact 预估一层配置（或一次合并结果）的影响。
//
// 倍率口径：`默认周期 / 生效周期`。周期减半 → 倍率 2（采集频率翻倍 → 指标量翻倍）。
// 周期有下限（1 tick）：除零会让倍率变成 +Inf，界面显示成「无穷倍」比不显示更糟。
func (s *ManagedConfigService) EstimateImpact(scope model.ManagedConfigScope, scopeRefID uint, values map[string]any, accessible []uint) (*ManagedImpact, error) {
	if !model.ValidManagedConfigScope(scope) {
		return nil, ErrManagedScopeInvalid
	}
	insts, err := s.instancesInScope(scope, scopeRefID)
	if err != nil {
		return nil, err
	}
	// accessible 为 nil 表示不受限；非 nil 时必须收敛到调用方可见的实例——
	// 否则计数与「代表实例」的合并周期都会泄露他组实例的配置信息。
	if accessible != nil {
		allowed := make(map[uint]struct{}, len(accessible))
		for _, id := range accessible {
			allowed[id] = struct{}{}
		}
		visible := insts[:0]
		for _, inst := range insts {
			if _, ok := allowed[inst.ID]; ok {
				visible = append(visible, inst)
			}
		}
		insts = visible
	}
	out := &ManagedImpact{
		TotalInstances:              len(insts),
		DefaultCollectPeriodTicks:   defaultCollectPeriodTicks,
		EffectiveCollectPeriodTicks: defaultCollectPeriodTicks,
		MetricMultiplier:            1,
		Notes:                       []string{},
	}
	applicable := 0
	for _, inst := range insts {
		if model.IsProbeApplicable(inst.Type, inst.Role) {
			applicable++
		}
	}
	out.AffectedInstances = applicable
	if applicable < len(insts) {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"作用域内共 %d 台，其中 %d 台适用探针（代理/Beacon/二进制不适用，将被跳过）",
			len(insts), applicable))
	}

	for k := range values {
		if IsForbiddenConfigPath(k) {
			out.ForbiddenKeys = append(out.ForbiddenKeys, k)
			continue
		}
		if tier, ok := ManagedConfigTierOf(k); ok && tier == ConfigTierGuarded {
			out.GuardedKeys = append(out.GuardedKeys, k)
		}
	}
	sort.Strings(out.ForbiddenKeys)
	sort.Strings(out.GuardedKeys)
	if len(out.ForbiddenKeys) > 0 {
		out.Notes = append(out.Notes, "存在禁止远程写入的键，本次不可下发")
		return out, nil
	}

	// 采集周期可能写成顶层键，也可能写成数值字符串；两种都认，认不出就按默认。
	if raw, ok := values["collect-period-ticks"]; ok {
		if ticks, ok := coercePositiveInt(raw); ok {
			out.EffectiveCollectPeriodTicks = ticks
		} else {
			out.Notes = append(out.Notes, "collect-period-ticks 不是正整数，倍率按默认周期估算")
		}
	} else if len(insts) > 0 {
		// 草稿没改周期时，生效周期可能来自下层（global/node/instance 已有值）：
		// 拿作用域第一台实例的合并结果当代表，比硬按默认周期估准得多。
		if merged, merr := s.MergeForInstance(insts[0]); merr == nil && merged != nil {
			if raw, ok := merged.Values["collect-period-ticks"]; ok {
				if ticks, ok := coercePositiveInt(raw); ok && ticks > 0 {
					out.EffectiveCollectPeriodTicks = ticks
				}
			}
		}
	}
	if out.EffectiveCollectPeriodTicks < minCollectPeriodTicks {
		out.EffectiveCollectPeriodTicks = minCollectPeriodTicks
	}
	out.MetricMultiplier = float64(out.DefaultCollectPeriodTicks) / float64(out.EffectiveCollectPeriodTicks)
	if out.MetricMultiplier != 1 {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"采集周期 %d tick → 指标量约为默认的 %.2f 倍", out.EffectiveCollectPeriodTicks, out.MetricMultiplier))
	}
	if len(out.GuardedKeys) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("触及 %d 个危险档键（影响数据量/资源/观测覆盖）", len(out.GuardedKeys)))
	}
	return out, nil
}

// instancesInScope 取某作用域覆盖的实例。
func (s *ManagedConfigService) instancesInScope(scope model.ManagedConfigScope, scopeRefID uint) ([]model.Instance, error) {
	q := s.db.Model(&model.Instance{})
	switch scope {
	case model.ManagedScopeGlobal:
		// 全平台
	case model.ManagedScopeNode:
		q = q.Where("node_id = ?", scopeRefID)
	case model.ManagedScopeInstance:
		q = q.Where("id = ?", scopeRefID)
	default:
		return nil, ErrManagedScopeInvalid
	}
	var rows []model.Instance
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询作用域实例失败: %w", err)
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// marshalManagedConfig 把点分键增量序列化为紧凑 JSON 对象文本。
func marshalManagedConfig(flat map[string]any) (string, error) {
	// 落库形态就是扁平点分键：读回时不需要再展平一次，也不会出现
	// 「点分键」与「同名嵌套对象」并存的两套表示。
	buf, err := json.Marshal(flat)
	if err != nil {
		return "", fmt.Errorf("序列化托管配置失败: %w", err)
	}
	return string(buf), nil
}

// DecodeManagedConfigObject 把前端提交的 JSON 文本解析为点分键配置对象。
//
// 对外暴露的薄包装，语义与内部的 parseManagedConfigObject 完全一致（空串=空配置、
// 非对象拒绝、数字保留字面量）。供 router 层做「只校验不落库」与「影响预演」使用，
// 避免 handler 自己再写一份解析——两处解析口径不一致会让校验通过但落库失败。
func DecodeManagedConfigObject(raw string) (map[string]any, error) {
	return parseManagedConfigObject(raw)
}

// parseManagedConfigObject 把配置文本解析为**点分键**对象；非对象一律拒绝。
//
// 展平在这里做而不是在合并时做：落库的就是探针能直接接收的形态（协议 §3.4.1 的 values
// 就是点分键），合并阶段因此只需处理「同一命名空间内的高覆盖低」这一种关系。
// 显式 null 的叶子保留为 nil（它是「删键」指令，不是「没有值」）。
//
// ## 为什么同时接受 YAML 与 JSON
// 托管配置本身就是探针 `config.yml` 的增量，前端编辑器产出的是**带注释的 YAML**。
// YAML 是 JSON 的超集，两者用一个解析器都能吃下，因此历史上以 JSON 落库的旧数据
// 不需要迁移，新数据也不必回头再改一遍。只认 JSON 的话，界面写着"改 YAML"、
// 后端却回一句 `invalid character 'c'`，用户在两端之间无法自洽。
//
// 解析顺序：先按 YAML（能覆盖 JSON），失败再按 JSON 报错——JSON 的具体语法错误
// （缺逗号、多余引号）由 encoding/json 给出比 YAML 解析器的报错更贴近用户心智。
func parseManagedConfigObject(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	v, yamlErr := parseManagedConfigYAML(trimmed)
	if yamlErr != nil {
		// YAML 也解不了：优先报 JSON 的错误（内容确实是 JSON 时它的定位更准）。
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		var jsonValue any
		if err := dec.Decode(&jsonValue); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrManagedConfigInvalid, err.Error())
		}
		v = jsonValue
	}
	if _, ok := v.(map[string]any); !ok {
		return nil, fmt.Errorf("%w: 根节点必须是对象（实际 %T）", ErrManagedConfigInvalid, v)
	}
	out := map[string]any{}
	flattenManagedConfig(normalizeJSONNumbers(v), "", out)
	return out, nil
}

// parseManagedConfigYAML 用 yaml.v3 解析配置文本。
//
// yaml.v3 解到 `map[string]interface{}` 时，嵌套映射的键类型是 `string`（不同于
// yaml.v2 的 `map[interface{}]interface{}`），因此可以直接参与后续展平；
// 但仍需把数值统一成 json.Number，避免 `20` 与 `20.0` 在比较时被判成不同值。
func parseManagedConfigYAML(text string) (any, error) {
	var v any
	if err := yaml.Unmarshal([]byte(text), &v); err != nil {
		return nil, err
	}
	return v, nil
}

// flattenManagedConfig 把嵌套对象展平成点分键。
//
// 空对象（`{ "aggregation": {} }`）不产生任何键：它没有可下发的叶子值，
// 若保留成一条 `aggregation = {}`，探针会收到一个它未必接受的中间对象。
func flattenManagedConfig(v any, prefix string, out map[string]any) {
	obj, ok := v.(map[string]any)
	if !ok {
		if prefix != "" {
			out[prefix] = v
		}
		return
	}
	if len(obj) == 0 {
		return
	}
	// 按序遍历：同一文档里 `"aggregation.window": x` 与 `aggregation: {window: y}`
	// 会撞同一个点分键，Go map 无序遍历会让「谁赢」随机。按字典序处理后结果确定：
	// 嵌套对象先展平（`aggregation` 排前），平铺键后写覆盖——行为固定且可测。
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		val := obj[k]
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		flattenManagedConfig(val, path, out)
	}
}

// normalizeJSONNumbers 递归把 json.Number 换成 int64/float64。
func normalizeJSONNumbers(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, val := range typed {
			out[k] = normalizeJSONNumbers(val)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, val := range typed {
			out[i] = normalizeJSONNumbers(val)
		}
		return out
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return i
		}
		if f, err := typed.Float64(); err == nil {
			return f
		}
		return typed.String()
	// YAML 解析器给出的是 Go 原生数值（int/uint64/float64），不是 json.Number。
	// 不在这里归一，同一条配置经 YAML 与经 JSON 进来会得到不同的动态类型，
	// 下游按 int64 取值的地方就会拿到 int 而静默失配（曾让既有断言全部变红）。
	case int:
		return int64(typed)
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	case uint:
		return int64(typed)
	case uint8:
		return int64(typed)
	case uint16:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint64:
		// 超出 int64 的极端值退回浮点，保住量级而不静默溢出。
		if typed > uint64(math.MaxInt64) {
			return float64(typed)
		}
		return int64(typed)
	case float32:
		return float64(typed)
	}
	return v
}

// validateManagedScopeRef 校验作用域与引用的搭配。
func validateManagedScopeRef(db *gorm.DB, scope model.ManagedConfigScope, scopeRefID uint) error {
	switch scope {
	case model.ManagedScopeGlobal:
		if scopeRefID != 0 {
			return fmt.Errorf("%w: global 层的 scopeRefId 必须为 0", ErrManagedScopeRefInvalid)
		}
		return nil
	case model.ManagedScopeNode:
		if scopeRefID == 0 {
			return fmt.Errorf("%w: node 层必须指定节点 ID", ErrManagedScopeRefInvalid)
		}
		var count int64
		if err := db.Model(&model.Node{}).Where("id = ?", scopeRefID).Count(&count).Error; err != nil {
			return fmt.Errorf("查询节点失败: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("%w: 节点 %d 不存在", ErrManagedConfigScopeRefNotFound, scopeRefID)
		}
		return nil
	case model.ManagedScopeInstance:
		if scopeRefID == 0 {
			return fmt.Errorf("%w: instance 层必须指定实例 ID", ErrManagedScopeRefInvalid)
		}
		var count int64
		if err := db.Model(&model.Instance{}).Where("id = ?", scopeRefID).Count(&count).Error; err != nil {
			return fmt.Errorf("查询实例失败: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("%w: 实例 %d 不存在", ErrManagedConfigScopeRefNotFound, scopeRefID)
		}
		return nil
	}
	return ErrManagedScopeInvalid
}

// resolveManagedScopeName 取作用域的可读名（失败返回空串，不影响主流程）。
func resolveManagedScopeName(db *gorm.DB, scope model.ManagedConfigScope, scopeRefID uint) string {
	switch scope {
	case model.ManagedScopeNode:
		var node model.Node
		if err := db.Select("name").First(&node, scopeRefID).Error; err == nil {
			return node.Name
		}
	case model.ManagedScopeInstance:
		var inst model.Instance
		if err := db.Select("name").First(&inst, scopeRefID).Error; err == nil {
			return inst.Name
		}
	}
	return ""
}

// coercePositiveInt 把若干种数值表示收敛成正整数；失败返回 ok=false。
func coercePositiveInt(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		if typed > 0 {
			return typed, true
		}
	case int64:
		if typed > 0 {
			return int(typed), true
		}
	case float64:
		if typed > 0 {
			return int(typed), true
		}
	case json.Number:
		if i, err := typed.Int64(); err == nil && i > 0 {
			return int(i), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil && i > 0 {
			return i, true
		}
	}
	return 0, false
}
