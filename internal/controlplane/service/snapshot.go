package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
)

// 实例整机快照与一键回滚（FR-466）。
//
// 现状痛点：平台已有四套各自独立的「回滚」（备份链 / 文件版本 / 配置版本 / 自更新二进制），
// 但没有「整机时间点」概念，运维要人工组合「停服 → 找备份 → 恢复 → 判断是否重建二进制」。
// 本服务补的是「快照」这一层统一入口，并强制「回滚前先留底」。
//
// 设计决策（spec §2.2）：快照**一律全量**，不吃增量链——快照是「可独立回滚的时间点」，
// 若挂增量链，链上任一环被删除/损坏都会让快照失效，违背「一键回滚」的可靠性预期。
// 存储代价由保留策略兜底（§2.4）。

// SnapshotService 错误。
var (
	// ErrSnapshotNotFound 快照不存在。
	ErrSnapshotNotFound = errors.New("快照不存在")
	// ErrSnapshotNotRollable 快照状态不可作为回滚目标（未完成/失败）。
	ErrSnapshotNotRollable = errors.New("快照未完成，无法作为回滚目标")
	// ErrSnapshotInstanceRunning 实例运行中且无法自动停服（节点离线/未连接）。
	ErrSnapshotInstanceRunning = errors.New("实例运行中，且无法自动停服")
	// ErrSnapshotOperationInFlight 同实例已有在途的破坏性操作（快照创建/回滚或版本变更）。
	ErrSnapshotOperationInFlight = errors.New("同实例已有在途操作")
	// ErrSnapshotQuotaExceeded 该实例的快照数量/总占用已达上限（m-2 创建前保护）。
	ErrSnapshotQuotaExceeded = errors.New("快照数量或总占用已达上限")
)

// snapshotStateLabel 状态的中文标签（任务阶段/错误文案）。
func snapshotStateLabel(s model.SnapshotState) string {
	switch s {
	case model.SnapshotStatePending:
		return "待执行"
	case model.SnapshotStateRunning:
		return "执行中"
	case model.SnapshotStateCompleted:
		return "已完成"
	case model.SnapshotStateFailed:
		return "失败"
	case model.SnapshotStateRolledBack:
		return "已回滚"
	}
	return string(s)
}

// SnapshotService 实例整机快照服务（FR-466）。
type SnapshotService struct {
	db *gorm.DB
	// backups 归档/回放实现（复用 FR-013/056/057 的备份体系，不重建一套）。
	backups *BackupService
	// instances 停服编排（回滚前必须停服；复用既有优雅停止路径）。
	instances *InstanceService
	tasks     *TaskService
	settings  SettingsReader

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	// audit 审计（instance.snapshot_* 动作）；nil 时不写审计。
	audit *AuditService

	// inFlight 本进程**已发起且尚未收敛到终态**的快照 ID（R3）。
	//
	// 它把「启动清扫只可能扫到上个进程留下的行」从 `main.go` 的语句顺序约定
	// 提升为代码内约束：`sweepState` 只处理不在本集合内的 running/pending 行，
	// 于是即便将来有人把 `Start()` 挪到监听器之后、或新增第二个 Start 调用点，
	// 也不会把本进程正在归档的快照误判为孤儿。
	inFlightMu sync.Mutex
	inFlight   map[uint]struct{}
}

// NewSnapshotService 创建快照服务。
func NewSnapshotService(db *gorm.DB, backups *BackupService, instances *InstanceService) *SnapshotService {
	return &SnapshotService{
		db: db, backups: backups, instances: instances,
		stopCh:   make(chan struct{}),
		inFlight: make(map[uint]struct{}),
	}
}

// markSnapshotInFlight 登记「本进程正在推进该快照」（R3）。
func (s *SnapshotService) markSnapshotInFlight(snapshotID uint) {
	if s == nil || snapshotID == 0 {
		return
	}
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if s.inFlight == nil {
		s.inFlight = make(map[uint]struct{})
	}
	s.inFlight[snapshotID] = struct{}{}
}

// clearSnapshotInFlight 注销在途登记（终态或执行体返回后）。
func (s *SnapshotService) clearSnapshotInFlight(snapshotID uint) {
	if s == nil {
		return
	}
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	delete(s.inFlight, snapshotID)
}

// snapshotInFlight 报告该快照是否由本进程在推进。
func (s *SnapshotService) snapshotInFlight(snapshotID uint) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	_, ok := s.inFlight[snapshotID]
	return ok
}

// SetTaskService 注入任务中心（快照创建/回滚走异步任务 + 阶段进度）。
func (s *SnapshotService) SetTaskService(t *TaskService) { s.tasks = t }

// SetSettingsReader 注入设置读取器（snapshot.retention_* / pre_rollback_keep）。
func (s *SnapshotService) SetSettingsReader(r SettingsReader) { s.settings = r }

// SnapshotCreateOptions 创建快照的可选参数（与 BackupService.CreateOptions 区分命名，
// 二者语义不同：后者控增量/存储位置，本类型控快照类型与执行方式）。
type SnapshotCreateOptions struct {
	// Kind 快照类型（缺省 manual）。
	Kind model.SnapshotKind
	// TriggeredBy 操作人。
	TriggeredBy uint
	// TriggeredByRollbackID 由哪次回滚触发（仅 pre_rollback 用）。
	TriggeredByRollbackID uint
	// Synchronous 为 true 时在调用方 goroutine 内跑完（回滚流程内的 pre_rollback 快照用，
	// 因为它必须在回滚继续之前完成）。为 false 时经任务中心异步执行。
	Synchronous bool
	// Stage 同步模式下的阶段上报回调（可为 nil）。
	Stage func(int, string)
}

