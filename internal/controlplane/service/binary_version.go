package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/JianManager/internal/controlplane/model"
	"github.com/wcpe/JianManager/proto/workerpb"
)

// 二进制版本管理服务（FR-468 §2.3/§2.4/§2.5）。
//
// 升级 = 换绑定 + 换文件（走任务中心）；回滚 = 一级对称操作（Current ↔ Previous 交换）。
// 制品内容是版本真源（Asset），本服务只改「实例当前跑哪个 asset」这一事实。

// ErrBinaryUpgradeInstanceRunning 实例运行中拒绝升级/回滚。
//
// 与 BackupService.Restore 的 ErrInstanceNotStopped 同口径：替换正在执行的可执行文件
// 在 Unix 上虽不必然失败，但会留下「进程跑旧 inode、磁盘是新文件」的不一致窗口，
// 且升级后必须重启才生效——静默进入这个窗口比直接拒绝更糟。首版不做自动停服编排
// （见 FR-466 快照回滚负责停服编排），由运维先停服再升级。
var ErrBinaryUpgradeInstanceRunning = errors.New("实例运行中，请先停止实例再执行版本变更")

// ErrBinaryVersionOperationInFlight 同实例已有在途的破坏性操作（FR-468 M-4）。
//
// 升级/回滚都会覆盖工作目录里的可执行文件，与快照回滚/创建属于同一互斥域；
// 两个并发版本变更（或一个版本变更与一个快照回滚）交错执行会互相覆盖文件与绑定记录。
var ErrBinaryVersionOperationInFlight = errors.New("同实例已有在途操作")

// BinaryVersionService 实例二进制版本查询 / 受控升级 / 一级回滚（FR-468）。
type BinaryVersionService struct {
	db        *gorm.DB
	provision *ProvisionService
	audit     *AuditService
}

// NewBinaryVersionService 创建二进制版本服务。
func NewBinaryVersionService(db *gorm.DB, provision *ProvisionService) *BinaryVersionService {
	return &BinaryVersionService{db: db, provision: provision}
}

// SetAudit 注入审计服务（main 接线）；nil 时不写审计。
func (s *BinaryVersionService) SetAudit(a *AuditService) { s.audit = a }

