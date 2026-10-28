package service

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 配置纳管（FR-533，见 docs/specs/config-beacon-migration/spec.md）。
//
// 设计要点与「为什么不叫导入」：
//   - 配置文件的事实真源是**服务器磁盘**，Beacon 的层版本只是副本。所以本能力只做
//     「把磁盘现状读入为 config_file + 实例级 v1」，不搬运任何外部系统数据；
//   - **只读磁盘**：本服务的依赖接口 ConfigAdoptSource 只有 Discover / Read 两个只读方法，
//     编译期就拿不到写盘能力，纳管前后文件 mtime 必然不变；
//   - **内容逐字节保真**：直接使用 Worker 返回的原文（Worker 侧是 os.ReadFile 后转 string），
//     不经过 ParseConfigContent / MergeConfigLayers / patchConfig 任何一条会重新序列化的路径。
//     内容 hash 因此天然等于磁盘文件 sha256；
//   - **不覆盖已纳管**：该实例上已经有同名配置的实例层版本时一律跳过并报 ALREADY_ADOPTED，
//     避免误操作冲掉运维后续做的修改。

// 纳管结果的稳定状态码（响应里与 code 分开：status 是可枚举的终态，code 是原因）。
const (
	// AdoptStatusCreated 新建了 config_file 并按需写入 v1。
	AdoptStatusCreated = "created"
	// AdoptStatusAppended 复用已有 config_file，追加了该实例的 v1。
	AdoptStatusAppended = "appended"
	// AdoptStatusUnchanged 无需任何写入（已有 config_file 且未要求建基线）。
	AdoptStatusUnchanged = "unchanged"
	// AdoptStatusAlreadyAdopted 该实例上已纳管，跳过且不覆盖。
	AdoptStatusAlreadyAdopted = "already_adopted"
	// AdoptStatusFailed 本文件失败（错误码见 code）。
	AdoptStatusFailed = "failed"
)

// 纳管动作（便于前端展示「将要/已经做了什么」）。
const (
	// AdoptActionNone 无写入动作。
	AdoptActionNone = "none"
	// AdoptActionCreate 新建 config_file + 实例级 v1。
	AdoptActionCreate = "create_file_and_baseline"
	// AdoptActionAppendVersion 复用 config_file，追加实例级 v1。
	AdoptActionAppendVersion = "append_baseline"
	// AdoptActionCreateFileOnly 只新建 config_file，不建版本。
	AdoptActionCreateFileOnly = "create_file_only"
)

// 纳管错误码（spec §6）。
const (
	// AdoptCodeAdopted 成功纳管。
	AdoptCodeAdopted = "ADOPTED"
	// AdoptCodeAlreadyAdopted 已纳管，跳过（不覆盖）。
	AdoptCodeAlreadyAdopted = "ALREADY_ADOPTED"
	// AdoptCodeFileNotFound 目标文件不在实例工作目录里。
	AdoptCodeFileNotFound = "FILE_NOT_FOUND"
	// AdoptCodeReadFailed 读取失败（权限不足、内容过大等）。
	AdoptCodeReadFailed = "READ_FAILED"
	// AdoptCodeInstanceOffline 实例所在节点未连接，读不到磁盘。
	AdoptCodeInstanceOffline = "INSTANCE_OFFLINE"
	// AdoptCodePathEscape 路径越界（绝对路径 / `..` 穿越）。
	AdoptCodePathEscape = "PATH_ESCAPE"
	// AdoptCodeInstanceNotFound 实例不存在。
	AdoptCodeInstanceNotFound = "INSTANCE_NOT_FOUND"
	// AdoptCodeFormatUnsupported 扩展名推断不出受支持的配置格式（如 .toml / .txt）。
	AdoptCodeFormatUnsupported = "FORMAT_UNSUPPORTED"
	// AdoptCodeFormatConflict 同名 config_file 已按别的格式建过，档案格式不可改。
	AdoptCodeFormatConflict = "FORMAT_CONFLICT"
	// AdoptCodeConfigFileArchived 同名 config_file 在回收站里，需先恢复。
	AdoptCodeConfigFileArchived = "CONFIG_FILE_ARCHIVED"
	// AdoptCodeWriteFailed 落库失败。
	AdoptCodeWriteFailed = "WRITE_FAILED"
)

// 逐实例的纳管状态（批量路径）。
const (
	// AdoptInstanceStatusMatched 该实例命中了待纳管文件。
	AdoptInstanceStatusMatched = "matched"
	// AdoptInstanceStatusNoMatch 该实例没有任何文件命中 pathPatterns。
	AdoptInstanceStatusNoMatch = "no_match"
	// AdoptInstanceStatusFailed 该实例整体失败（如 Discover 失败）。
	AdoptInstanceStatusFailed = "failed"
)

// adoptBatchConcurrency 是批量纳管默认的按实例并发上限。
//
// 只并发「读」（gRPC 往返，是真正的瓶颈），落库阶段恒为单线程串行（见 writeMu），
// 因此这个值不会带来写竞争，也不会把 CP 的 DB 连接池打满。
const adoptBatchConcurrency = 8