// Create 创建实例整机快照（FR-466 §2.2）。
//
// 同步段：校验实例存在 → 登记 InstanceSnapshot → 登记/启动任务；
// 后台段：登记全量 Backup → 打包工作目录 → 回填 RootBackupID → 采集二进制指纹。
//
// 快照在实例运行中**允许创建但标注**（spec §5）：非停服状态下 MC 世界文件可能非一致，
// 这种一致性风险必须让运维看见（Note），而不是静默产出一个不可信的快照。
func (s *SnapshotService) Create(instanceID uint, name string, opts SnapshotCreateOptions) (*model.InstanceSnapshot, error) {
	inst, err := s.loadInstance(instanceID)
	if err != nil {
		return nil, err
	}
	// M-4：快照创建与回滚/版本变更共用工作目录，同样拒绝在途的破坏性操作并存。
	// pre_rollback 是回滚流程内部发起的同步创建（此时不应有其它在途任务），故不受本检查阻断。
	if kind0 := opts.Kind; kind0 != model.SnapshotKindPreRollback {
		if err := s.ensureNoInflightOperation(instanceID); err != nil {
			return nil, err
		}
	}
	// m-2：创建前做上限/配额保护，避免反复创建把节点磁盘吃满。
	if err := s.ensureSnapshotCapacity(inst); err != nil {
		return nil, err
	}
	kind := opts.Kind
	if kind == "" {
		kind = model.SnapshotKindManual
	}
	snapName := strings.TrimSpace(name)
	if snapName == "" {
		snapName = fmt.Sprintf("快照 %s", time.Now().Format("2006-01-02 15:04:05"))
	}
	snap := &model.InstanceSnapshot{
		InstanceID:            instanceID,
		Name:                  snapName,
		Kind:                  kind,
		State:                 model.SnapshotStatePending,
		TriggeredBy:           opts.TriggeredBy,
		TriggeredByRollbackID: opts.TriggeredByRollbackID,
		Note:                  snapshotConsistencyNote(inst),
		ConfigHash:            instanceConfigHash(inst),
		ConfigSummary:         instanceConfigSummary(inst),
	}
	if err := s.db.Create(snap).Error; err != nil {
		return nil, fmt.Errorf("登记快照失败: %w", err)
	}
	// 审计下沉到 service 层（edge m-3）：动作登记在 HTTP handler 时，经 MCP 工具
	// `instance_snapshot_create` 创建的快照**不产生**该审计，而 API.md 对「创建整机快照」
	// 声明了「审计: instance.snapshot_create」——声明在 MCP 路径下不成立。
	// 与 rollback 同口径（`instance.snapshot_rollback` 也写在 service 层，两条入口都覆盖）。
	// HTTP 侧独有的 ClientIP 由 handler 层不再重复记录（审计已有 instance 维度与操作人）。
	s.recordSnapshotCreateAudit(opts.TriggeredBy, inst, snap)

	// R3：任务中心路径同样登记在途，执行体返回即注销（终态已由 runCreate 落库）。
	work := func(ctx context.Context, stage func(int, string)) (string, error) {
		defer s.clearSnapshotInFlight(snap.ID)
		if err := s.runCreate(inst, snap, stage); err != nil {
			return "", err
		}
		return "", nil
	}

	if opts.Synchronous {
		s.markSnapshotInFlight(snap.ID)
		defer s.clearSnapshotInFlight(snap.ID)
		if err := s.runCreate(inst, snap, opts.Stage); err != nil {
			s.markSnapshotFailed(snap, err)
			return nil, err
		}
		// 回读落库后的终态：调用方（回滚编排/HTTP 响应）需要 completed 状态与回填的
		// RootBackupID、二进制指纹，而不是登记时的内存快照。
		if reloaded, rerr := s.GetByID(snap.ID); rerr == nil {
			return reloaded, nil
		}
		return snap, nil
	}
	if s.tasks == nil {
		// 无任务中心（测试/未接线）：回退后台 goroutine，保持行为可用。
		s.markSnapshotInFlight(snap.ID)
		go func() {
			defer s.clearSnapshotInFlight(snap.ID)
			if err := s.runCreate(inst, snap, nil); err != nil {
				s.markSnapshotFailed(snap, err)
				slog.Error("后台创建快照失败", "snapshotId", snap.ID, "error", err)
			}
		}()
		return snap, nil
	}
	// 任务中心路径：执行体由任务运行时驱动，work 内部自行注销在途登记。
	s.markSnapshotInFlight(snap.ID)
	taskID := s.tasks.RunAsync(RunSpec{
		NodeID: inst.NodeID, InstanceID: instanceID, Kind: model.TaskKindSnapshotCreate,
		Title: fmt.Sprintf("创建快照 %s", snap.Name), CreatedBy: opts.TriggeredBy,
	}, work)
	if taskID == "" {
		s.clearSnapshotInFlight(snap.ID)
		s.markSnapshotFailed(snap, errors.New("登记快照任务失败"))
		return nil, errors.New("登记快照任务失败")
	}
	return snap, nil
}

// runCreate 执行快照的归档段：登记全量 Backup → 打包 → 回填 RootBackupID → 采集二进制指纹。
func (s *SnapshotService) runCreate(inst *model.Instance, snap *model.InstanceSnapshot, stage func(int, string)) error {
	report := func(p int, text string) {
		if stage != nil {
			stage(p, text)
		}
	}
	_ = s.db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).
		Update("state", model.SnapshotStateRunning).Error
	report(10, "登记全量备份…")

	backup, err := s.backups.RegisterFull(inst.ID, "snapshot-"+snap.UUID)
	if err != nil {
		return err
	}
	report(25, "打包工作目录（全量）…")
	if err := s.backups.ExecuteBackup(backup, stage); err != nil {
		return err
	}

	var completed model.Backup
	if err := s.db.First(&completed, backup.ID).Error; err == nil {
		_ = s.db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).
			Updates(map[string]any{"root_backup_id": completed.ID, "size_mb": completed.FileSizeMB}).Error
		snap.RootBackupID = completed.ID
		snap.SizeMB = completed.FileSizeMB
	}

	report(80, "采集二进制指纹…")
	name, sha := s.binaryFingerprint(inst)
	report(95, "完成…")
	return s.db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).Updates(map[string]any{
		"state":          model.SnapshotStateCompleted,
		"binary_name":    name,
		"binary_sha256":  sha,
		"failure_reason": "",
	}).Error
}