// BinaryUpgradeCandidate 可升级到的候选版本（FR-468 §2.5）。
type BinaryUpgradeCandidate struct {
	AssetID  uint   `json:"assetId"`
	Filename string `json:"filename"`
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// BinaryVersionView 实例二进制版本视图（GET /instances/:id/binary-version）。
type BinaryVersionView struct {
	InstanceID uint `json:"instanceId"`
	// Bound 是否登记了版本绑定（非 binary/beacon 实例或搭建早于 FR-468 时为 false）。
	Bound bool `json:"bound"`
	// CurrentAssetID 当前生效制品；0 = 无制品库版本（url/node_file 来源）。
	CurrentAssetID  uint   `json:"currentAssetId"`
	CurrentVersion  string `json:"currentVersion"`
	CurrentFilename string `json:"currentFilename"`
	CurrentSHA256   string `json:"currentSha256"`
	// HasRollback 是否存在可回滚的上一版本。
	HasRollback      bool   `json:"hasRollback"`
	PreviousAssetID  uint   `json:"previousAssetId"`
	PreviousVersion  string `json:"previousVersion"`
	PreviousFilename string `json:"previousFilename"`
	// DriftDetected 版本漂移（启动命令不再指向绑定文件 / 绑定摘要与制品库不一致 / 磁盘实际摘要不符）。
	DriftDetected bool   `json:"driftDetected"`
	DriftReason   string `json:"driftReason,omitempty"`
	// DiskSHA256 工作目录里实际二进制文件的 SHA-256（FR-468 §2.5，经 Worker HashFile 现算）。
	// 仅在 DiskSHA256Checked 为 true 时有效。
	DiskSHA256 string `json:"diskSha256,omitempty"`
	// DiskSHA256Checked 本次查询是否真的做了磁盘比对（节点离线/实例运行中/文件过大时为 false）。
	// 与 DriftDetected 分开表达：未校验不等于没有漂移，前端不得把「未校验」渲染成「已核对」。
	DiskSHA256Checked bool `json:"diskSha256Checked"`
	// DiskCheckSkippedReason 「读过盘但没能算出摘要」的原因（N-4，文件过大/读取失败）。
	//
	// 与 DriftReason 分开表达：**「未校验」不是「漂移」**。原先这两种情形共用 reason 字符串，
	// 而调用方见 reason != "" 就置 DriftDetected=true，于是「文件太大、本次没比对」被前端
	// 渲染成「检测到版本漂移」告警——与 DiskSHA256Checked 自身的注释直接矛盾，且会把
	// 无病呻吟的告警推给运维。真正的漂移必须是「已成功算出摘要且与绑定不符」。
	DiskCheckSkippedReason string `json:"diskCheckSkippedReason,omitempty"`
	// NoLibraryVersion 无制品库版本（url/node_file 来源）——升级入口应给明确提示。
	NoLibraryVersion bool   `json:"noLibraryVersion"`
	Note             string `json:"note,omitempty"`
	// Candidates 可升级到的候选版本（按 id 倒序，含当前版本）。
	Candidates []BinaryUpgradeCandidate `json:"candidates"`
}

// BinaryVersionResult 升级/回滚结果。
type BinaryVersionResult struct {
	TaskID         string `json:"taskId"`
	InstanceID     uint   `json:"instanceId"`
	FromAssetID    uint   `json:"fromAssetId"`
	FromVersion    string `json:"fromVersion"`
	ToAssetID      uint   `json:"toAssetId"`
	ToVersion      string `json:"toVersion"`
	ToFilename     string `json:"toFilename"`
	StartCommand   string `json:"startCommand"`
	CommandChanged bool   `json:"commandChanged"`
}

// View 读取实例的二进制版本视图（含可升级候选与漂移提示）。
func (s *BinaryVersionService) View(instanceID uint) (*BinaryVersionView, error) {
	var inst model.Instance
	// N-4 顺带修正：Select 必须带上 status / uuid / node_id——diskBinaryDigest 全部要用
	// （status 决定能否比对、uuid 是 Worker 侧实例定位、node_id 定位节点连接）。
	// 原先漏选 status，inst.Status 恒为零值，于是「实例必须已停止/已崩溃才做磁盘比对」这条
	// 守卫**恒为假**：磁盘内容比对（M-3 的唯一判据）在任何状态下都不会执行，
	// 磁盘漂移检测形同不存在。这是除 DriftDetected 误标之外的第二个 N-4 缺口。
	if err := s.db.Select("id", "uuid", "name", "node_id", "status", "start_command", "process_type", "type").
		First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, err
	}
	binding, err := s.provision.instanceBinaryBinding(instanceID)
	if err != nil {
		return nil, err
	}
	view := &BinaryVersionView{InstanceID: instanceID, Candidates: []BinaryUpgradeCandidate{}}
	if binding == nil {
		view.Note = "该实例未登记二进制版本绑定（非 binary/beacon 实例，或搭建早于版本管理落地）"
		return view, nil
	}
	view.Bound = true
	view.CurrentAssetID = binding.CurrentAssetID
	view.CurrentVersion = binding.CurrentVersion
	view.CurrentFilename = binding.CurrentFilename
	view.CurrentSHA256 = binding.CurrentSHA256
	view.PreviousAssetID = binding.PreviousAssetID
	view.PreviousFilename = binding.PreviousFilename
	view.HasRollback = binding.HasRollbackTarget()
	if binding.PreviousAssetID != 0 {
		if asset, aerr := s.provision.resolveBinaryAsset(binding.PreviousAssetID); aerr == nil {
			view.PreviousVersion = asset.Version
		}
	}
	if binding.CurrentAssetID == 0 {
		view.NoLibraryVersion = true
		view.Note = "当前二进制来自 url / node_file 来源，无制品库版本；如需版本管理请先把它入库为制品再升级"
	}

	// 漂移检测（FR-468 §2.5）：三层判据，从「确定」到「可疑」。
	//
	// ①启动命令是否仍指向绑定文件（最确定的漂移：命令被人工改过）；
	// ②绑定记录的摘要 vs 制品库摘要（DB vs DB，能发现「制品被替换或重新入库」）；
	// ③**实际磁盘文件的 sha256** vs 绑定摘要（唯一能发现「人工同名覆盖了工作目录里的
	//   二进制」的判据——①②在此场景下全都恒等，正是 M-3 要补的缺口）。
	if view.CurrentFilename != "" && inst.StartCommand != "" && !strings.Contains(inst.StartCommand, view.CurrentFilename) {
		view.DriftDetected = true
		view.DriftReason = driftAppend(view.DriftReason, "启动命令已不指向绑定文件 "+view.CurrentFilename+"（可能被人工换过文件）")
	}
	if binding.CurrentAssetID != 0 {
		if asset, aerr := s.provision.resolveBinaryAsset(binding.CurrentAssetID); aerr == nil {
			if binding.CurrentSHA256 != "" && !strings.EqualFold(binding.CurrentSHA256, asset.SHA256) {
				view.DriftDetected = true
				view.DriftReason = driftAppend(view.DriftReason, "绑定摘要与制品库摘要不一致（制品内容被替换或重新入库）")
			}
		}
	}
	// ③ 磁盘实比：只在有绑定摘要可比对且实例处于停止态时做（运行中的可执行文件可能被
	// 正常写操作/自动更新改动，此处只报告事实，不阻断任何操作）。
	//
	// N-4：三态必须分开——已算出摘要 / 读过但没算成（过大、不可读）/ 根本没读。
	// 只有「已算出摘要且与绑定不符」才是漂移；「本次未比对」只降级为提示字段，
	// 绝不能借 DriftReason 把「没检查」渲染成「检查出错」。
	if digest := s.diskBinaryDigest(&inst, binding); digest.checked {
		view.DiskSHA256 = digest.sha256
		view.DiskSHA256Checked = true
		if digest.skipReason != "" {
			view.DiskCheckSkippedReason = digest.skipReason
		} else if digest.sha256 != "" && binding.CurrentSHA256 != "" &&
			!strings.EqualFold(binding.CurrentSHA256, digest.sha256) {
			view.DriftDetected = true
			view.DriftReason = driftAppend(view.DriftReason,
				"工作目录中的实际文件摘要与绑定记录不一致（文件被人工替换，非经版本管理变更）")
		}
	}

	cands, cerr := s.upgradeCandidates(binding)
	if cerr != nil {
		// 候选列表是增强信息，失败不阻断版本视图。
		slog.Warn("查询二进制升级候选失败", "instanceId", instanceID, "error", cerr)
	}
	view.Candidates = cands
	return view, nil
}