// 纳管相关哨兵错误。
var (
	// ErrAdoptPathEscape 路径越界（绝对路径或含 `..`）。
	ErrAdoptPathEscape = errors.New("路径越界：只接受实例工作目录内的相对路径")
	// ErrAdoptPathEmpty 路径为空。
	ErrAdoptPathEmpty = errors.New("文件路径不能为空")
	// ErrAdoptPatternRequired 批量纳管未给出 pathPatterns。
	ErrAdoptPatternRequired = errors.New("批量纳管必须提供 pathPatterns（避免误纳管目录下全部文件）")
	// ErrAdoptFilesRequired 单文件/多文件纳管未给出 files。
	ErrAdoptFilesRequired = errors.New("必须提供 files（单文件/多文件纳管）或 target + pathPatterns（批量纳管）")
)

// ConfigAdoptSource 是纳管所需的「实例磁盘配置只读视图」。
//
// 刻意只开两个只读方法：纳管的硬约束是「读磁盘、绝不写磁盘」，
// 用接口把这条约束变成编译期事实——本服务根本拿不到任何写方法。
// 生产实现是 *ConfigService（Discover/Read 走 Worker gRPC），单测注入 fake。
type ConfigAdoptSource interface {
	Discover(instanceID uint) ([]DiscoveredConfig, bool, error)
	Read(instanceID uint, filePath string) (*ConfigReadResult, error)
}

// AdoptFileItem 是单文件纳管的一项。
type AdoptFileItem struct {
	// InstanceID 目标实例。
	InstanceID uint `json:"instanceId"`
	// Path 实例工作目录内的相对路径。
	Path string `json:"path"`
	// AsName 可选的 config_file 逻辑名；留空则用 path。
	AsName string `json:"asName,omitempty"`
}

// ConfigAdoptRequest 是纳管请求（单文件/多文件与批量两种形态共用一个入口）。
//
// 形态判定：Target 非空 → 批量（target + pathPatterns）；否则 → files 列表。
type ConfigAdoptRequest struct {
	// Files 显式列出的待纳管文件。
	Files []AdoptFileItem `json:"files,omitempty"`
	// Target 批量目标（分组 / 实例集合），复用 BusinessTargetResolver 解析。
	Target *BusinessTarget `json:"target,omitempty"`
	// PathPatterns 批量的通配过滤（语法见 matchConfigPath）。
	PathPatterns []string `json:"pathPatterns,omitempty"`
	// CreateBaseline 是否创建实例级 v1 版本。
	CreateBaseline bool `json:"createBaseline"`
	// DryRun 只报告不写库。
	DryRun bool `json:"dryRun"`
}

// AdoptFileResult 是单个文件的纳管结果。
type AdoptFileResult struct {
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName,omitempty"`
	// Path 规范化后的相对路径。
	Path string `json:"path"`
	// Name config_file 逻辑名。
	Name   string `json:"name"`
	Format string `json:"format,omitempty"`
	// Size 内容字节数（== 磁盘文件大小）。
	Size int `json:"size"`
	// ContentHash sha256(磁盘原文) 小写 hex；与写入层版本的 content_hash 一致。
	ContentHash  string `json:"contentHash,omitempty"`
	ConfigFileID uint   `json:"configFileId,omitempty"`
	VersionID    uint   `json:"versionId,omitempty"`
	VersionNo    int    `json:"versionNo,omitempty"`
	Status       string `json:"status"`
	Action       string `json:"action,omitempty"`
	Code         string `json:"code,omitempty"`
	Message      string `json:"message,omitempty"`
	// Warning 内容可读但解析告警（如 YAML 语法错误）——纳管照常记录现状，仅提示。
	Warning string `json:"warning,omitempty"`
	DryRun  bool   `json:"dryRun"`
}

// AdoptInstanceResult 是批量纳管的逐实例结果。
type AdoptInstanceResult struct {
	InstanceID   uint   `json:"instanceId"`
	InstanceName string `json:"instanceName,omitempty"`
	Status       string `json:"status"`
	Code         string `json:"code,omitempty"`
	Message      string `json:"message,omitempty"`
	// DiscoveredFiles Discover 到的配置文件总数。
	DiscoveredFiles int `json:"discoveredFiles"`
	// MatchedFiles 命中 pathPatterns 的文件数。
	MatchedFiles int `json:"matchedFiles"`
	// Truncated Discover 是否被规模上限截断（截断时结果可能不完整）。
	Truncated bool              `json:"truncated"`
	Results   []AdoptFileResult `json:"results,omitempty"`
}

// AdoptSummary 是纳管汇总。
type AdoptSummary struct {
	Total   int `json:"total"`
	Created int `json:"created"`
	// Appended 复用已有 config_file 并追加了基线版本。
	Appended       int `json:"appended"`
	Unchanged      int `json:"unchanged"`
	AlreadyAdopted int `json:"alreadyAdopted"`
	Failed         int `json:"failed"`
	// Instances 批量时参与的目标实例数。
	Instances int `json:"instances,omitempty"`
	// TruncatedInstances Discover 被截断的实例数。
	TruncatedInstances int  `json:"truncatedInstances,omitempty"`
	DryRun             bool `json:"dryRun"`
}

// ConfigAdoptResponse 是纳管响应。
type ConfigAdoptResponse struct {
	// Results 单文件/多文件纳管的逐文件结果（批量形态下为空，逐实例结果在 instances 里）。
	Results   []AdoptFileResult     `json:"results"`
	Instances []AdoptInstanceResult `json:"instances,omitempty"`
	Summary   AdoptSummary          `json:"summary"`
	// DryRunMode 提醒调用方本次没有写库。
	DryRunMode bool `json:"dryRun"`
}