// markSnapshotFailed 标记快照失败并记录原因。
func (s *SnapshotService) markSnapshotFailed(snap *model.InstanceSnapshot, err error) {
	reason := err.Error()
	if len(reason) > 512 {
		reason = reason[:512]
	}
	_ = s.db.Model(&model.InstanceSnapshot{}).Where("id = ?", snap.ID).
		Updates(map[string]any{"state": model.SnapshotStateFailed, "failure_reason": reason}).Error
}

// sweepStartupOrphans 启动时一次性清扫「归档中」的孤儿快照（m6）。
//
// 场景：`runCreate` 先把状态置 running 再归档，归档过程在**进程内存**里推进。
// CP 若在归档中重启（发布、崩溃、被 OOM 杀），该任务的 goroutine 随进程消失，
// 而库里的行永远停在 running：既不会自行收敛到终态，也不受保留策略的条数维管辖
// （`kind <> pre_rollback` 只豁免条数；pre_rollback 更是被天数维特意保住）——
// 于是前端永久显示「归档中」，只能人工改库或删除，`snapshot.max_per_instance`
// 还会把这类幽灵行一直算进配额。
//
// 语义：启动瞬间仍在 running 的快照**必然**已失去推进它的执行者（归档是本进程内的
// goroutine + 同步 RPC，不存在跨进程续跑），故一律标记 failed 并写明原因——复用
// `markSnapshotFailed` 的终态语义，不新增状态、不猜测「是否真的还在跑」。
//
// **不依赖 `main.go` 的语句顺序**（R3）：本函数跳过 `inFlight` 集合内的行，即
// 「本进程已在推进的快照绝不被判为孤儿」。这条约束原先只由「`Start()` 早于所有监听器」
// 这一 main 里的语句顺序保证，任何调整（把 Start 挪到 r.Run 之后、新增热重启调用点、
// 复用为多进程形态）都会让它静默变成破坏性操作：把正在归档的快照判为 failed，
// 而底层 Backup 可能已在落盘，留下假失败 + 认不出的孤儿归档。
//
// 选择「标记失败」而非「删除」：快照记录是回滚留痕的一部分，行本身不该被静默抹掉；
// 且其底层备份若已落盘（归档在**末段**中断时可能已 RegisterFull 成功），
// 留行能让运维看见并手工处置，而不是留下一份谁也认不出的孤儿归档。
func (s *SnapshotService) sweepStartupOrphans() int {
	const orphanReason = "控制面重启导致归档中断（启动清扫标记失败；如需快照请重新创建）"
	return s.sweepState(model.SnapshotStateRunning, orphanReason, "启动清扫：查询残留 running 快照失败",
		"启动清扫：把残留的归档中快照标记为失败")
}

// sweepStartupPendingOrphans 启动时把**残留 pending** 快照标记失败。
//
// 与 running 同源：`Create` 先落 pending 再登记任务；登记任务阶段若进程退出，
// 行会停在 pending（running 都还没写）。pending 同样没有执行者会再来推进它。
// pending 与 running 分开扫，是为了让日志与计数能区分中断发生在哪一阶段。
func (s *SnapshotService) sweepStartupPendingOrphans() int {
	const orphanReason = "控制面重启时归档尚未开始（启动清扫标记失败；如需快照请重新创建）"
	return s.sweepState(model.SnapshotStatePending, orphanReason, "启动清扫：查询残留 pending 快照失败",
		"启动清扫：把残留的待归档快照标记为失败")
}

// sweepState 把某状态下**不由本进程推进**的残留快照标记为失败，返回标记条数（R19）。
//
// 两个清扫阶段的查询、空判断、逐条标记与计数逐行相同，仅状态常量与文案不同，
// 故合并为一处实现（避免两份实现各自漂移）。日志文案由调用方分别给出，保留
// 「区分中断发生在哪一阶段」的原始意图；queryFailMsg 与 sweepMsg 分别用于查询失败
// 与清扫完成的日志，使两种阶段的告警可区分。
func (s *SnapshotService) sweepState(state model.SnapshotState, orphanReason, queryFailMsg, sweepMsg string) int {
	var orphans []model.InstanceSnapshot
	if err := s.db.Where("state = ?", state).Find(&orphans).Error; err != nil {
		slog.Error(queryFailMsg, "error", err)
		return 0
	}
	if len(orphans) == 0 {
		return 0
	}
	marked := 0
	for i := range orphans {
		// 本进程正在推进的快照不是孤儿（R3）：只跳过，不改状态。
		if s.snapshotInFlight(orphans[i].ID) {
			continue
		}
		// 逐条走 markSnapshotFailed 所需的入参形态，保持失败原因与运行期一致（统一封顶 512）。
		s.markSnapshotFailed(&orphans[i], errors.New(orphanReason))
		marked++
	}
	if marked == 0 {
		return 0
	}
	slog.Warn(sweepMsg, "count", marked, "reason", orphanReason)
	return marked
}