// binaryDiskDigestMaxBytes 磁盘摘要比对的单次读盘上限（FR-468 §2.5）。
//
// 版本视图是**高频读**接口（详情页打开即调），不能每次都去读一个数 GB 的文件：
// 这里给一个上限，超过即放弃比对并在 DriftReason 里如实说明「未校验」，
// 而不是静默略过（静默略过等于假装核对过）。
const binaryDiskDigestMaxBytes = 512 * 1024 * 1024

// driftAppend 以分号拼接漂移原因（避免前一处原因被后一处覆盖）。
func driftAppend(existing, next string) string {
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	return existing + "；" + next
}

// diskBinaryDigestResult 磁盘内容比对的**三态**结果（N-4）。
//
// 为什么需要独立类型而不是 (sha, checked, reason)：后者的三元组让调用方很容易把
// 「reason 非空」误当成「有漂移」，而这两种情形的因果完全不同——
//   - checked=false：这次根本没读盘（运行中/无节点连接/通道异常）；
//   - checked=true 且 skipReason!=""：读过盘但没算出摘要（文件过大/不可读）；
//   - checked=true 且 sha256!=""：真的算出了摘要，此时才可能判定漂移。
//
// 只有第 3 种且与绑定摘要不符才是漂移；前两种只能如实提示「本次未比对」。
type diskBinaryDigestResult struct {
	sha256  string
	checked bool
	// skipReason 已尝试比对但未能得出摘要的原因（过大/不可读）；非空时 sha256 必为空。
	skipReason string
}