// ConfigHealthView 是配置纳管健康自检视图（spec §11）。
//
// **只报告 JM 侧事实，不做任何自动判定**：JM 无法从自身探测 Beacon 侧的配置开关
// （那是另一个系统的状态），据此「告警」只会把正常的拓扑同步误报成双写风险。
type ConfigHealthView struct {
	ConfigFileCount   int64  `json:"configFileCount"`
	LayerVersionCount int64  `json:"layerVersionCount"`
	LastWriteAt       string `json:"lastWriteAt,omitempty"`
	Notice            string `json:"notice"`
}

// adoptHealthNotice 是固定提示文案（不做自动判定时的替代物）。
const adoptHealthNotice = "若 Beacon 侧配置功能仍开启，请确认不会两边同时修改同一份配置。JM 无法自动检测该状态。"

// ConfigAdoptService 配置纳管服务（FR-533）。
type ConfigAdoptService struct {
	db       *gorm.DB
	files    *ConfigFileService
	source   ConfigAdoptSource
	resolver *BusinessTargetResolver
	now      func() time.Time

	// writeMu 串行化全部「落库」段。
	//
	// 为什么需要：同名 config_file 在多个实例并行纳管时会被同时创建，
	// config_file.name 与 (config_file_id, scope_level, scope_ref_id, version_no)
	// 都是唯一索引，并行写必然撞索引报错。落库是本地毫秒级操作，
	// 而真正的瓶颈是读磁盘（gRPC 往返），因此「并发读 + 串行写」既不牺牲吞吐也不会冲突。
	writeMu sync.Mutex

	// concurrency 批量纳管的按实例并发上限（只作用于读阶段）。
	concurrency int
}

// NewConfigAdoptService 创建配置纳管服务。
//
// files 必须非 nil（用于建 config_file 与追加层版本）；source 为只读磁盘视图。
func NewConfigAdoptService(db *gorm.DB, files *ConfigFileService, source ConfigAdoptSource) *ConfigAdoptService {
	return &ConfigAdoptService{
		db:          db,
		files:       files,
		source:      source,
		resolver:    NewBusinessTargetResolver(db),
		now:         time.Now,
		concurrency: adoptBatchConcurrency,
	}
}

// SetConcurrency 覆盖批量纳管的并发上限（测试与容量调优用）。
func (s *ConfigAdoptService) SetConcurrency(n int) {
	if n > 0 {
		s.concurrency = n
	}
}

// adoptTask 是一个待纳管文件读阶段产出的中间结果。
//
// 读（慢、可并发）与写（快、必须串行）用这个结构解耦：
// 读阶段只填 Content/ContentHash 或 ReadErrCode，写阶段只消费它，不再碰磁盘。
type adoptTask struct {
	InstanceID   uint
	InstanceName string
	Path         string
	Name         string
	Format       model.ConfigFileFormat
	// Content 磁盘原文，逐字节保真；绝不在本服务内被重新序列化。
	Content     string
	ContentHash string

	// ReadErrCode 非空表示读阶段已失败，写阶段原样落这个结果。
	ReadErrCode    string
	ReadErrMessage string
}

// Adopt 执行纳管（单文件/多文件 或 批量，由请求形态决定）。
func (s *ConfigAdoptService) Adopt(req ConfigAdoptRequest, authorID uint) (*ConfigAdoptResponse, error) {
	if req.Target != nil {
		return s.adoptBatch(req, authorID)
	}
	return s.adoptFiles(req, authorID)
}

// ---------------------------------------------------------------------------
// 单文件 / 多文件纳管
// ---------------------------------------------------------------------------

// adoptFiles 按显式文件列表纳管。
func (s *ConfigAdoptService) adoptFiles(req ConfigAdoptRequest, authorID uint) (*ConfigAdoptResponse, error) {
	if len(req.Files) == 0 {
		return nil, ErrAdoptFilesRequired
	}
	if s.source == nil {
		return nil, fmt.Errorf("磁盘配置读取源未初始化")
	}
	if s.files == nil {
		return nil, fmt.Errorf("配置文件服务未初始化")
	}
	tasks := s.readExplicitFiles(req.Files)
	resp := &ConfigAdoptResponse{Results: make([]AdoptFileResult, 0, len(tasks)), DryRunMode: req.DryRun}
	for _, t := range tasks {
		resp.Results = append(resp.Results, s.applyTask(t, req.CreateBaseline, req.DryRun, authorID))
	}
	resp.Summary = summarizeAdopt(resp.Results, 0, 0)
	resp.Summary.DryRun = req.DryRun
	return resp, nil
}