// binaryFingerprint 从启动命令首 token 解析启动二进制名与摘要（FR-466 §2.2 步骤 3，含 FR-468 协同）。
//
// 摘要来源：FR-468 的版本绑定（若已登记）优先——那是经制品库校验过的权威摘要；
// 无绑定（url/node_file 来源或非 binary 实例）时只给文件名，摘要留空（不猜测）。
func (s *SnapshotService) binaryFingerprint(inst *model.Instance) (string, string) {
	cmd := strings.TrimSpace(inst.StartCommand)
	if cmd == "" {
		return "", ""
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return "", ""
	}
	first := fields[0]
	// 只认相对工作目录的执行形式（./xxx）与裸文件名；带路径的绝对命令不做猜测。
	name := strings.TrimPrefix(first, "./")
	if strings.ContainsAny(name, "/\\") || name == "" {
		return "", ""
	}
	var binding model.InstanceBinaryBinding
	if err := s.db.Where("instance_id = ?", inst.ID).First(&binding).Error; err == nil {
		if binding.CurrentFilename == name && binding.CurrentSHA256 != "" {
			return name, binding.CurrentSHA256
		}
	}
	return name, ""
}

// loadInstance 载入实例（快照所需的全部字段）。
func (s *SnapshotService) loadInstance(instanceID uint) (*model.Instance, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, err
	}
	return &inst, nil
}

// snapshotConsistencyNote 生成一致性提示（运行态创建快照时明确标注风险，spec §5）。
func snapshotConsistencyNote(inst *model.Instance) string {
	switch inst.Status {
	case model.InstanceStatusRunning, model.InstanceStatusStarting, model.InstanceStatusStopping:
		return "创建时实例未停止：MC 世界文件可能非一致，回滚后请核对存档"
	}
	return ""
}

// instanceConfigHash 计算启动命令 + 环境变量的摘要（回滚差异提示用）。
func instanceConfigHash(inst *model.Instance) string {
	sum := sha256.Sum256([]byte(inst.StartCommand + "\x00" + inst.EnvVars))
	return hex.EncodeToString(sum[:])
}

// instanceConfigSummary 生成配置摘要的人可读形式（截断到列长）。
func instanceConfigSummary(inst *model.Instance) string {
	summary := fmt.Sprintf("start=%s", strings.TrimSpace(inst.StartCommand))
	if len(summary) > 512 {
		summary = summary[:512]
	}
	return summary
}

// ListByInstance 返回实例的快照列表（创建时间倒序）。实例不存在返回 ErrInstanceNotFound。
//
// 列表**必须**带上底链可用性（B-1）：快照是否真的能回滚取决于 RootBackupID 指向的
// Backup 行是否还在，仅看 state 会把死链显示成「可回滚」，让用户点了才失败。
func (s *SnapshotService) ListByInstance(instanceID uint) ([]model.InstanceSnapshot, error) {
	if _, err := s.loadInstance(instanceID); err != nil {
		return nil, err
	}
	var out []model.InstanceSnapshot
	err := s.db.Where("instance_id = ?", instanceID).
		Order("created_at DESC, id DESC").Find(&out).Error
	if err != nil {
		return nil, err
	}
	s.annotateBacking(out)
	return out, nil
}

// GetByID 取快照（不做底链校验；需要判断可回滚性时用 GetRollableByID）。
func (s *SnapshotService) GetByID(snapshotID uint) (*model.InstanceSnapshot, error) {
	var snap model.InstanceSnapshot
	if err := s.db.First(&snap, snapshotID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSnapshotNotFound
		}
		return nil, err
	}
	return &snap, nil
}

// RollbackResult 回滚结果。
type RollbackResult struct {
	TaskID string `json:"taskId"`
	// SnapshotID 回滚目标快照。
	SnapshotID uint `json:"snapshotId"`
	// PreRollbackSnapshotID 强制创建的「回滚前」快照（可据此退回回滚前状态）。
	PreRollbackSnapshotID uint `json:"preRollbackSnapshotId"`
	// InstanceID 目标实例。
	InstanceID uint `json:"instanceId"`
	// Stopped 本次是否因运行态先做了停服编排。
	Stopped bool `json:"stopped"`
	// BinaryMismatch 目标快照的二进制指纹与当前不一致（**仅提示，不越权替换可执行文件**）。
	BinaryMismatch bool   `json:"binaryMismatch"`
	BinaryNote     string `json:"binaryNote,omitempty"`
	// FinalStatus 回滚后的实例状态。恒为 STOPPED（不自动拉起，留人工确认）。
	FinalStatus string `json:"finalStatus"`
}