// diskBinaryDigest 经 Worker 现算工作目录里启动二进制的真实 sha256（FR-468 §2.5 / M-3）。
//
// 情形与措辞：
//   - 实例运行中：可执行文件可能正被正常使用/自动更新，比对结论不可靠 → 不校验；
//   - 无节点连接：取不到磁盘事实 → 不校验（不把「取不到」当「无漂移」）；
//   - 文件过大：放弃比对并**如实说明**（避免详情页读盘数 GB）；
//   - 文件不可读/不存在：给出独立提示（可能是人工删除或移动），**不谎称漂移**——
//     「未校验」与「真漂移」是两件事（N-4）。
func (s *BinaryVersionService) diskBinaryDigest(inst *model.Instance, binding *model.InstanceBinaryBinding) diskBinaryDigestResult {
	filename := strings.TrimSpace(binaryFilenameOf(inst, binding))
	if filename == "" {
		return diskBinaryDigestResult{}
	}
	if inst.Status != model.InstanceStatusStopped && inst.Status != model.InstanceStatusCrashed {
		return diskBinaryDigestResult{}
	}
	if s.provision == nil || s.provision.pool == nil {
		return diskBinaryDigestResult{}
	}
	var node model.Node
	if err := s.db.Select("uuid").First(&node, inst.NodeID).Error; err != nil || node.UUID == "" {
		return diskBinaryDigestResult{}
	}
	client, ok := s.provision.pool.Get(node.UUID)
	if !ok || client == nil || client.Worker == nil {
		// client.Worker 为 nil 时调用会直接 nil 解引用 panic。这是增强信息的读路径，
		// 任何依赖缺失都只应降级为「未校验」，绝不能让详情页把 CP 打崩。
		return diskBinaryDigestResult{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), binaryDiskDigestTimeout)
	defer cancel()
	resp, err := client.Worker.HashFile(ctx, &workerpb.HashFileRequest{
		InstanceUuid: inst.UUID,
		Path:         filename,
		MaxBytes:     binaryDiskDigestMaxBytes,
	})
	if err != nil || resp == nil {
		// 通道异常：不阻断版本视图（它是增强信息），但明确留痕「未校验」。
		slog.Debug("二进制磁盘摘要比对未完成（通道异常）", "instanceId", inst.ID, "error", err)
		return diskBinaryDigestResult{}
	}
	switch {
	case resp.TooLarge:
		return diskBinaryDigestResult{checked: true, skipReason: fmt.Sprintf(
			"工作目录中的 %s 超过 %d MiB，本次未做磁盘内容比对（仅比对绑定记录）",
			filename, binaryDiskDigestMaxBytes/(1024*1024))}
	case resp.Error != "":
		if inst.Status == model.InstanceStatusStopped || inst.Status == model.InstanceStatusCrashed {
			return diskBinaryDigestResult{checked: true, skipReason: fmt.Sprintf(
				"启动命令指向的 %s 无法读取（%s）：文件可能已被人工删除或移动，本次未能比对内容",
				filename, resp.Error)}
		}
		return diskBinaryDigestResult{}
	default:
		return diskBinaryDigestResult{
			sha256:  strings.ToLower(strings.TrimSpace(resp.Sha256)),
			checked: true,
		}
	}
}

// binaryFilenameOf 取「当前应作为启动二进制的文件名」：绑定优先，回退启动命令首 token。
func binaryFilenameOf(inst *model.Instance, binding *model.InstanceBinaryBinding) string {
	if binding != nil && strings.TrimSpace(binding.CurrentFilename) != "" {
		return strings.TrimSpace(binding.CurrentFilename)
	}
	fields := strings.Fields(strings.TrimSpace(inst.StartCommand))
	if len(fields) == 0 {
		return ""
	}
	name := strings.TrimPrefix(fields[0], "./")
	if strings.ContainsAny(name, "/\\") {
		return ""
	}
	return name
}

// upgradeCandidates 取可升级候选：与当前绑定同形态（Beacon 形态 / 通用二进制形态）的可用制品。
func (s *BinaryVersionService) upgradeCandidates(binding *model.InstanceBinaryBinding) ([]BinaryUpgradeCandidate, error) {
	var assets []model.Asset
	beaconShaped := isBeaconLinuxBinaryName(binding.CurrentFilename)
	if err := s.db.Order("id DESC").Limit(100).Find(&assets).Error; err != nil {
		return nil, err
	}
	out := make([]BinaryUpgradeCandidate, 0, len(assets))
	for i := range assets {
		a := assets[i]
		if a.Size <= 0 || a.StorageState == model.AssetStorageLost {
			continue
		}
		if beaconShaped && !isBeaconLinuxBinaryName(a.Filename) {
			continue
		}
		if !validBinaryFilename(a.Filename) {
			continue
		}
		out = append(out, BinaryUpgradeCandidate{
			AssetID: a.ID, Filename: a.Filename, Version: a.Version, SHA256: a.SHA256, Size: a.Size,
		})
		if len(out) >= 50 {
			break
		}
	}
	return out, nil
}