// readExplicitFiles 读取显式文件列表：按实例分组并发，组内串行。
//
// 组内串行是刻意的：同一实例上并发读配置文件会让同一 Worker 的连接被抢占，
// 而纳管通常是「一个实例十几份配置」，串行足够快（本地读，毫秒级）。
func (s *ConfigAdoptService) readExplicitFiles(items []AdoptFileItem) []adoptTask {
	tasks := make([]adoptTask, len(items))
	groups := make(map[uint][]int, len(items))
	order := make([]uint, 0, len(items))
	for i, it := range items {
		if _, ok := groups[it.InstanceID]; !ok {
			order = append(order, it.InstanceID)
		}
		groups[it.InstanceID] = append(groups[it.InstanceID], i)
	}
	conc := s.concurrency
	if conc <= 0 {
		conc = adoptBatchConcurrency
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, instanceID := range order {
		indexes := groups[instanceID]
		sem <- struct{}{}
		wg.Add(1)
		go func(instanceID uint, indexes []int) {
			defer wg.Done()
			defer func() { <-sem }()
			for _, idx := range indexes {
				it := items[idx]
				tasks[idx] = s.readTask(instanceID, it.Path, it.AsName)
			}
		}(instanceID, indexes)
	}
	wg.Wait()
	return tasks
}

// ---------------------------------------------------------------------------
// 批量纳管
// ---------------------------------------------------------------------------

// adoptInstancePlan 是批量纳管的逐实例中间结果。
type adoptInstancePlan struct {
	InstanceID      uint
	InstanceName    string
	Status          string
	Code            string
	Message         string
	DiscoveredFiles int
	MatchedFiles    int
	Truncated       bool
	Tasks           []adoptTask
}

// adoptBatch 按分组 / 实例集合批量纳管。
func (s *ConfigAdoptService) adoptBatch(req ConfigAdoptRequest, authorID uint) (*ConfigAdoptResponse, error) {
	if s.source == nil {
		return nil, fmt.Errorf("磁盘配置读取源未初始化")
	}
	if s.files == nil {
		return nil, fmt.Errorf("配置文件服务未初始化")
	}
	if s.resolver == nil {
		return nil, fmt.Errorf("目标解析器未初始化")
	}
	if req.Target == nil || req.Target.Empty() {
		return nil, ErrBusinessTargetUnspecified
	}
	patterns := normalizeAdoptPatterns(req.PathPatterns)
	if len(patterns) == 0 {
		return nil, ErrAdoptPatternRequired
	}
	resolved, err := s.resolver.Resolve(*req.Target)
	if err != nil {
		return nil, err
	}
	plans := s.readBatch(resolved, patterns)

	resp := &ConfigAdoptResponse{
		Results:    []AdoptFileResult{},
		Instances:  make([]AdoptInstanceResult, 0, len(plans)),
		DryRunMode: req.DryRun,
	}
	truncatedInstances := 0
	for _, plan := range plans {
		inst := AdoptInstanceResult{
			InstanceID:      plan.InstanceID,
			InstanceName:    plan.InstanceName,
			Status:          plan.Status,
			Code:            plan.Code,
			Message:         plan.Message,
			DiscoveredFiles: plan.DiscoveredFiles,
			MatchedFiles:    plan.MatchedFiles,
			Truncated:       plan.Truncated,
			Results:         make([]AdoptFileResult, 0, len(plan.Tasks)),
		}
		if plan.Truncated {
			truncatedInstances++
		}
		// 落库仍按 resolved 顺序单线程推进：部分实例失败不影响其余实例（spec 验收 8）。
		for _, t := range plan.Tasks {
			inst.Results = append(inst.Results, s.applyTask(t, req.CreateBaseline, req.DryRun, authorID))
		}
		resp.Instances = append(resp.Instances, inst)
	}
	resp.Summary = summarizeAdopt(flattenAdoptResults(resp.Instances), len(plans), truncatedInstances)
	resp.Summary.DryRun = req.DryRun
	return resp, nil
}

// readBatch 并发（按实例）读取批量目标：Discover → pathPatterns 过滤 → 逐文件读取。
func (s *ConfigAdoptService) readBatch(resolved *BusinessResolvedTarget, patterns []string) []adoptInstancePlan {
	if resolved == nil || len(resolved.Instances) == 0 {
		return nil
	}
	conc := s.concurrency
	if conc <= 0 {
		conc = adoptBatchConcurrency
	}
	sem := make(chan struct{}, conc)
	plans := make([]adoptInstancePlan, len(resolved.Instances))
	var wg sync.WaitGroup
	for i := range resolved.Instances {
		inst := resolved.Instances[i]
		sem <- struct{}{}
		wg.Add(1)
		go func(idx int, inst model.Instance) {
			defer wg.Done()
			defer func() { <-sem }()
			plans[idx] = s.planInstance(inst, patterns)
		}(i, inst)
	}
	wg.Wait()
	return plans
}

// planInstance 对单个实例做 Discover + 过滤 + 读取（不落库、不写盘）。
func (s *ConfigAdoptService) planInstance(inst model.Instance, patterns []string) adoptInstancePlan {
	plan := adoptInstancePlan{InstanceID: inst.ID, InstanceName: inst.Name}
	discovered, truncated, err := s.source.Discover(inst.ID)
	if err != nil {
		code, msg := classifyAdoptReadError(err)
		plan.Status = AdoptInstanceStatusFailed
		plan.Code = code
		plan.Message = msg
		return plan
	}
	plan.Truncated = truncated
	plan.DiscoveredFiles = len(discovered)
	// 排序让 dryRun 与实跑的逐条结果顺序稳定，便于前端对比。
	sort.Slice(discovered, func(i, j int) bool { return discovered[i].Path < discovered[j].Path })
	for _, f := range discovered {
		if !matchAnyAdoptPattern(patterns, f.Path) {
			continue
		}
		task := s.readTask(inst.ID, f.Path, "")
		task.InstanceName = inst.Name
		plan.Tasks = append(plan.Tasks, task)
		plan.MatchedFiles++
	}
	if plan.MatchedFiles == 0 {
		plan.Status = AdoptInstanceStatusNoMatch
		return plan
	}
	plan.Status = AdoptInstanceStatusMatched
	return plan
}

// ---------------------------------------------------------------------------
// 读阶段
// ---------------------------------------------------------------------------

// readTask 读取单个文件的磁盘现状（规范化路径 → 推断格式 → Read），产出待落库任务。
//
// 这里刻意**不使用** Read 返回的 Fields / Validation / SchemaJSON：
// 那些都是解析产物，纳管要的是原文。原文是逐字节的事实，解析产物是解释。
func (s *ConfigAdoptService) readTask(instanceID uint, filePath, asName string) adoptTask {
	t := adoptTask{InstanceID: instanceID}
	clean, err := normalizeAdoptPath(filePath)
	if err != nil {
		t.Path = strings.TrimSpace(filePath)
		t.ReadErrCode = AdoptCodePathEscape
		t.ReadErrMessage = err.Error()
		return t
	}
	t.Path = clean
	name := strings.TrimSpace(asName)
	if name == "" {
		name = clean
	}
	t.Name = name
	format, ok := adoptFormatOf(clean)
	if !ok {
		t.ReadErrCode = AdoptCodeFormatUnsupported
		t.ReadErrMessage = fmt.Sprintf("扩展名推断不出受支持的配置格式（只支持 yaml / json / properties）：%s", clean)
		return t
	}
	t.Format = format
	res, err := s.source.Read(instanceID, clean)
	if err != nil {
		t.ReadErrCode, t.ReadErrMessage = classifyAdoptReadError(err)
		return t
	}
	if res == nil {
		t.ReadErrCode = AdoptCodeReadFailed
		t.ReadErrMessage = "读取配置返回空结果"
		return t
	}
	// 逐字节保真：res.Content 就是 Worker os.ReadFile 的原文。
	t.Content = res.Content
	t.ContentHash = hashContent(res.Content)
	return t
}

// ---------------------------------------------------------------------------
// 写阶段（落库）
// ---------------------------------------------------------------------------

// applyTask 把读阶段结果落成纳管结果：dryRun 只做计划，否则串行落库。
func (s *ConfigAdoptService) applyTask(t adoptTask, createBaseline, dryRun bool, authorID uint) AdoptFileResult {
	out := AdoptFileResult{
		InstanceID:   t.InstanceID,
		InstanceName: t.InstanceName,
		Path:         t.Path,
		Name:         t.Name,
		Format:       string(t.Format),
		Size:         len(t.Content),
		ContentHash:  t.ContentHash,
		DryRun:       dryRun,
	}
	if t.ReadErrCode != "" {
		out.Status = AdoptStatusFailed
		out.Code = t.ReadErrCode
		out.Message = t.ReadErrMessage
		return out
	}
	// 只读探测：内容能否被解析。失败不阻断纳管（纳管的语义是「记录现状」，
	// 一份语法有问题的配置更需要被记录以便后续修复），只作为提示带出。
	out.Warning = probeAdoptContent(t.Format, t.Content)
	return s.commitTask(out, t, createBaseline, dryRun, authorID)
}

// commitTask 判定冲突并（在非 dryRun 时）落库。
func (s *ConfigAdoptService) commitTask(out AdoptFileResult, t adoptTask, createBaseline, dryRun bool, authorID uint) AdoptFileResult {
	if !dryRun {
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
	}
	fail := func(code, msg string) AdoptFileResult {
		out.Status = AdoptStatusFailed
		out.Code = code
		out.Message = msg
		return out
	}

	existing, err := s.findConfigFileByName(t.Name)
	if err != nil {
		return fail(AdoptCodeWriteFailed, "查询配置文件失败: "+err.Error())
	}
	if existing != nil {
		if existing.DeletedAt.Valid {
			return fail(AdoptCodeConfigFileArchived, "同名配置文件在回收站中，请先从回收站恢复再纳管："+t.Name)
		}
		if existing.Format != t.Format {
			return fail(AdoptCodeFormatConflict,
				fmt.Sprintf("同名配置文件已按 %s 格式创建，格式创建后不可修改；纳管目标为 %s", existing.Format, t.Format))
		}
	}

	// 冲突判定：该实例上已有该配置的实例层版本 → 已纳管，绝不覆盖（spec §6）。
	var current *model.ConfigFile
	if existing != nil {
		current = existing
		has, herr := s.hasInstanceVersion(existing.ID, t.InstanceID)
		if herr != nil {
			return fail(AdoptCodeWriteFailed, "查询已纳管版本失败: "+herr.Error())
		}
		if has {
			out.ConfigFileID = existing.ID
			out.Status = AdoptStatusAlreadyAdopted
			out.Code = AdoptCodeAlreadyAdopted
			out.Action = AdoptActionNone
			out.Message = "该实例上此配置已纳管，跳过（不覆盖）"
			return out
		}
	}

	created := false
	if !dryRun {
		row, wasCreated, cerr := s.ensureConfigFile(t.Name, t.Format, t.InstanceID, authorID)
		if cerr != nil {
			code, msg := adoptWriteErrorCode(cerr)
			return fail(code, msg)
		}
		current = row
		created = wasCreated
	} else if current == nil {
		// dryRun 且尚无该 config_file：结果里给出「将新建」。
		created = true
	}

	if current != nil {
		out.ConfigFileID = current.ID
	}

	if !createBaseline {
		out.Action = AdoptActionNone
		if created {
			out.Status = AdoptStatusCreated
			out.Action = AdoptActionCreateFileOnly
			out.Message = "已登记配置文件；未要求建基线，故未写版本"
			return out
		}
		out.Status = AdoptStatusUnchanged
		out.Message = "配置文件已存在且未要求建基线，无需写入"
		return out
	}

	if dryRun {
		out.Status = AdoptStatusCreated
		out.Action = AdoptActionCreate
		if !created {
			out.Status = AdoptStatusAppended
			out.Action = AdoptActionAppendVersion
		}
		out.VersionNo = 1
		out.Message = "预演：将写入实例级基线版本（v1）"
		return out
	}

	// 建 v1：走 appendLayer 而非 SaveLayer，理由见 appendBaseline 注释。
	ver, aerr := s.appendBaseline(current, t, authorID)
	if aerr != nil {
		code, msg := adoptWriteErrorCode(aerr)
		return fail(code, msg)
	}
	out.VersionID = ver.ID
	out.VersionNo = ver.VersionNo
	// 报告实际入库的 hash，便于调用方核对「库 == 磁盘」。
	out.ContentHash = ver.ContentHash
	if created {
		out.Status = AdoptStatusCreated
		out.Action = AdoptActionCreate
		out.Code = AdoptCodeAdopted
	} else {
		out.Status = AdoptStatusAppended
		out.Action = AdoptActionAppendVersion
		out.Code = AdoptCodeAdopted
	}
	return out
}

// appendBaseline 在实例作用域写入纳管基线版本。
//
// **为什么不走 SaveLayer**：SaveLayer 会先对原文做语法与 schema 校验，一份语法有问题的
// YAML 会被拒绝入库。但纳管的语义是「记录磁盘现状」——坏掉的配置恰恰最需要被记录，
// 否则运维永远无法用 JM 把它修好。因此这里只校验作用域引用是否存在（避免「配了却没人收到」），
// 内容原样入库。
//
// 注意 appendLayer 内部用 hashContent(content) 计算 content_hash，与磁盘 sha256 完全一致。
func (s *ConfigAdoptService) appendBaseline(file *model.ConfigFile, t adoptTask, authorID uint) (*model.ConfigLayerVersion, error) {
	if file == nil {
		return nil, fmt.Errorf("配置文件记录为空")
	}
	if err := s.files.ensureScopeExists(model.ConfigScopeInstance, t.InstanceID); err != nil {
		return nil, err
	}
	at := time.Now()
	if s.now != nil {
		at = s.now()
	}
	remark := adoptBaselineRemark(t, at)
	return s.files.appendLayer(file, model.ConfigScopeInstance, t.InstanceID, t.Content, remark, nil, authorID)
}

// adoptBaselineRemark 生成基线版本备注（spec §10 待定项 3：标注纳管来源）。
// remark 列是 varchar(255)，用包内既有 truncateRunes 按字符边界截断。
func adoptBaselineRemark(t adoptTask, at time.Time) string {
	src := fmt.Sprintf("纳管自磁盘 %s", t.Path)
	if t.InstanceName != "" {
		src = fmt.Sprintf("纳管自磁盘 %s/%s", t.InstanceName, t.Path)
	}
	return truncateRunes(src+" @"+at.Format(time.RFC3339), 255)
}

// ---------------------------------------------------------------------------
// 配置文件辅助
// ---------------------------------------------------------------------------

// findConfigFileByName 按名字查配置文件；不存在返回 (nil, nil)。
//
// 用 Unscoped：name 的唯一索引把软删行也算占用（见 model.ConfigFile 注释），
// 只查未软删会漏掉「同名但已删」的冲突。
func (s *ConfigAdoptService) findConfigFileByName(name string) (*model.ConfigFile, error) {
	var row model.ConfigFile
	err := s.db.Unscoped().Where("name = ?", name).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// ensureConfigFile 取或建 config_file，返回 (记录, 是否新建, 错误)。
func (s *ConfigAdoptService) ensureConfigFile(name string, format model.ConfigFileFormat, instanceID, authorID uint) (*model.ConfigFile, bool, error) {
	existing, err := s.findConfigFileByName(name)
	if err != nil {
		return nil, false, err
	}
	if existing != nil {
		if existing.DeletedAt.Valid {
			return nil, false, fmt.Errorf("%w: %s", ErrAdoptConfigFileArchived, name)
		}
		if existing.Format != format {
			return nil, false, fmt.Errorf("%w: 已有格式 %s，本次 %s", ErrAdoptFormatConflict, existing.Format, format)
		}
		return existing, false, nil
	}
	row, cerr := s.files.Create(ConfigFileInput{
		Name:        name,
		Format:      string(format),
		Description: fmt.Sprintf("纳管自实例 #%d 的磁盘配置", instanceID),
	}, authorID)
	if cerr != nil {
		// 并发下同名可能刚被别人建好：重查一次复用，本次不再新建。
		if errors.Is(cerr, ErrConfigFileExists) {
			again, rerr := s.findConfigFileByName(name)
			if rerr == nil && again != nil && !again.DeletedAt.Valid && again.Format == format {
				return again, false, nil
			}
		}
		return nil, false, cerr
	}
	return row, true, nil
}

// hasInstanceVersion 判断该配置在某个实例作用域上是否已有版本。
//
// 以「作用域 + 版本存在」而非「config_file 存在」判定已纳管：
//
//	同名 config_file 可被多个实例共享，每个实例各有独立的实例层版本链
//	（spec §5：同名文件在不同实例内容不同时各自独立存 v1）。
//	若按 config_file 是否存在判定，第二个实例就永远纳管不进去了。
func (s *ConfigAdoptService) hasInstanceVersion(configFileID, instanceID uint) (bool, error) {
	var count int64
	err := s.db.Model(&model.ConfigLayerVersion{}).
		Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?",
			configFileID, model.ConfigScopeInstance, instanceID).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ---------------------------------------------------------------------------
// 健康自检（spec §11 保留项）
// ---------------------------------------------------------------------------

// Health 返回配置纳管的健康自检视图。
//
// 只统计 JM 自己的事实并给固定文案提示，不做任何基于 Beacon 状态的自动告警：
// 已核实 beacon_push.go 只推送实例注册（/beacon/v1/agent/register），全无配置内容推送，
// 因此按「Beacon 同步开着」判定双写风险会把正常拓扑同步误报成配置冲突。
func (s *ConfigAdoptService) Health() (*ConfigHealthView, error) {
	view := &ConfigHealthView{Notice: adoptHealthNotice}
	if s.db == nil {
		return view, nil
	}
	if err := s.db.Model(&model.ConfigFile{}).Count(&view.ConfigFileCount).Error; err != nil {
		return nil, fmt.Errorf("统计配置文件数量失败: %w", err)
	}
	if err := s.db.Model(&model.ConfigLayerVersion{}).Count(&view.LayerVersionCount).Error; err != nil {
		return nil, fmt.Errorf("统计层版本数量失败: %w", err)
	}
	// 最近一次配置写入时间：层版本只追加不修改，取最大 created_at 即可。
	var latest []model.ConfigLayerVersion
	if err := s.db.Model(&model.ConfigLayerVersion{}).
		Order("created_at desc").Limit(1).Find(&latest).Error; err != nil {
		return nil, fmt.Errorf("查询最近配置写入时间失败: %w", err)
	}
	if len(latest) > 0 && !latest[0].CreatedAt.IsZero() {
		view.LastWriteAt = latest[0].CreatedAt.UTC().Format(time.RFC3339)
	}
	return view, nil
}

// ---------------------------------------------------------------------------
// 纯函数辅助（便于单测）
// ---------------------------------------------------------------------------

// adoptFormatOf 按扩展名推断 config_file 支持的格式。
//
// 与 Discover 的 configFormatOf 的差异：后者会返回 toml/txt 等"格式名"供展示，
// 而 config_file 只支持 yaml / json / properties（model.ValidConfigFileFormat）。
// 因此 .toml / .txt / .conf / 无扩展名都返回 false，由调用方报 FORMAT_UNSUPPORTED。
func adoptFormatOf(p string) (model.ConfigFileFormat, bool) {
	switch strings.ToLower(path.Ext(strings.ReplaceAll(p, "\\", "/"))) {
	case ".properties":
		return model.ConfigFormatProperties, true
	case ".yml", ".yaml":
		return model.ConfigFormatYAML, true
	case ".json":
		return model.ConfigFormatJSON, true
	default:
		return "", false
	}
}

// normalizeAdoptPath 规范化用户给出的相对路径并拦下越界写法。
//
// 双保险：CP 侧先拦（给出清晰的 PATH_ESCAPE），Worker 侧 validateNonRootPath 再兜底。
func normalizeAdoptPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", ErrAdoptPathEmpty
	}
	// 统一分隔符：Windows 风格的 \ 也接受，落库与匹配一律用 /。
	p = strings.ReplaceAll(p, "\\", "/")
	if strings.HasPrefix(p, "/") {
		return "", ErrAdoptPathEscape
	}
	// Windows 盘符（C:/x）同样是绝对路径。
	if len(p) > 1 && p[1] == ':' {
		return "", ErrAdoptPathEscape
	}
	segs := strings.Split(p, "/")
	clean := make([]string, 0, len(segs))
	for _, seg := range segs {
		switch seg {
		case "", ".":
			continue
		case "..":
			// 穿越段一律拒绝，不做「规范化后仍在目录内」的宽容判定：
			// 纳管入参由人工给出，出现 .. 本身就说明调用方预期与事实不符。
			return "", ErrAdoptPathEscape
		}
		clean = append(clean, seg)
	}
	if len(clean) == 0 {
		return "", ErrAdoptPathEmpty
	}
	return strings.Join(clean, "/"), nil
}