// Rollback 一键回滚到指定快照（FR-466 §2.3 核心闭环）。
//
// 顺序**强制**为：
//  1. 校验目标快照可回滚 + 底链存在（B-1：底链缺失显式降级，不带病入队）+ 实例存在；
//  2. 控制面互斥：同实例在途 snapshot_*/binary_upgrade 任务则拒绝（M-4）；
//  3. 【强制】创建 pre_rollback 快照（失败即中止，不留无退路的操作）；
//  4. 运行态处置：RUNNING/STARTING/STOPPING → 先优雅停止（非拒绝；与 FR-013 Restore 不同，
//     此处由快照负责停服编排）；
//  5. 回放目标快照的 RootBackupID（委托 BackupService.ExecuteRestore，复用 RestoreBackup RPC）；
//  6. 二进制不一致 → 提示「数据已回滚，二进制未动」（二进制回滚走 FR-468，不自动执行）；
//  7. 恢复实例状态：恒回 STOPPED（不自动拉起）；终态站内信 + 审计 instance.snapshot_rollback。
//
// 并发与审计（M-4/M-6）：任务执行体内部再取一次实例互斥锁，且**回放前重验实例状态**——
// 入队前的停服检查是 TOCTOU 的，入队后实例可能被其它路径拉起。审计写在**终态**
// （任务体内成功/失败各写一次），而不是 RunAsync 返回后立刻记成功。
func (s *SnapshotService) Rollback(ctx context.Context, snapshotID, operatorID uint) (*RollbackResult, error) {
	target, err := s.GetRollableByID(snapshotID)
	if err != nil {
		return nil, err
	}
	inst, err := s.loadInstance(target.InstanceID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureNoInflightOperation(inst.ID); err != nil {
		return nil, err
	}

	// 【强制】回滚前留底：失败即中止（不进入会覆盖当前状态的后续步骤）。
	pre, err := s.Create(inst.ID, fmt.Sprintf("回滚前自动快照 %s", time.Now().Format("2006-01-02 15:04:05")), SnapshotCreateOptions{
		Kind:                  model.SnapshotKindPreRollback,
		TriggeredBy:           operatorID,
		TriggeredByRollbackID: snapshotID,
		Synchronous:           true,
	})
	if err != nil {
		return nil, fmt.Errorf("创建回滚前快照失败，已中止回滚（不留无退路的操作）: %w", err)
	}
	if s.audit != nil {
		s.audit.RecordResultSafe(operatorID, "instance.snapshot_rollback_pre", "instance",
			strconv.FormatUint(uint64(inst.ID), 10), "pre_rollback snapshot#"+strconv.FormatUint(uint64(pre.ID), 10), "", true, "")
	}

	stopped, err := s.stopForRollback(ctx, inst)
	if err != nil {
		// 停服未收敛：回退实例状态并清理本次 pre_rollback（M-5），避免
		// ①实例悬挂在 STOPPING 且无人知晓 ②每次失败多留一条不受条数裁剪的全量快照。
		s.cleanupAbortedRollback(inst.ID, pre, err)
		return nil, err
	}

	binaryMismatch, binaryNote := s.binaryDiff(inst, target)
	res := &RollbackResult{
		SnapshotID:            snapshotID,
		PreRollbackSnapshotID: pre.ID,
		InstanceID:            inst.ID,
		Stopped:               stopped,
		BinaryMismatch:        binaryMismatch,
		BinaryNote:            binaryNote,
		FinalStatus:           string(model.InstanceStatusStopped),
	}

	run := func(ctx context.Context, stage func(int, string)) (string, error) {
		report := func(p int, text string) {
			if stage != nil {
				stage(p, text)
			}
		}
		// M-4：任务体内再取一次实例互斥锁。两个并发回滚都可能在入队前通过检查，
		// 此处串行化保证「全量打包 + 覆盖式回放」不会交错破坏工作目录。
		releaseOp := s.lockInstanceOperation(inst.ID)
		if releaseOp == nil {
			err := fmt.Errorf("%w：同实例已有在途操作（快照回滚/版本变更）", ErrSnapshotOperationInFlight)
			s.recordRollbackAudit(operatorID, inst, res, false, err.Error())
			return "", err
		}
		defer releaseOp()
		report(30, "确认实例已停止…")
		// 回放前重验实例状态：入队瞬间的停服检查是 TOCTOU 的，实例可能在排队期间被拉起；
		// 运行态回放会与自动存档/世界写入竞争，直接覆盖工作目录有损坏风险。
		if err := s.ensureStoppedBeforeRestore(inst.ID, ctx); err != nil {
			s.recordRollbackAudit(operatorID, inst, res, false, err.Error())
			return "", err
		}
		report(40, "回放快照数据…")
		var backup model.Backup
		if err := s.db.First(&backup, target.RootBackupID).Error; err != nil {
			err := fmt.Errorf("快照底层备份记录缺失: %w", err)
			s.recordRollbackAudit(operatorID, inst, res, false, err.Error())
			return "", err
		}
		if err := s.backups.ExecuteRestore(&backup, stage); err != nil {
			s.recordRollbackAudit(operatorID, inst, res, false, err.Error())
			return "", err
		}
		report(90, "校验并收尾…")
		// 回滚后实例恒为 STOPPED（不自动拉起，留人工确认，spec §2.3 步骤 6）。
		if err := s.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
			Updates(map[string]any{
				"status":        model.InstanceStatusStopped,
				"status_reason": "",
			}).Error; err != nil {
			s.recordRollbackAudit(operatorID, inst, res, false, err.Error())
			return "", err
		}
		_ = s.db.Model(&model.InstanceSnapshot{}).Where("id = ?", target.ID).
			Update("state", model.SnapshotStateRolledBack).Error
		// 回滚后裁剪一轮（pre_rollback 不受条数裁剪，详见 pruneOnce）。
		// R10：只裁本实例——回滚只改变本实例的快照集合，全平台扫描属无谓成本
		// （每个有快照的实例 2~3 次查询），且它落在用户等待的任务体内；
		// 其它实例交由周期巡检处理。
		s.pruneInstanceOnce(inst.ID)
		// M-6：审计写在**终态**，而非 RunAsync 返回后立刻记成功。
		s.recordRollbackAudit(operatorID, inst, res, true, "")
		slog.Info("快照回滚完成", "snapshotId", target.ID, "instance", inst.Name,
			"instanceId", inst.ID, "instanceUuid", inst.UUID, "preRollbackSnapshotId", pre.ID,
			"binaryMismatch", binaryMismatch)
		return "", nil
	}

	if s.tasks == nil {
		if _, err := run(ctx, nil); err != nil {
			return nil, err
		}
		res.TaskID = ""
		return res, nil
	}
	taskID := s.tasks.RunAsync(RunSpec{
		NodeID: inst.NodeID, InstanceID: inst.ID, Kind: model.TaskKindSnapshotRollback,
		Title: fmt.Sprintf("回滚到快照 %s", target.Name), CreatedBy: operatorID,
	}, run)
	if taskID == "" {
		// 登记失败：本次已建的 pre_rollback 同样要清理（否则磁盘只增不减）。
		s.cleanupAbortedRollback(inst.ID, pre, errors.New("登记回滚任务失败"))
		return nil, errors.New("登记回滚任务失败")
	}
	res.TaskID = taskID
	return res, nil
}

// stopForRollback 运行态处置：需要时先优雅停服；已停止则原样返回。
//
// 与 FR-013 `BackupService.Restore` 的「运行态直接拒绝」不同：快照回滚由本服务承担停服编排，
// 因为「一键回滚」的用户预期就是「不用自己先停服再回来点」。
func (s *SnapshotService) stopForRollback(ctx context.Context, inst *model.Instance) (bool, error) {
	switch inst.Status {
	case model.InstanceStatusRunning, model.InstanceStatusStarting, model.InstanceStatusStopping:
	default:
		return false, nil
	}
	if s.instances == nil {
		return false, fmt.Errorf("%w（停止能力未装配）", ErrSnapshotInstanceRunning)
	}
	if err := s.instances.Stop(inst.ID); err != nil {
		return false, fmt.Errorf("%w：%v", ErrSnapshotInstanceRunning, err)
	}
	// 等待状态收敛到 STOPPED（优雅停止是异步的：CP 发起、Worker 执行、状态经心跳回写）。
	deadline := time.Now().Add(snapshotStopWaitTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		var current model.Instance
		if err := s.db.Select("status").First(&current, inst.ID).Error; err != nil {
			return true, err
		}
		if current.Status == model.InstanceStatusStopped || current.Status == model.InstanceStatusCrashed {
			return true, nil
		}
		time.Sleep(snapshotStopPollInterval)
	}
	return true, fmt.Errorf("%w：停止后等待 %s 仍未收敛到 STOPPED，已中止回滚", ErrSnapshotInstanceRunning, snapshotStopWaitTimeout)
}

// snapshotStopWaitTimeout 停服收敛等待上限（优雅停止默认 30s + 状态回写余量）。
const snapshotStopWaitTimeout = 90 * time.Second

// snapshotStopPollInterval 停服收敛轮询间隔。
const snapshotStopPollInterval = 500 * time.Millisecond

// binaryDiff 比较目标快照与当前工作目录二进制指纹；不一致只提示不替换（spec §2.3 步骤 5）。
func (s *SnapshotService) binaryDiff(inst *model.Instance, target *model.InstanceSnapshot) (bool, string) {
	if target.BinaryName == "" && target.BinarySHA256 == "" {
		return false, ""
	}
	name, sha := s.binaryFingerprint(inst)
	if name == "" {
		return true, "快照记录了二进制 " + target.BinaryName + "，但当前启动命令未指向可识别的二进制文件；回滚只覆盖数据，二进制未动"
	}
	if target.BinaryName != "" && target.BinaryName != name {
		return true, fmt.Sprintf("快照二进制为 %s，当前为 %s：数据已回滚，二进制未动（请用二进制版本管理回滚）", target.BinaryName, name)
	}
	if target.BinarySHA256 != "" && sha != "" && !strings.EqualFold(target.BinarySHA256, sha) {
		return true, fmt.Sprintf("快照二进制 %s 的摘要与当前不一致：数据已回滚，二进制未动（请用二进制版本管理回滚）", target.BinaryName)
	}
	return false, ""
}

// recordRollbackAudit 写回滚审计（instance.snapshot_rollback），**在终态调用**（M-6）。
//
// 成功与失败共用同一动作名，靠 success/errMsg 区分——与平台其它审计口径一致；
// 关键点是它不再在入队后立刻记成功：任务失败会补写一条 success=false 的记录。
func (s *SnapshotService) recordRollbackAudit(operatorID uint, inst *model.Instance, res *RollbackResult, success bool, errMsg string) {
	if s.audit == nil {
		return
	}
	detail := fmt.Sprintf("snapshot#%d pre_rollback#%d stopped=%t binaryMismatch=%t",
		res.SnapshotID, res.PreRollbackSnapshotID, res.Stopped, res.BinaryMismatch)
	s.audit.RecordResultSafe(operatorID, "instance.snapshot_rollback", "instance",
		strconv.FormatUint(uint64(inst.ID), 10), detail, "", success, errMsg)
}

// recordSnapshotCreateAudit 写创建审计（instance.snapshot_create，edge m-3）。
//
// 落点选 service 层而非 HTTP handler：MCP 工具与 HTTP 端点都要覆盖同一动作，
// 与 `instance.snapshot_rollback` 的落点一致（那条也写在 service 层）。
func (s *SnapshotService) recordSnapshotCreateAudit(operatorID uint, inst *model.Instance, snap *model.InstanceSnapshot) {
	if s.audit == nil || snap == nil {
		return
	}
	detail := fmt.Sprintf("snapshot#%d kind=%s", snap.ID, snap.Kind)
	s.audit.RecordResultSafe(operatorID, "instance.snapshot_create", "instance",
		strconv.FormatUint(uint64(inst.ID), 10), detail, "", true, "")
}

// ensureNoInflightOperation 拒绝「同实例已有在途破坏性操作」的新请求（M-4）。
//
// 覆盖快照创建/回滚与二进制版本变更：它们都会大范围读写工作目录，
// 交错执行会互相覆盖（例如两个回滚同时全量打包 + 覆盖式回放）。
// 返回 nil 表示可继续（含未注入任务中心的情形）。
func (s *SnapshotService) ensureNoInflightOperation(instanceID uint) error {
	if s.tasks == nil {
		return nil
	}
	var count int64
	if err := s.db.Model(&model.Task{}).
		Where("instance_id = ? AND kind IN ? AND state IN ?", instanceID,
			[]string{model.TaskKindSnapshotCreate, model.TaskKindSnapshotRollback, model.TaskKindBinaryUpgrade},
			[]model.TaskState{model.TaskStatePending, model.TaskStateRunning}).
		Count(&count).Error; err != nil {
		// 查询失败不阻断（保守放行由任务体内的互斥锁兜底），但要留痕。
		//
		// R13：这条「兜底」只在**单 Control Plane 进程**部署下成立——
		// `InstanceService.acquireInstanceOperation` 是进程内 sync.Mutex，多 CP 实例
		// （或滚动发布期新旧进程并存）时锁不跨进程，两道闸会同时失效，退化为无保护：
		// 两个全量打包 + 覆盖式回放可并发作用于同一工作目录。
		// 平台当前部署形态是单 CP（见 docs/ARCHITECTURE.md §12「Control Plane 一个」），
		// 故此处可接受；若将来支持多 CP，必须把互斥下沉到 DB（如任务表唯一部分索引或
		// advisory lock），而不是继续依赖本注释兜底。
		slog.Warn("查询实例在途操作失败（继续执行）", "instanceId", instanceID, "error", err)
		return nil
	}
	if count > 0 {
		return fmt.Errorf("%w：同实例已有在途操作（快照创建/回滚或版本变更），请等待其完成", ErrSnapshotOperationInFlight)
	}
	return nil
}

// lockInstanceOperation 取实例互斥锁（M-4）。未装配实例服务时返回 nil 表示无法加锁。
//
// 复用 InstanceService 的生命周期锁：快照回滚与启停/删除互斥，
// 避免「回滚执行中实例被拉起」这类破坏工作目录的交错。
func (s *SnapshotService) lockInstanceOperation(instanceID uint) func() {
	if s.instances == nil {
		return nil
	}
	return s.instances.acquireInstanceOperation(instanceID)
}

// ensureStoppedBeforeRestore 回放前重验实例状态（M-4 的「运行态保护扩展到回放阶段」）。
//
// 入队前的 stopForRollback 是 TOCTOU 的：任务在队列中等待时实例可能被其它路径拉起。
// 此处只做终点校验、不再自动停服——任务体内自动停服会与互斥锁形成「等待自己」的死锁风险，
// 且用户此刻的本意是回滚数据而非再次停服，明确失败并让人重试更安全。
func (s *SnapshotService) ensureStoppedBeforeRestore(instanceID uint, ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	var current model.Instance
	if err := s.db.Select("status").First(&current, instanceID).Error; err != nil {
		return err
	}
	switch current.Status {
	case model.InstanceStatusRunning, model.InstanceStatusStarting, model.InstanceStatusStopping:
		return fmt.Errorf("%w：回放前实例状态为 %s（排队期间被拉起），为避免覆盖运行中的数据已中止",
			ErrSnapshotInstanceRunning, current.Status)
	}
	return nil
}

// cleanupAbortedRollback 中止回滚时清理副作用（M-5）：回退实例状态 + 删除本次 pre_rollback。
//
// 两个副作用都要处理：
//   - STOPPING 残留：stopForRollback 超时说明实例没停成，若把它留在 STOPPING，
//     实例既没在跑也不再是 STOPPED，需要人工 Kill 才能恢复；这里回退到 STOPPED 并写明原因。
//   - pre_rollback 残留：它不受条数裁剪（spec §2.4），每次失败留一份全量归档会让磁盘只增不减。
//     失败即删除；删除本身失败只记日志，不掩盖原始错误。
func (s *SnapshotService) cleanupAbortedRollback(instanceID uint, pre *model.InstanceSnapshot, cause error) {
	if pre != nil && pre.ID != 0 {
		if err := s.Delete(pre.ID); err != nil {
			// R24：清理失败原先只 Warn 就结束——而每次失败都留一份不受条数裁剪的全量归档，
			// 正是 M-5 要消除的成本。这里除日志外补一条审计，让「累计发现」成为可能；
			// 日志同时带上实例 UUID（运维唯一能跨系统检索的标识，数值 ID 跨环境会撞）。
			slog.Warn("清理中止回滚产生的 pre_rollback 快照失败",
				"snapshotId", pre.ID, "instanceId", instanceID, "instanceUuid", s.instanceUUIDOf(instanceID), "error", err)
			if s.audit != nil {
				s.audit.RecordResultSafe(0, "instance.snapshot_cleanup_failed", "instance",
					strconv.FormatUint(uint64(instanceID), 10),
					"pre_rollback snapshot#"+strconv.FormatUint(uint64(pre.ID), 10), "", false, err.Error())
			}
		}
	}
	var current model.Instance
	if err := s.db.Select("status").First(&current, instanceID).Error; err != nil {
		return
	}
	if current.Status != model.InstanceStatusStopping {
		return
	}
	reason := "回滚中止：停服未收敛，实例已回退为 STOPPED；若进程仍在请检查后手动 Kill"
	if cause != nil {
		reason = "回滚中止：" + cause.Error() + "；实例已回退为 STOPPED，若进程仍在请手动 Kill"
	}
	if len(reason) > 255 {
		reason = reason[:255]
	}
	if err := s.db.Model(&model.Instance{}).Where("id = ?", instanceID).
		Updates(map[string]any{
			"status":        model.InstanceStatusStopped,
			"status_reason": reason,
		}).Error; err != nil {
		slog.Warn("回退实例状态失败（仍为 STOPPING，需人工处理）",
			"instanceId", instanceID, "instanceUuid", s.instanceUUIDOf(instanceID), "error", err)
	}
}

// instanceUUIDOf 取实例 UUID 用于日志（R24：UUID 是运维唯一能跨环境检索的标识）。
//
// 查询失败/实例已删时返回空串——日志字段缺失不影响主流程，绝不为它报错或阻塞。
func (s *SnapshotService) instanceUUIDOf(instanceID uint) string {
	if s == nil || s.db == nil || instanceID == 0 {
		return ""
	}
	var inst model.Instance
	if err := s.db.Select("uuid").First(&inst, instanceID).Error; err != nil {
		return ""
	}
	return inst.UUID
}

// audit 审计服务（可选注入）。
func (s *SnapshotService) SetAudit(a *AuditService) { s.audit = a }

// Delete 删除快照记录（底层 Backup 一并删除；pre_rollback 若被回滚链引用仍可删——
// 它只是「退回点」的记录，删除后不再能退回，但不会破坏其它快照）。
//
// R5：删底链前必须确认它**没有被别的快照**当作 RootBackupID 共享。快照在设计上
// 保证一对一（`RegisterFull` 每次新建 Backup 行），但 Delete 是公开 API
// （`DELETE /snapshots/:sid`），一旦出现共享（人工改库、或将来引入「同底链多快照」
// 优化），删一个快照会静默毁掉另一个——而 `annotateBacking` 已把「底链缺失」实现为
// 可表达状态，说明设计者预期底链会消失，删除侧必须做对称保护。
// 命中共享时只软删快照行、保留底链（另一个快照仍然可用），并把这件事写进日志。
func (s *SnapshotService) Delete(snapshotID uint) error {
	snap, err := s.GetByID(snapshotID)
	if err != nil {
		return err
	}
	if snap.RootBackupID != 0 {
		switch shared, serr := s.backlinkSharedByOtherSnapshot(snap.RootBackupID, snapshotID); {
		case serr != nil:
			// 查不出来源不明的引用时保守保留底链：留一份归档远好于毁掉别的快照。
			slog.Warn("查询底链共享引用失败，本次保留底层备份只删快照记录",
				"snapshotId", snapshotID, "backupId", snap.RootBackupID, "error", serr)
		case shared:
			slog.Warn("底链被其它快照共享，保留底层备份只删本快照记录",
				"snapshotId", snapshotID, "backupId", snap.RootBackupID)
		default:
			// 底层备份删除失败（被增量链引用等）不阻断快照记录清理。
			if err := s.backups.Delete(snap.RootBackupID); err != nil {
				slog.Warn("删除快照底层备份失败（继续清理快照记录）", "snapshotId", snapshotID, "error", err)
			}
		}
	}
	return s.db.Delete(&model.InstanceSnapshot{}, snapshotID).Error
}

// backlinkSharedByOtherSnapshot 报告该底链是否仍被除 excludeSnapshotID 之外的快照引用（R5）。
//
// 与「被增量子备份引用」（BackupService.Delete 的护栏）是**两个不同维度**，故不能直接
// 复用后者的判定：前者是「同一份归档被多个快照共享」，后者是「归档被增量链占为父」。
// 两者在删除前都必须拦住，各自覆盖一种数据链断裂方式。
func (s *SnapshotService) backlinkSharedByOtherSnapshot(backupID, excludeSnapshotID uint) (bool, error) {
	if backupID == 0 {
		return false, nil
	}
	var count int64
	if err := s.db.Model(&model.InstanceSnapshot{}).
		Where("root_backup_id = ? AND id <> ?", backupID, excludeSnapshotID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ensureSnapshotCapacity 快照创建前的上限/配额保护（m-2，spec §2.4）。
//
// 目标不是精确预测归档大小（无法预测），而是拦住「保留策略还没生效前就把磁盘写满」
// 这一类可预见的自伤：快照是全量归档，同一实例重复创建会成倍占用节点磁盘。
// 判据用**已完成的快照总占用 + 现存量**两条保守上限：
//   - 单实例快照数量上限 snapshot.max_per_instance（默认 20，0=不限）；
//   - 单实例快照总占用上限 snapshot.max_total_mb（默认 0=不限，按需开启）。
//
// pre_rollback 不受数量上限约束（它是回滚的退回点，spec §2.4 明确不可被挤掉）——
// 但调用方在回滚流程内做的正是 pre_rollback，故本函数只对非 pre_rollback 生效。
func (s *SnapshotService) ensureSnapshotCapacity(inst *model.Instance) error {
	if s == nil || s.db == nil || inst == nil {
		return nil
	}
	maxCount := s.intSetting(SettingKeySnapshotMaxPerInstance, 0)
	maxTotalMB := s.intSetting(SettingKeySnapshotMaxTotalMB, 0)
	if maxCount <= 0 && maxTotalMB <= 0 {
		return nil
	}
	if maxCount > 0 {
		var count int64
		if err := s.db.Model(&model.InstanceSnapshot{}).
			Where("instance_id = ? AND kind <> ? AND state <> ?", inst.ID,
				model.SnapshotKindPreRollback, model.SnapshotStateFailed).
			Count(&count).Error; err == nil && count >= int64(maxCount) {
			return fmt.Errorf("%w：该实例快照数已达上限 %d 条，请先删除旧快照或调高 snapshot.max_per_instance",
				ErrSnapshotQuotaExceeded, maxCount)
		}
	}
	if maxTotalMB > 0 {
		var used float64
		if err := s.db.Model(&model.InstanceSnapshot{}).
			Where("instance_id = ?", inst.ID).
			Select("COALESCE(SUM(size_mb), 0)").Scan(&used).Error; err == nil && used >= float64(maxTotalMB) {
			return fmt.Errorf("%w：该实例快照总占用约 %.0f MiB 已达上限 %d MiB，请先删除旧快照或调高 snapshot.max_total_mb",
				ErrSnapshotQuotaExceeded, used, maxTotalMB)
		}
	}
	return nil
}