// Upgrade 把实例升级到指定制品版本（FR-468 §2.3）。
//
// 步骤：校验目标制品 → 校验实例状态（必须已停止）→ Previous=Current（回滚点）→
// 下发取件 → 落盘名变化则更新 startCommand → 换绑定 → 审计 instance.binary_upgrade。
func (s *BinaryVersionService) Upgrade(ctx context.Context, instanceID, targetAssetID, operatorID uint) (*BinaryVersionResult, error) {
	if targetAssetID == 0 {
		return nil, fmt.Errorf("%w: 缺少 assetId", ErrBinaryVersionTargetInvalid)
	}
	inst, err := s.loadStoppedBinaryInstance(instanceID)
	if err != nil {
		return nil, err
	}
	binding, err := s.provision.instanceBinaryBinding(instanceID)
	if err != nil {
		return nil, err
	}
	asset, err := s.validateUpgradeTarget(targetAssetID)
	if err != nil {
		return nil, err
	}
	if binding != nil && binding.CurrentAssetID == targetAssetID {
		return nil, fmt.Errorf("%w: 目标版本与当前版本相同（asset#%d）", ErrBinaryVersionTargetInvalid, targetAssetID)
	}
	return s.applyVersionChange(ctx, inst, binding, asset, operatorID, "upgrade")
}

// Rollback 回滚到上一版本（FR-468 §2.4，一级回滚，语义对称：回滚本身也可再回滚）。
func (s *BinaryVersionService) Rollback(ctx context.Context, instanceID, operatorID uint) (*BinaryVersionResult, error) {
	inst, err := s.loadStoppedBinaryInstance(instanceID)
	if err != nil {
		return nil, err
	}
	binding, err := s.provision.instanceBinaryBinding(instanceID)
	if err != nil {
		return nil, err
	}
	if binding == nil || binding.PreviousAssetID == 0 {
		return nil, ErrNoPreviousBinaryVersion
	}
	asset, err := s.validateUpgradeTarget(binding.PreviousAssetID)
	if err != nil {
		return nil, fmt.Errorf("%w（上一版本 asset#%d）：%v", ErrNoPreviousBinaryVersion, binding.PreviousAssetID, err)
	}
	return s.applyVersionChange(ctx, inst, binding, asset, operatorID, "rollback")
}

// loadStoppedBinaryInstance 载入实例并校验「可做版本变更」：实例存在且已停止。
func (s *BinaryVersionService) loadStoppedBinaryInstance(instanceID uint) (*model.Instance, error) {
	var inst model.Instance
	if err := s.db.First(&inst, instanceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInstanceNotFound
		}
		return nil, err
	}
	switch inst.Status {
	case model.InstanceStatusStarting, model.InstanceStatusRunning, model.InstanceStatusStopping:
		return nil, fmt.Errorf("%w（当前状态 %s）", ErrBinaryUpgradeInstanceRunning, inst.Status)
	}
	return &inst, nil
}

// validateUpgradeTarget 校验目标制品是合法可交付的二进制（尺寸 >0、非 lost、文件名合法）。
func (s *BinaryVersionService) validateUpgradeTarget(assetID uint) (*model.Asset, error) {
	asset, err := s.provision.resolveBinaryAsset(assetID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBinaryVersionTargetInvalid, err)
	}
	if !validBinaryFilename(beaconAssetFilename(asset)) {
		return nil, fmt.Errorf("%w: asset#%d 文件名非法（%s）", ErrBinaryVersionTargetInvalid, assetID, asset.Filename)
	}
	return asset, nil
}