// normalizeAdoptPatterns 清洗通配模式（去空、统一分隔符、去重）。
func normalizeAdoptPatterns(patterns []string) []string {
	out := make([]string, 0, len(patterns))
	seen := make(map[string]struct{}, len(patterns))
	for _, p := range patterns {
		p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
		// 去掉前导 "/" 与 "./"，让 `./plugins/*.yml` 这类写法也能用。
		p = strings.TrimPrefix(p, "./")
		p = strings.TrimPrefix(p, "/")
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// matchAnyAdoptPattern 判断路径是否命中任一模式。
func matchAnyAdoptPattern(patterns []string, p string) bool {
	for _, pat := range patterns {
		if matchConfigPath(pat, p) {
			return true
		}
	}
	return false
}

// matchConfigPath 判断相对路径是否命中通配模式（spec §10 待定项 1 的落地选择）。
//
// 语法：
//   - 逐段 glob，段内支持 * ? [...]（复用 path.Match）；
//   - 单独一段 `**` 表示「零个或多个路径段」，可跨层（`plugins/**/config.yml`）；
//   - 大小写不敏感：MC 插件配置文件名大小写混用极常见（config.yml / Config.YML），
//     纳管目标由人工给出，宁可宽松也不漏；
//   - 非法模式（括号不闭合等）不匹配任何路径——不静默退化成"匹配全部"。
func matchConfigPath(pattern, p string) bool {
	pat := strings.Split(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(pattern), "\\", "/")), "/")
	target := strings.Split(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")), "/")
	return matchAdoptSegments(pat, target)
}

// matchAdoptSegments 逐段递归匹配（`**` 分支穷举吞掉的段数）。
func matchAdoptSegments(pat, seg []string) bool {
	if len(pat) == 0 {
		return len(seg) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(seg); i++ {
			if matchAdoptSegments(pat[1:], seg[i:]) {
				return true
			}
		}
		return false
	}
	if len(seg) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], seg[0])
	if err != nil || !ok {
		return false
	}
	return matchAdoptSegments(pat[1:], seg[1:])
}