// applyVersionChange 执行一次版本变更（升级与回滚共用，direction 仅影响文案与审计动作）。
//
// 绑定切换与文件落地在同一任务内完成；任务失败时绑定不变（保持「当前版本」为真），
// 避免出现「绑定已改但文件没换」的虚假一致。
//
// 并发与审计（M-4/M-6）：
//   - 入队前拒绝「同实例已有在途 snapshot_*/binary_upgrade 任务」；
//   - 任务体内再取一次实例互斥锁（把「同实例串行」从检查时延伸到执行期）；
//   - 启动命令与绑定切换在同一事务内提交（m-9），避免「命令已改、绑定未改」；
//   - 审计写在**终态**（成功/失败各一次），而不是 RunAsync 返回后立刻记成功。
func (s *BinaryVersionService) applyVersionChange(ctx context.Context, inst *model.Instance, binding *model.InstanceBinaryBinding,
	asset *model.Asset, operatorID uint, direction string) (*BinaryVersionResult, error) {
	if s.provision == nil || s.provision.tasks == nil {
		return nil, errors.New("版本变更需要任务中心支撑（当前未装配任务服务）")
	}
	if err := s.ensureNoInflightOperation(inst.ID); err != nil {
		return nil, err
	}
	plan, err := s.provision.planForAsset(asset, inst.StartCommand)
	if err != nil {
		return nil, err
	}

	fromAssetID, fromVersion := uint(0), ""
	if binding != nil {
		fromAssetID, fromVersion = binding.CurrentAssetID, binding.CurrentVersion
	}
	// 落盘名变化 → 只替换启动命令里对**旧二进制文件**的引用（`./run.sh` 这类不含旧名的
	// 命令是运维的明确意图，不改写）。回滚时以绑定时记录的启动命令为基准（m-6）。
	newCommand, rewritten := commandAfterVersionChange(inst, binding, plan)
	if !rewritten {
		newCommand = ""
	}

	action := "instance.binary_upgrade"
	directionText := binaryDirectionText(direction)
	if direction == "rollback" {
		action = "instance.binary_rollback"
	}
	title := fmt.Sprintf("二进制%s %s → %s", directionText, inst.Name, binaryAssetLabel(asset))
	instCopy := *inst
	res := &BinaryVersionResult{
		InstanceID: inst.ID, FromAssetID: fromAssetID, FromVersion: fromVersion,
		ToAssetID: asset.ID, ToVersion: asset.Version, ToFilename: plan.Filename,
		CommandChanged: newCommand != "",
	}
	auditDetail := fmt.Sprintf("asset#%d → asset#%d（%s）", fromAssetID, asset.ID, plan.Filename)
	taskID := s.provision.tasks.RunAsync(RunSpec{
		NodeID: inst.NodeID, InstanceID: inst.ID, Kind: model.TaskKindBinaryUpgrade,
		Title: title, CreatedBy: operatorID, Timeout: binaryVersionChangeTimeout,
	}, func(taskCtx context.Context, stage func(int, string)) (string, error) {
		// M-4：任务体内取实例互斥锁，把「同实例串行」从入队前检查延伸到真正执行期。
		releaseOp := s.lockInstanceOperation(inst.ID)
		if releaseOp != nil {
			defer releaseOp()
		}
		// 回放/替换前重验实例状态：入队瞬间的检查是 TOCTOU 的，实例可能在排队期间被拉起。
		if err := s.ensureStoppedBeforeChange(inst.ID); err != nil {
			s.recordVersionAudit(operatorID, action, inst.ID, auditDetail, false, err.Error())
			return "", err
		}
		stage(binaryStageResolve, "解析目标版本…")
		if err := s.provision.provisionBinaryOnWorker(taskCtx, &instCopy, plan, stage); err != nil {
			_ = s.db.Model(&model.Instance{}).Where("id = ?", inst.ID).
				Update("status_reason", "版本变更失败："+err.Error()).Error
			s.recordVersionAudit(operatorID, action, inst.ID, auditDetail, false, err.Error())
			return "", err
		}
		stage(binaryStageDeriveCmd, "更新启动命令与版本绑定…")
		// m-9：启动命令与绑定切换必须同成败——否则会出现「命令已指向新文件、绑定仍是旧版本」，
		// 后续漂移检测与回滚都会基于错误的「当前版本」做判断。
		if err := s.commitVersionChange(inst, binding, asset, plan, direction, newCommand); err != nil {
			s.recordVersionAudit(operatorID, action, inst.ID, auditDetail, false, err.Error())
			return "", err
		}
		res.StartCommand = newCommand
		// M-6：审计写在终态（成功），失败路径在上方各分支补写。
		s.recordVersionAudit(operatorID, action, inst.ID, auditDetail, true, "")
		slog.Info("二进制版本变更完成", "instance", inst.Name, "instanceId", inst.ID,
			"direction", direction, "fromAsset", fromAssetID, "toAsset", asset.ID, "filename", plan.Filename)
		return "", nil
	})
	if taskID == "" {
		return nil, errors.New("登记版本变更任务失败")
	}
	res.TaskID = taskID
	return res, nil
}

// commitVersionChange 在同一事务内提交「启动命令更新 + 绑定切换」（m-9）。
func (s *BinaryVersionService) commitVersionChange(inst *model.Instance, binding *model.InstanceBinaryBinding,
	asset *model.Asset, plan *binaryFetchPlan, direction, newCommand string) error {
	next := &model.InstanceBinaryBinding{
		InstanceID:            inst.ID,
		CurrentAssetID:        asset.ID,
		CurrentFilename:       plan.Filename,
		CurrentSHA256:         strings.ToLower(strings.TrimSpace(asset.SHA256)),
		CurrentVersion:        asset.Version,
		StartCommandAtBinding: bindingStartCommandAt(inst, plan),
	}
	if binding != nil {
		// 升级：Previous=旧 Current（回滚点）；回滚：Current ↔ Previous 交换，
		// 使「回滚」本身也可再回滚（语义对称）。
		next.PreviousAssetID = binding.CurrentAssetID
		next.PreviousFilename = binding.CurrentFilename
		next.PreviousSHA256 = binding.CurrentSHA256
		next.ID = binding.ID
	}
	_ = direction // 两个方向的 Previous 写入口径相同，方向只影响文案与审计动作

	return s.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"status_reason": ""}
		if newCommand != "" {
			updates["start_command"] = newCommand
		}
		if err := tx.Model(&model.Instance{}).Where("id = ?", inst.ID).Updates(updates).Error; err != nil {
			return err
		}
		if binding == nil {
			return s.upsertBindingInTx(tx, next)
		}
		return tx.Model(&model.InstanceBinaryBinding{}).Where("id = ?", binding.ID).
			Select("*").Omit("id", "instance_id").Updates(next).Error
	})
}