// probeAdoptContent 只读探测内容能否被解析为结构化配置。
//
// 只用于给前端提示数据质量问题：解析结果**不写回、不参与入库**，
// 因此不会破坏「逐字节保真」（ParseConfigContent 不修改入参，也不产生新的存储内容）。
func probeAdoptContent(format model.ConfigFileFormat, content string) string {
	if _, err := ParseConfigContent(format, content); err != nil {
		return "内容解析失败（已按原文纳管，修复前合并预览会报错）: " + err.Error()
	}
	return ""
}

// classifyAdoptReadError 把读磁盘失败归类为契约错误码（spec §6）。
//
// 判定顺序很重要：节点未连接必须先于文本匹配，否则 Worker 不可达时的连接错误文本
// 可能被误判成 READ_FAILED，运维会去查文件权限而实际该查网络。
//
// 「离线」口径刻意是**节点未连接**而非实例状态非 RUNNING：纳管读的是磁盘文件，
// 实例停止时文件依然在，恰恰维护窗口才是最需要纳管的时机。
func classifyAdoptReadError(err error) (code, message string) {
	if err == nil {
		return "", ""
	}
	if errors.Is(err, ErrNodeNotConnected) {
		return AdoptCodeInstanceOffline, "节点未连接，无法读取磁盘上的配置文件"
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return AdoptCodeInstanceNotFound, "实例不存在"
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "no such file"),
		strings.Contains(lower, "does not exist"),
		strings.Contains(lower, "not exist"),
		strings.Contains(msg, "文件不存在"):
		return AdoptCodeFileNotFound, msg
	case strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "access is denied"),
		strings.Contains(msg, "权限"),
		strings.Contains(msg, "仅允许操作配置文件"):
		return AdoptCodeReadFailed, msg
	}
	return AdoptCodeReadFailed, msg
}