// upsertBindingInTx 在给定事务内 upsert 绑定（复用生产服务的唯一键口径）。
func (s *BinaryVersionService) upsertBindingInTx(tx *gorm.DB, next *model.InstanceBinaryBinding) error {
	var existing model.InstanceBinaryBinding
	err := tx.Where("instance_id = ?", next.InstanceID).First(&existing).Error
	if err == nil {
		next.ID = existing.ID
		return tx.Model(&model.InstanceBinaryBinding{}).Where("id = ?", existing.ID).
			Select("*").Omit("id", "instance_id").Updates(next).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(next).Error
}

// commandAfterVersionChange 计算版本变更后应落库的启动命令（m-6）。
//
// 语义：把启动命令里对**旧二进制文件**的引用替换为新落盘名；当启动命令是脚本（如
// `./run.sh`，不含旧文件名）时保留运维意图、不改写。返回 (新命令, 是否需落库)。
//
// 实现细节：优先用 binding.StartCommandAtBinding 还原「绑定时刻的命令形态」，
// 使「升级→回滚」回到的是**绑定当时的命令**而不是被一路改写过的命令——
// 该字段此前只写不读，这里正是它被引入时承诺的用途。
func commandAfterVersionChange(inst *model.Instance, binding *model.InstanceBinaryBinding, plan *binaryFetchPlan) (string, bool) {
	base := strings.TrimSpace(inst.StartCommand)
	oldFilename := bindingFilenameOf(binding, inst)
	if binding != nil {
		// 绑定时刻的命令作为回滚基准：仅当它与当前命令「指向同一旧文件」时才采用，
		// 否则说明运维后来又手工改过命令，此时应以当前命令为准（不覆盖运维意图）。
		atBinding := strings.TrimSpace(binding.StartCommandAtBinding)
		if atBinding != "" && oldFilename != "" && strings.Contains(atBinding, oldFilename) {
			base = atBinding
		}
	}
	next, rewritten := replaceBinaryTokenInStartCommand(base, oldFilename, plan.Filename)
	if !rewritten {
		return "", false
	}
	if next == strings.TrimSpace(inst.StartCommand) {
		return "", false
	}
	return next, true
}

// bindingStartCommandAt 记录「本次绑定对应的启动命令」（m-6）。
//
// 这是回滚基准的来源：它记录的是**本次变更后实例实际会用的启动命令**，
// 而不是 plan.Filename 直接派生的形式（后者会在运维用脚本启动时丢掉脚本意图）。
func bindingStartCommandAt(inst *model.Instance, plan *binaryFetchPlan) string {
	if inst != nil && strings.TrimSpace(inst.StartCommand) != "" {
		return strings.TrimSpace(inst.StartCommand)
	}
	return deriveBinaryStartCommand(plan.Filename)
}

// ensureNoInflightOperation 拒绝「同实例已有在途破坏性操作」的新版本变更（M-4）。
func (s *BinaryVersionService) ensureNoInflightOperation(instanceID uint) error {
	var count int64
	if err := s.db.Model(&model.Task{}).
		Where("instance_id = ? AND kind IN ? AND state IN ?", instanceID,
			[]string{model.TaskKindSnapshotCreate, model.TaskKindSnapshotRollback, model.TaskKindBinaryUpgrade},
			[]model.TaskState{model.TaskStatePending, model.TaskStateRunning}).
		Count(&count).Error; err != nil {
		slog.Warn("查询实例在途操作失败（继续执行）", "instanceId", instanceID, "error", err)
		return nil
	}
	if count > 0 {
		return fmt.Errorf("%w：同实例已有在途操作（快照创建/回滚或版本变更），请等待其完成", ErrBinaryVersionOperationInFlight)
	}
	return nil
}

// lockInstanceOperation 取实例互斥锁（M-4）；未装配实例服务时返回 nil。
func (s *BinaryVersionService) lockInstanceOperation(instanceID uint) func() {
	if s.provision == nil || s.provision.instance == nil {
		return nil
	}
	return s.provision.instance.acquireInstanceOperation(instanceID)
}

// ensureStoppedBeforeChange 执行前重验实例状态（M-4 的「保护扩展到执行阶段」）。
func (s *BinaryVersionService) ensureStoppedBeforeChange(instanceID uint) error {
	var current model.Instance
	if err := s.db.Select("status").First(&current, instanceID).Error; err != nil {
		return err
	}
	switch current.Status {
	case model.InstanceStatusStarting, model.InstanceStatusRunning, model.InstanceStatusStopping:
		return fmt.Errorf("%w：执行前实例状态为 %s（排队期间被拉起），为避免替换运行中的可执行文件已中止",
			ErrBinaryUpgradeInstanceRunning, current.Status)
	}
	return nil
}

// recordVersionAudit 写版本变更审计（终态，M-6）。
func (s *BinaryVersionService) recordVersionAudit(operatorID uint, action string, instanceID uint, detail string, success bool, errMsg string) {
	if s.audit == nil {
		return
	}
	s.audit.RecordResultSafe(operatorID, action, "instance", fmt.Sprintf("%d", instanceID), detail, "", success, errMsg)
}

// 绑定写入已统一收敛到 commitVersionChange（与启动命令更新同事务，m-9）：
// 原先独立的 switchBinding 只剩「写绑定」一半，保留它会让「命令已改、绑定未改」
// 这条本已被修复的路径重新变得可能——故删除，绑定写入只有一处入口。
// 另注：升级与回滚的 Previous 写入口径本就相同（升级=旧 Current 成为回滚点；
// 回滚=当前成为新的回滚点），原先 switchBinding 里的 if/else 是一条空分支。

// planForAsset 为目标制品构造取件计划（升级/回滚共用）。
func (p *ProvisionService) planForAsset(asset *model.Asset, currentStartCommand string) (*binaryFetchPlan, error) {
	if p.artifactVersions == nil {
		return nil, errors.New("制品版本库服务未装配，无法执行版本变更")
	}
	token, err := p.artifactVersions.IssueBinaryDownloadToken(BinaryDownloadTokenScope{AssetID: asset.ID})
	if err != nil {
		return nil, fmt.Errorf("签发制品分发 token 失败: %w", err)
	}
	// 升级/回滚不依赖请求上下文，基址走平台公共基址设置。
	downloadURL, err := p.BuildBinaryDownloadURLForRequest(asset.ID, token, "")
	if err != nil {
		return nil, err
	}
	filename := beaconAssetFilename(asset)
	if !validBinaryFilename(filename) {
		return nil, fmt.Errorf("%w: asset#%d 无可用落盘文件名", ErrBinaryVersionTargetInvalid, asset.ID)
	}
	return &binaryFetchPlan{
		SourceKind:  string(BinarySourceURL),
		DownloadURL: downloadURL,
		SHA256:      strings.ToLower(strings.TrimSpace(asset.SHA256)),
		Filename:    filename,
		Executable:  true,
		AssetID:     asset.ID,
	}, nil
}

// binaryDirectionText 版本变更方向的中文文案（任务标题）。
func binaryDirectionText(direction string) string {
	if direction == "rollback" {
		return "回滚"
	}
	return "升级"
}

// binaryAssetLabel 制品的人类可读标签（任务标题用）。
func binaryAssetLabel(asset *model.Asset) string {
	version := strings.TrimSpace(asset.Version)
	if version == "" {
		version = fmt.Sprintf("asset#%d", asset.ID)
	}
	return version
}

// binaryVersionChangeTimeout 版本变更任务超时（与取件自限一致口径）。
const binaryVersionChangeTimeout = binaryFetchTimeout + 5*time.Minute