// ErrAdoptConfigFileArchived 同名配置文件在回收站中。
var ErrAdoptConfigFileArchived = errors.New("同名配置文件在回收站中")

// ErrAdoptFormatConflict 同名配置文件已按别的格式创建。
var ErrAdoptFormatConflict = errors.New("配置文件格式冲突")

// adoptWriteErrorCode 把落库阶段错误映射为纳管错误码。
func adoptWriteErrorCode(err error) (code, message string) {
	if err == nil {
		return "", ""
	}
	switch {
	case errors.Is(err, ErrAdoptConfigFileArchived):
		return AdoptCodeConfigFileArchived, err.Error()
	case errors.Is(err, ErrAdoptFormatConflict):
		return AdoptCodeFormatConflict, err.Error()
	case errors.Is(err, ErrConfigFormatImmutable), errors.Is(err, ErrConfigNameImmutable):
		return AdoptCodeFormatConflict, err.Error()
	case errors.Is(err, ErrConfigScopeRefNotFound), errors.Is(err, ErrConfigInstanceNotInScope):
		return AdoptCodeInstanceNotFound, err.Error()
	case errors.Is(err, ErrConfigFileExists):
		return AdoptCodeWriteFailed, err.Error()
	}
	return AdoptCodeWriteFailed, err.Error()
}

// summarizeAdopt 汇总逐文件结果。
func summarizeAdopt(results []AdoptFileResult, instances, truncatedInstances int) AdoptSummary {
	sum := AdoptSummary{Total: len(results), Instances: instances, TruncatedInstances: truncatedInstances}
	for _, r := range results {
		switch r.Status {
		case AdoptStatusCreated:
			sum.Created++
		case AdoptStatusAppended:
			sum.Appended++
		case AdoptStatusUnchanged:
			sum.Unchanged++
		case AdoptStatusAlreadyAdopted:
			sum.AlreadyAdopted++
		case AdoptStatusFailed:
			sum.Failed++
		}
	}
	return sum
}

// flattenAdoptResults 把逐实例结果摊平成逐文件结果（汇总用）。
func flattenAdoptResults(instances []AdoptInstanceResult) []AdoptFileResult {
	out := make([]AdoptFileResult, 0, len(instances))
	for _, inst := range instances {
		out = append(out, inst.Results...)
	}
	return out
}
