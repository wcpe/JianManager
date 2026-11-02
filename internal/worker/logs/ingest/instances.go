package ingest

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/pipeline"
)

// InstanceBinding is durable acquisition metadata, not CP business state.
// It preserves holder/source identity when CP replays instance registration.
type InstanceBinding struct {
	UUID       string               `json:"uuid"`
	TargetID   string               `json:"target_id"`
	Generation string               `json:"generation"`
	Mode       pipeline.AcquireMode `json:"mode"`
	WorkDir    string               `json:"work_dir"`
	// RuleKey 是日志采集规则名（FR-502），空 = bukkit（向后兼容）。
	// 它参与 binding 相等性判定：规则变化即视为「绑定变更」，与路径变化同口径。
	// 但**是否重建源**另按解析后的生效载体判定（2026-10-15 复核，见
	// sameEffectiveLogLayout）：规则名换了而解析结果逐字相同时只刷新本字段。
	RuleKey string `json:"rule_key,omitempty"`
	// RelativePath 是实例级活动日志相对路径覆盖（FR-502），空 = 用规则默认。
	RelativePath string `json:"relative_path,omitempty"`
}

// InstanceRegistration 是 RegisterInstance 的入参（FR-502 起由位置参数改为结构体，
// 便于后续扩展且避免调用点参数错位）。
type InstanceRegistration struct {
	UUID         string
	TargetID     string
	Generation   string
	Mode         string
	WorkDir      string
	RuleKey      string // 日志采集规则名，空 = bukkit
	RelativePath string // 实例级日志相对路径覆盖，空 = 用规则默认
}

// ErrInstanceBindingPending 表示输出已安全写入暂存区，但实例日志绑定尚未到达。
var ErrInstanceBindingPending = errors.New("ingest: instance log binding pending")

type pendingFlushError struct {
	stream string
	err    error
}

func (e *pendingFlushError) Error() string { return fmt.Sprintf("%s: %v", e.stream, e.err) }
func (e *pendingFlushError) Unwrap() error { return e.err }

func (m *Manager) MissingInstanceBindings(instanceIDs []string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var missing []string
	for _, id := range instanceIDs {
		if _, ok := m.state.Instances[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

func validateWorkDir(workDir string) error {
	cleaned := filepath.Clean(workDir)
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return fmt.Errorf("ingest: refusing to collect logs from %q", workDir)
	}
	// 链接探测必须先于 EvalSymlinks：Windows 上 junction 不被 EvalSymlinks 解析——
	// 它在路径中会被原样返回（「解析后偏离原路径」判据假阴性），而含 junction 的更深
	// 路径会让 EvalSymlinks 直接报错、被误判为「尚未创建」而放行。junction 是 Windows 上
	// **无需特权**即可创建的链接形式（符号链接需 SeCreateSymbolicLinkPrivilege），
	// 打开动作同样会跟随它读到约定位置之外的文件，属同一判据面。
	if link, target, ok := firstReparseLink(cleaned); ok {
		return fmt.Errorf("ingest: refusing symlinked log work dir %q (link %q resolves to %q)", cleaned, link, target)
	}
	// 已存在的路径：解析符号链接后不得偏离原路径，否则说明中间某级是指向别处的链接。
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		return nil // 尚未创建：无链接可跟随，交由后续采集按需创建。
	}
	if resolved != cleaned {
		return fmt.Errorf("ingest: refusing symlinked log work dir %q (resolves to %q)", cleaned, resolved)
	}
	return nil
}

// firstReparseLink 返回 path 中第一个链接级（Unix 符号链接 / Windows 符号链接或 junction）
// 及其目标；无链接时 ok=false。
//
// 为什么不能只用 filepath.EvalSymlinks：Windows 上 junction 的 Lstat 报 ModeIrregular
// 而非 ModeSymlink，EvalSymlinks 因此既不解析它、也无法穿过它（见 validateWorkDir 注释）。
// os.Readlink 对 Unix 符号链接与 Windows 两种链接（含 junction）都返回目标，故用它统一探测。
func firstReparseLink(path string) (link, target string, ok bool) {
	volume := filepath.VolumeName(path)
	rest := path[len(volume):]
	current := volume
	if strings.HasPrefix(rest, string(filepath.Separator)) {
		current += string(filepath.Separator)
		rest = rest[len(string(filepath.Separator)):]
	}
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		// 非链接（普通文件/目录）与不存在的级：os.Readlink 均返回错误。
		if t, err := os.Readlink(current); err == nil {
			return current, t, true
		}
	}
	return "", "", false
}

func (m *Manager) RegisterInstance(reg InstanceRegistration) error {
	if m == nil {
		return fmt.Errorf("ingest: instance acquisition unavailable")
	}
	if reg.UUID == "" || !strings.HasPrefix(reg.TargetID, "inst:") || reg.Generation == "" || reg.WorkDir == "" {
		return fmt.Errorf("ingest: incomplete instance log binding")
	}
	if err := validateWorkDir(reg.WorkDir); err != nil {
		return err
	}
	binding := InstanceBinding{
		UUID:         reg.UUID,
		TargetID:     reg.TargetID,
		Generation:   reg.Generation,
		Mode:         pipeline.AcquireMode(reg.Mode),
		WorkDir:      filepath.Clean(reg.WorkDir),
		RuleKey:      reg.RuleKey,
		RelativePath: reg.RelativePath,
	}
	// FR-503：登记参数按当前生效采集策略收敛（三层语义见 policy.go）。
	// 只在**登记时**读取一次——策略变更不影响已在采实例（运行中切换路径会造成账本断裂）。
	policy := m.AcqPolicySnapshot()
	if !policy.RulesEnabled {
		// 规则引擎关闭 = 回退历史默认布局（bukkit）；空 RuleKey 正是「按历史行为」的表达。
		binding.RuleKey = ""
	}
	if override, ok := policy.PathOverrides[binding.TargetID]; ok {
		// 实例级路径覆盖优先于 CP 下发的相对路径与规则默认（手动覆盖档）。
		binding.RelativePath = override
	}
	// 安全约束（2026-10-19）：实例日志路径必须落在实例自己的 WorkDir 之内。
	// 在校验**生效后**的位置做，两条来源（CP proto 字段与心跳 path_overrides）一并覆盖。
	if err := validateInstanceRelativePath(binding.WorkDir, binding.RelativePath); err != nil {
		return err
	}
	if !binding.Mode.Valid() {
		return fmt.Errorf("ingest: invalid instance acquisition mode")
	}
	// 登记只取 registerMu，**不取 cycleMu**：cycleMu 被整轮采集/预备切换/停止长持有
	// （饱和期分钟级），取它会把「CP 启动实例」这一步拖过 RPC 截止时间。
	// 详见 Manager.registerMu 的注释与 docs/specs/log-ingest-register-lock/spec.md 的锁审计表。
	m.registerMu.Lock()
	defer m.registerMu.Unlock()
	// rollback 仅在「规则升级重建」路径上非 nil：它定格 detach 之前的旧状态（A6）。
	var rollback *ruleChangeRollback
	m.mu.Lock()
	existing, exists := m.state.Instances[reg.UUID]
	m.mu.Unlock()
	// 重启后没有绑定，但同名源已经存在：这是**索引纯内存**后的必然形态，不是异常。
	//
	// 2026-10-30 裁定：采集索引改 `stateindex.OpenInMemory`（机械盘不落盘）⇒ `m.state.Instances`
	// 重启后恒为空；而源本身由 `cursorRegistry`（带源配置的持久游标表）重建（见
	// `sourcesFromCursorRegistry`）。于是「CP 重启后首次登记」看到的形态是
	// 「`m.pipes[key]` 在、`m.state.Instances[uuid]` 不在」。
	//
	// 若不做下面这段，`exists=false` ⇒ 直接走完整登记分支 ⇒ `Register` 撞上已存在的 pipe
	// 且配置（新规则 ⇔ 新路径）不同 ⇒ `source identity already bound to another configuration`
	// ⇒ **重启后任何规则升级都必定失败**，CP 怎么重试都不会成功。
	//
	// 处理方式与「绑定在、但规则变了」全路：用**从游标表重建出来的配置**反向构出旧的生效载体，
	// 再按下面的同一套判据决定「只刷新元数据」还是「detach 后重建」。
	// 不得无条件下 detach：与既有配置逐字相同时，重建是纯代价（见 sameEffectiveLogLayout 的注释）。
	if !exists {
		if recovered, ok := m.bindingFromRebuiltSources(binding); ok {
			existing, exists = recovered, true
		}
	}
	if exists && existing != binding {
		// FR-502：区分「源位置变化」与「采集规则升级」。
		//
		// 前者（WorkDir/TargetID/Generation/Mode 变化）意味着日志源换了位置，
		// 必须走真正的源切换，不能靠幂等重登记蒙混——继续拒绝。
		//
		// 后者（仅 RuleKey/RelativePath 变化）是本项的**正常路径**：既有实例
		// 首次引入规则时，历史绑定没有规则名（空），而 CP 会下发具体规则；
		// 若一并拒绝，规则将永远无法在存量实例上生效。规则只决定**从哪个文件读**，
		// 不改变源身份（sourceID/StorageNamespace/generation 均不变），因此可安全地
		// 以新规则重新解析路径并重建源。
		if !sameSourceIdentity(existing, binding) {
			return fmt.Errorf("ingest: instance log binding changed without a source transition")
		}
		// 2026-10-15 复核：只有**解析后的生效载体**真的变了才重建源。
		//
		// 表达形式变化（rule_key 空 ↔ 恒下发的 "bukkit"、策略档位把生效值固化进绑定）
		// 而载体逐字相同时，重建是纯代价：清零该源账目（WAL/水位/投影/已验证代次）并从
		// 文件头重读既有内容（实测同一份 56 字节内容被读第二遍：read 56→112），
		// 而所有存量实例升级后首次登记都命中这一形态。
		if sameEffectiveLogLayout(existing, binding) {
			// 只把新记法写回绑定：源、管道与账本一概不动，随后仍走下方「已存在」分支
			// （接管暂存输出、按模式补 raw 文件），与幂等重登记完全同路。
			if err := m.rebindInstanceMetadata(binding); err != nil {
				return err
			}
		} else {
			// A3：重建前先确认旧源的「已读未投递」已清空。
			//
			// 摘除会把旧源的 WAL/水位/投影一并清掉，而 WAL 里可能还躺着**已读出但尚未确认投递**
			// 的事件（投递失败窗口 / 暂停 / 重试积压）。这些事件既不进缺口、也不告警，随状态清空
			// 直接消失 —— 静默丢数据，本仓最不能接受的那一类。
			//
			// 取舍：**宁可重建延后，不可静默丢**。检出残留即拒绝本次重建（旧源原样保留，由 CP
			// 下次重试或运维介入），并给出一条可判读的错误；不登记「不可自动消解的缺口」——
			// 那会让该源被永久停采（既有教训）。
			//
			// 为什么口径只取 WAL、不先 Flush 出悬挂的半条事件：未闭合多行缓冲是**每个源的稳态**
			// （最后一行永远在等下一个边界），把它也计入残留会让每一次规则升级都被永久拒绝——
			// 那正是「为了不丢而引入新的阻塞」，与本次修法的取舍相悖。
			//
			// 为什么不在这里「就地结算」：唯一可用的投递入口是 pipeline.Pipeline.DeliverPending，
			// 它会发起**真实 VL 投递**（含整窗重发分支与持久化门）。本函数在 registerMu 内，把
			// 登记 RPC 挂在网络与门外正是 2026-10-03 事故的同型形态；且该入口与兜底环共用
			// 「同源投递串行」前提，内联调用还要额外认领在飞表。因此改为把排空**非阻塞交办**给
			// 既有后台链（兜底环的投递欠账步 / #56 排空宿主都会驱动同一入口，二者都走有界执行器），
			// 本次立即返回明确错误：「交办排空 + 拒绝本次重建」不丢事件，也不是死结。
			if pending, pendingBytes, sourceKey := m.instanceRuleChangeUndelivered(existing); pending > 0 {
				if sourceKey == "" {
					sourceKey = existing.TargetID
				}
				m.schedulePendingReplay(sourceKey)
				slog.Warn("规则升级未执行：旧源尚有已读未投递事件（已交办后台排空，投递确认后重试即可）",
					"uuid", existing.UUID, "source", sourceKey, "pending", pending, "bytes", pendingBytes)
				return fmt.Errorf("ingest: 旧源 %s 尚有 %d 条已读未投递事件（约 %d 字节），"+
					"拒绝以新规则重建以免丢失（已交办后台排空，待投递确认后重试）",
					sourceKey, pending, pendingBytes)
			}
			// A6：摘除前定格旧绑定、旧源配置与旧账目镜像——重建失败时据此回滚。
			rollback = m.snapshotRuleChangeRollback(existing)
			if err := m.detachSourcesForRuleChange(existing); err != nil {
				return err
			}
			// 旧源已摘除：改走下面的完整登记分支，以新规则重建源与绑定。
			exists = false
		}
	}

	// 暂存接管与绑定变更必须共用同一把锁，保证注册期间的输出不会插入回放中间。
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	if exists {
		if err := m.flushPendingForBinding(binding); err != nil {
			m.recordRawWriteFailure(binding, pendingFlushStream(err), 0)
			return fmt.Errorf("ingest: adopt pending instance output: %w", err)
		}
		if binding.Mode == pipeline.ModeStdioPrimary {
			for _, stream := range []string{"stdout", "stderr"} {
				if err := m.ensureRawFile(m.rawInstancePath(binding, stream)); err != nil {
					m.recordRawWriteFailure(binding, stream, 0)
					return err
				}
			}
		}
		return nil
	}

	streams := []string{"stdout"}
	if binding.Mode == pipeline.ModeStdioPrimary {
		streams = append(streams, "stderr")
	}
	// INGEST-5 修复：收集已登记的源 key，错误路径必须一并回滚 pipes/sources/SourceConfigs，
	// 避免留下「源在采集、绑定不在」的半个登记中间态。
	var registeredKeys []string
	for _, stream := range streams {
		path := instanceLivePath(binding)
		sourceID := binding.TargetID + "/file"
		if binding.Mode == pipeline.ModeStdioPrimary {
			path = m.rawInstancePath(binding, stream)
			sourceID = binding.TargetID + "/" + stream
		}
		// 规则变更导致源重建时，从新文件末尾开始采集（FR-502）：
		// 新文件里可能含**已投递过的历史内容**（如同名文件被换掉后仍保留旧数据），
		// 从头重读会重复导入。既有 tailer 对「指纹不匹配」已有同款防护，此处沿用其语义。
		startAtEOF := m.takeStartAtEOF(sourceID + "/" + reg.Generation)
		if err := m.Register(SourceConfig{LogSourceID: sourceID, SourceGeneration: reg.Generation,
			Mode: binding.Mode, Path: path, Stream: stream, SourceCategory: logtypes.SourceInstance,
			StorageNamespace: binding.TargetID, ArchiveGlob: fileArchiveGlob(binding),
			StartAtEOF: startAtEOF}); err != nil {
			m.rollbackRegisterSources(registeredKeys)
			// A6：规则升级重建失败时，回滚**本次新建的源**只是第一步——旧绑定与旧源已被 detach
			// 摘掉，不回滚就会停在「无绑定、无源」的半个状态。
			m.restoreRuleChangeRollback(rollback)
			return err
		}
		registeredKeys = append(registeredKeys, sourceID+"/"+reg.Generation)
	}
	m.mu.Lock()
	if m.state.Instances == nil {
		m.state.Instances = make(map[string]InstanceBinding)
	}
	m.state.Instances[reg.UUID] = binding
	m.mu.Unlock()
	// 有界热路径（P0 复盘 2026-10-03 ✓）：**不得在持 registerMu 时无限等持久化门** ✗ ——
	// 原 `m.persist()` = persistPriority（无 maxWait ⇒ 门上无限等 ✓）⇒ 注册风暴下"每个注册者
	// 抱着 registerMu 无限等门"（SIGQUIT 成串 sema / 四把 wait_max 分钟级的机制本体 ✓✓）。
	// 语义自检（不变量 ✓）：**"注册返回"改由"内存状态已在 m.mu 内提交（终将落库）"保证** ✓；
	// 超时即交后台持久化通道（与采集轮同款热路径 ✓，失败由后台通道的错误面兜底 ✓），
	// 不再承诺"返回即已落盘" ✗ —— 该承诺属用户面路径（persistPriority/同步模式 ✓），登记侧不需要 ✗。
	if err := m.persistHot(); err != nil {
		m.rollbackRegisterSources(registeredKeys)
		m.mu.Lock()
		delete(m.state.Instances, reg.UUID)
		m.mu.Unlock()
		// A6：落库失败同样是「已 detach 却没能建起来」，旧源与旧绑定必须回来。
		m.restoreRuleChangeRollback(rollback)
		return err
	}
	if err := m.flushPendingForBinding(binding); err != nil {
		m.recordRawWriteFailure(binding, pendingFlushStream(err), 0)
		return fmt.Errorf("ingest: adopt pending instance output: %w", err)
	}
	if binding.Mode == pipeline.ModeStdioPrimary {
		for _, stream := range []string{"stdout", "stderr"} {
			if err := m.ensureRawFile(m.rawInstancePath(binding, stream)); err != nil {
				m.recordRawWriteFailure(binding, stream, 0)
				return err
			}
		}
	}
	return nil
}

// takeStartAtEOF 取出并清除「该源应从文件末尾开始采集」的一次性标记（FR-502）。
func (m *Manager) takeStartAtEOF(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startAtEOF == nil {
		return false
	}
	flag := m.startAtEOF[key]
	if flag {
		delete(m.startAtEOF, key)
	}
	return flag
}

// sameSourceIdentity 判断两个绑定是否指向**同一日志源身份**（FR-502）。
//
// 这四者共同决定源的物理位置与账本身份；它们不变时，源仍是同一个源，
// 仅采集规则（从哪个文件读）的差异可以安全地就地应用。
func sameSourceIdentity(a, b InstanceBinding) bool {
	return a.UUID == b.UUID &&
		a.TargetID == b.TargetID &&
		a.Generation == b.Generation &&
		a.Mode == b.Mode &&
		a.WorkDir == b.WorkDir
}

// sameEffectiveLogLayout 判断两个绑定解析出的**生效载体**是否一致（2026-10-15 复核新增）。
//
// 判据只比较「源究竟读哪个文件、发现哪些归档」这两项输出（instanceLivePath + fileArchiveGlob），
// 不比较 RuleKey/RelativePath 这些**表达形式**。为什么必须如此：
//   - 历史绑定的 rule_key 为空（FR-502 引入该列前入库，stateindex 迁移把该列回填为空串），而 CP 恒下发
//     非空规则名（默认 bukkit）；两者解析结果**逐字相同**（logs/latest.log + logs/*.gz），
//     按「结构体不等」判据却会走破坏性重建——所有存量实例升级后首次登记都会命中这一形态；
//   - 策略档位（rules_enabled 关闭 ⇒ 固化空规则名、path_overrides 命中 ⇒ 固化覆盖路径）
//     同样会把不同表达固化成同一路径。
//
// 代价不对称：等价时多走一次重建，会清零该源账目（WAL/水位/投影/已验证代次）并从文件头重读
// 既有内容（实测同一份 56 字节内容被读第二遍：read 56→112）；而少走一次重建，最坏只是绑定里
// 保留旧记法（下次登记再判一次，幂等）。故这里取「载体一致即等价」。
func sameEffectiveLogLayout(a, b InstanceBinding) bool {
	if a.Mode != b.Mode {
		return false
	}
	if a.Mode != pipeline.ModeFilePrimary {
		// 非 FILE_PRIMARY（stdio）的载体是 raw 文件，由 UUID+Generation 派生
		// （见 rawInstancePath），与规则名/相对路径无关 ⇒ 这两个字段怎么变都不换载体。
		return true
	}
	return instanceLivePath(a) == instanceLivePath(b) && fileArchiveGlob(a) == fileArchiveGlob(b)
}

// recalcInstanceSourceConfig 按**当前规则**重算实例源的物理路径与归档 glob（FR-504 启动恢复用）。
//
// 入参 source 来自账本（历史写入），其 Path/ArchiveGlob 是**当时代码**算出来的；而规则算法会演进
// （bungee 的归档 glob 由 `proxy.log.[0-9]*` 改为空即一例）。启动恢复若原样信任账本，算法修正
// 对**已登记源**就永不生效——真机实测：glob 修正上线并重启 Worker 后，索引 `source_aux.config`
// 里的旧 glob 一直留着，采集轮仍按旧 glob 去发现归档。
//
// 只在能按（源标识 + 代次 + 模式）唯一定位到绑定时重算；定位不到（如 worker.yml 的显式源、
// 或绑定已被摘除）一律原样返回，绝不猜测。
func (m *Manager) recalcInstanceSourceConfig(source SourceConfig) SourceConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, binding := range m.state.Instances {
		if binding.Generation != source.SourceGeneration || binding.Mode != source.Mode {
			continue
		}
		if binding.Mode == pipeline.ModeFilePrimary {
			if binding.TargetID+"/file" != source.LogSourceID {
				continue
			}
			source.Path = instanceLivePath(binding)
			source.ArchiveGlob = fileArchiveGlob(binding)
			return source
		}
		for _, stream := range []string{"stdout", "stderr"} {
			if binding.TargetID+"/"+stream != source.LogSourceID {
				continue
			}
			source.Path = m.rawInstancePath(binding, stream)
			source.ArchiveGlob = fileArchiveGlob(binding)
			return source
		}
	}
	// 重启后 `state.Instances` 恒为空（索引纯内存，2026-10-30 裁定）⇒ 上面的绑定查找找不到东西。
	// 那时改从持久化的 `Path` 反推规则并重算 glob：这是 FR-504「按当前规则重算归档范围」
	// 在重启路径上的**唯一**可用证据面。不这样做，规则表改完就是永久不生效。
	if glob, changed := archiveGlobFromPersistedLayout(source); changed {
		source.ArchiveGlob = glob
	}
	return source
}

// archiveGlobFromPersistedLayout 在**没有绑定可查**时，从持久化的源配置自身重算归档 glob（FR-504 的重启形态）。
//
// 为什么要它：`recalcInstanceSourceConfig` 靠 `m.state.Instances` 定位绑定，而索引已改纯内存
// （2026-10-30 裁定）⇒ 重启后 `state.Instances` 恒为空 ⇒ FR-504 的「按当前规则重算」
// **对已登记源永不生效**：规则表改完之后，旧 glob 会永久留在盘上（真机形态：采集轮继续按
// 旧 glob 去发现归档，修正相当于没上线）。
//
// 可用的事实只有一件，但足够：源的 `Path` 是持久化的，而每条内置规则的 `LiveRelative` 是它自己
// 独有的活动日志文件名。于是「Path 以哪条规则的 LiveRelative 结尾」就能唯一认定规则，
// 再从该规则重算 glob——**不需要记得规则名，也不需要绑定**。
//
// 定位不到（路径被手工改过、或与任何内置规则都不匹配）一律不改：那是运维/CP 的显式选择，
// 猜测只会把配好的东西改错。
func archiveGlobFromPersistedLayout(source SourceConfig) (string, bool) {
	if source.Mode != pipeline.ModeFilePrimary || source.Path == "" {
		return "", false
	}
	// 按 LiveRelative 的长度倒序找：`logs/latest.log` 与 `latest.log` 这类嵌套关系下，
	// 必须先试更长的那个，否则短的后缀会先命中、反推出错的工作目录。
	rules := make([]Rule, 0, len(builtinRules))
	for _, rule := range builtinRules {
		rules = append(rules, rule)
	}
	sort.SliceStable(rules, func(i, j int) bool {
		return len(rules[i].LiveRelative) > len(rules[j].LiveRelative)
	})
	for _, rule := range rules {
		liveRel := filepath.FromSlash(rule.LiveRelative)
		if !strings.HasSuffix(source.Path, string(filepath.Separator)+liveRel) {
			continue
		}
		workDir := strings.TrimSuffix(source.Path, string(filepath.Separator)+liveRel)
		want := ""
		if rule.ArchiveGlob != "" {
			want = filepath.Join(workDir, filepath.FromSlash(rule.ArchiveGlob))
		}
		if want == source.ArchiveGlob {
			return "", false // 已经是最新口径，不必改
		}
		return want, true
	}
	return "", false
}

// rebindInstanceMetadata 只刷新实例绑定的元数据（规则名/路径覆盖这类**表达**），
// 不动源、管道与账本——用于「等价规则升级」（2026-10-15 复核，见 sameEffectiveLogLayout）。
//
// **为什么落库失败不回滚内存**：源完全未动，绑定记法只是「重启后按哪种表达解析」的提示；
// 失败最坏是重启后按旧记法再判一次等价（幂等，不会造成重读或丢账目）。把它当错误回滚，
// 反而会把已经生效的记法丢掉、让下一次登记重复判决。
//
// 注意：源配置里**随算法演进的项**（如归档 glob）不在这里修——等价判据按同一算法比较，
// 等价即意味着这些项也相同；真会被旧算法值困住的是「账本里存着历史值」的**启动恢复**路径，
// 那条路径单独重算（见 recalcInstanceSourceConfig 与其在 New 中的调用）。
//
// 调用方须已持 registerMu（本函数只取 m.mu，锁序 registerMu → m.mu ✓）。
func (m *Manager) rebindInstanceMetadata(binding InstanceBinding) error {
	m.mu.Lock()
	if m.state.Instances == nil {
		m.state.Instances = make(map[string]InstanceBinding)
	}
	m.state.Instances[binding.UUID] = binding
	m.mu.Unlock()
	// 与登记分支同款的有界落库：注册风暴下不得抱着门无限等（超时即交后台通道）。
	return m.persistHot()
}

// detachSourcesForRuleChange 摘除某实例当前的源登记，为规则升级后的重建让路（FR-502）。
//
// **为什么必须连位置与投影一起清掉**：规则变化会改变**物理路径**
// （如空规则→bungee：logs/latest.log → proxy.log.0）。新文件与旧文件是**不同的内容流**，
// 若保留旧账本的位置与投影，采集器会用旧事件内容去对新文件做校验，导致
// 「projection content mismatch」并把整轮采集绊住（实测于真机）。
// 因此这里按「该源重新开始」处理：清掉 pipes/sources/SourceConfigs/位置/投影，
// 让调用方以新规则从新文件重新采集。
//
// 源身份（sourceID / storage namespace / generation）保持不变——下游仍视其为
// 同一逻辑实例的日志源；只是其物理载体换了，故从新载体的起点重新开始。
//
// 调用方须已持 registerMu；本函数自行取 pendingMu 与 m.mu（锁序 registerMu → pendingMu → m.mu ✓）。
func (m *Manager) detachSourcesForRuleChange(old InstanceBinding) error {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()

	m.mu.Lock()
	for _, key := range instanceSourceKeys(old) {
		delete(m.pipes, key)
		delete(m.sources, key)
		delete(m.state.SourceConfigs, key)
		// Sources 持有该源的账本（含位置水位与投影），必须一并清除：
		// 新物理文件的起点即新账本的起点，残留旧账本会让校验拿旧内容对新文件，
		// 报「projection content mismatch」并绊住整轮采集（实测于真机）。
		delete(m.state.Sources, key)
		delete(m.persistedRev, key)
		delete(m.persistedCover, key)
		// 标记「重建时从新文件末尾开始」（FR-502）：新文件可能含已投递过的历史内容，
		// 从头重读会重复导入（真机实测 30 倍重复）。语义与既有 tailer 的
		// reanchorAtEOFLocked 一致——禁止按旧偏移回读历史，缺的内容交由对账补齐。
		if m.startAtEOF == nil {
			m.startAtEOF = make(map[string]bool)
		}
		m.startAtEOF[key] = true
	}
	// 绑定一并摘除：调用方随后以新规则走完整登记分支并写回新绑定。
	delete(m.state.Instances, old.UUID)
	m.publishReconcileViewLocked(true)
	m.mu.Unlock()
	return nil
}

// instanceSourceKeys 返回某绑定对应的全部源键（FILE_PRIMARY 一条；STDIO_PRIMARY 两条流）。
//
// 键即 pipes / sources / SourceConfigs / 账目镜像四张表的统一键（`<logSourceID>/<generation>`）。
// 登记、摘除、残留探针与失败回滚必须按**同一口径**枚举——口径分叉就会留下「摘了源却留着配置」
// 这类半状态，而这类半状态在重启后才会显形。
func instanceSourceKeys(binding InstanceBinding) []string {
	if binding.Mode == pipeline.ModeStdioPrimary {
		return []string{
			binding.TargetID + "/stdout/" + binding.Generation,
			binding.TargetID + "/stderr/" + binding.Generation,
		}
	}
	return []string{binding.TargetID + "/file/" + binding.Generation}
}

// instanceUndeliveredResidue 统计某源「已读入 WAL、但尚未被投递水位覆盖」的事件数与字节数（A3）。
//
// 判据为什么取**投递水位**而不是回收水位：WAL 条目只在 `Durable && Record.End <= reclaim` 时才
// 被剪枝（见 acquire 的 pruneReclaimed），所以 WAL 里同时躺着两类条目——「已投递但尚未回收」
// 与「尚未投递」。只有后者会随账目清空而真正丢失。拿 reclaim 当判据会把前者误判成残留，
// 使重建被无谓地长期拒绝（回收水位受恢复责任链约束，可能落后很久）。
//
// 只读：仅读一次账本水位与一份 WAL 快照，不改任何状态；调用方须已持 registerMu
// （见 RegisterInstance 的规则升级分支）。
func instanceUndeliveredResidue(p *pipeline.Pipeline) (entries int, bytes int64) {
	if p == nil {
		return 0, 0
	}
	delivery := uint64(0)
	if pos, ok := p.Positions(); ok {
		delivery = pos.Delivery
	}
	for _, entry := range p.WAL().Snapshot() {
		if entry.Event.Record.End <= delivery {
			continue // 已被投递水位覆盖：只是还没被回收，不是未投递
		}
		entries++
		bytes += int64(len(entry.Event.Message))
	}
	return entries, bytes
}

// instanceRuleChangeUndelivered 汇总某绑定全部流的「已读未投递」残留，并给出用于交办排空的源键。
//
// 源键取**第一个有残留的流**：排空交办按源进行，stdio 两条流各自独立；退化为任一条即可
// （交办只是让后台链尽快驱动同一入口，真正的判据是上面的条数）。
func (m *Manager) instanceRuleChangeUndelivered(old InstanceBinding) (entries int, bytes int64, sourceKey string) {
	type streamPipe struct {
		key  string
		pipe *pipeline.Pipeline
	}
	keys := instanceSourceKeys(old)
	m.mu.Lock()
	streams := make([]streamPipe, 0, len(keys))
	for _, key := range keys {
		if p := m.pipes[key]; p != nil {
			streams = append(streams, streamPipe{key: key, pipe: p})
		}
	}
	m.mu.Unlock()
	for _, stream := range streams {
		e, b := instanceUndeliveredResidue(stream.pipe)
		entries += e
		bytes += b
		if e > 0 && sourceKey == "" {
			sourceKey = stream.key
		}
	}
	return entries, bytes, sourceKey
}

// bindingFromRebuiltSources 用「已在本进程重建的源配置」反向构出一个历史绑定（仅用于规则升级判定）。
//
// 背景：索引纯内存后重启拿不到 `state.Instances`，但源仍然活着（由游标表重建）。此刻若 CP 下发
// 了新规则，必须能从**现有源的真实路径**反推出「上一次用的是哪套布局」，才能判断
// 「只是规则名变了」还是「真的有载体切换」。
//
// 只从现有源逆推可得的三件事：TargetID（源 ID 的前缀）、Generation、Mode。
// WorkDir 直接取新登记的（它决定日志目录，且调用方已校验过）——载体判定只需要
// 「旧路径 vs 新路径」这一个事实，而旧路径在源配置里是现成的。
// RuleKey/RelativePath 留空，使与 `sameEffectiveLogLayout` 的对比回退到「按路径判」：
// 那正是重启后唯一可拿到的证据面。
func (m *Manager) bindingFromRebuiltSources(binding InstanceBinding) (InstanceBinding, bool) {
	keys := instanceSourceKeys(binding)
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for _, key := range keys {
		if _, ok := m.pipes[key]; ok {
			found = true
			break
		}
	}
	if !found {
		return InstanceBinding{}, false
	}
	// 源在、绑定不在 ⇒ 旧绑定只能靠源配置本身表达。把 RuleKey/RelativePath 留空，
	// 并把 WorkDir 定为新值：下游的 `sameEffectiveLogLayout` 会重新解析两边的生效路径
	// 并按「路径是否逐字相同」作答，与重启前是否有 RuleKey 无关。
	recovered := binding
	recovered.RuleKey = ""
	recovered.RelativePath = ""
	return recovered, true
}

// ruleChangeRollback 是「规则升级重建」失败时回到旧状态的快照（A6）。
type ruleChangeRollback struct {
	uuid     string
	binding  InstanceBinding
	configs  map[string]SourceConfig
	accounts map[string]persistedSource
}

// snapshotRuleChangeRollback 在 detach **之前**定格旧绑定、旧源配置与旧账目镜像（A6）。
//
// 为什么账目镜像也要定格：detach 会把 `m.state.Sources[key]` 一并删掉，而 m.Register 正是
// **从该镜像**还原水位与 WAL 的（见 Register 里 led.Restore / wal.Restore 的入参）。只放回源配置
// 而不放回镜像，等于把旧源当成「全新源」重建——它会从文件头重读整份既有日志并重复投递，
// 与真机「30 倍重复」是同一形态。
//
// 调用方须已持 registerMu（本函数只取 m.mu，锁序 registerMu → m.mu ✓）。
func (m *Manager) snapshotRuleChangeRollback(old InstanceBinding) *ruleChangeRollback {
	snap := &ruleChangeRollback{
		uuid:     old.UUID,
		binding:  old,
		configs:  make(map[string]SourceConfig),
		accounts: make(map[string]persistedSource),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range instanceSourceKeys(old) {
		// 配置优先取**索引口径**（m.state.SourceConfigs）：它是重启后真正被恢复的那一份，
		// 且只含显式字段（继承来的字符集/时区不写回索引，见 Register 的口径说明）。
		if cfg, ok := m.state.SourceConfigs[key]; ok {
			snap.configs[key] = cfg
		} else if cfg, ok := m.sources[key]; ok {
			snap.configs[key] = cfg
		}
		if saved, ok := m.state.Sources[key]; ok {
			snap.accounts[key] = saved
		}
	}
	return snap
}

// restoreRuleChangeRollback 回滚一次失败的规则升级重建：放回旧账目镜像与旧配置，按旧配置原样
// 重建旧源，最后恢复旧绑定（A6）。
//
// 为什么必须做：detach 已经把旧绑定与旧源摘掉，若不回滚，实例就停在「无绑定、无源」的半个状态
// ——日志完全断流，且因为绑定也不在了，只能等 CP 下一次真正的登记事件。
//
// 行为边界（如实说明，不夸大）：
//   - 恢复的是「源登记 + 旧绑定 + detach 之前的账目镜像」。镜像里的水位由持久化链推动，
//     **可能落后于 detach 那一刻的内存水位**，故恢复后可能重读一小段并重复投递（FR-497 允许
//     重复，投递面按 event_id 幂等）；但不会从文件头整份重读，也绝不丢账目。
//   - detach 之后、恢复之前写入内存的账目增量不在快照里 ⇒ 同样回退到镜像时点（安全方向：重复，不丢）。
//
// 调用方须已持 registerMu，且调用前必须已用 rollbackRegisterSources 清掉本次新建的源
// （本函数内部会调用 m.Register，锁序 registerMu → m.mu ✓）。
func (m *Manager) restoreRuleChangeRollback(snap *ruleChangeRollback) {
	if snap == nil {
		return
	}
	m.mu.Lock()
	if m.state.SourceConfigs == nil {
		m.state.SourceConfigs = make(map[string]SourceConfig)
	}
	if m.state.Sources == nil {
		m.state.Sources = make(map[string]persistedSource)
	}
	for key, cfg := range snap.configs {
		m.state.SourceConfigs[key] = cfg
	}
	for key, saved := range snap.accounts {
		m.state.Sources[key] = saved
	}
	// 绑定先摘掉，全部源都恢复成功后再放回（见下方"未全部恢复"的说明）。
	delete(m.state.Instances, snap.uuid)
	m.publishReconcileViewLocked(true)
	m.mu.Unlock()

	restored := true
	keys := make([]string, 0, len(snap.configs))
	for key := range snap.configs {
		keys = append(keys, key)
	}
	sort.Strings(keys) // 固定顺序：stdio 两条流也要按确定次序重建
	for _, key := range keys {
		cfg := snap.configs[key]
		if err := m.Register(cfg); err != nil {
			restored = false
			slog.Error("规则升级重建失败后的旧源恢复失败：该源暂不可采，等 CP 重新登记",
				"uuid", snap.uuid, "logSourceID", cfg.LogSourceID,
				"generation", cfg.SourceGeneration, "error", err)
		}
	}
	if !restored {
		// 刻意**不**恢复绑定：绑定一旦在位，CP 的下一次同名登记会走「绑定未变即幂等返回」的
		// 快路径，而那条路径不会重建源 ⇒ 该实例会被永久钉在「有绑定、无源」上。留空绑定让下次
		// 登记走完整重建分支，是这里唯一能自愈的形态。
		slog.Error("旧源未全部恢复：本次不恢复实例绑定，让 CP 下次登记走完整重建路径（幂等快路径会跳过建源）",
			"uuid", snap.uuid, "target", snap.binding.TargetID, "ruleKey", snap.binding.RuleKey)
		return
	}
	m.mu.Lock()
	if m.state.Instances == nil {
		m.state.Instances = make(map[string]InstanceBinding)
	}
	m.state.Instances[snap.uuid] = snap.binding
	m.publishReconcileViewLocked(true)
	m.mu.Unlock()
}

// 调用方须已持 registerMu + pendingMu（本函数只取 m.mu，锁序 registerMu → pendingMu → m.mu ✓）。
func (m *Manager) rollbackRegisterSources(keys []string) {
	if len(keys) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		delete(m.pipes, key)
		delete(m.sources, key)
		delete(m.state.SourceConfigs, key)
		delete(m.persistedRev, key)
		delete(m.persistedCover, key)
	}
	m.publishReconcileViewLocked(true) // 源集合变更：立即发布对账只读视图
}

// validateInstanceRelativePath 校验实例级日志路径（安全约束，2026-10-19；链接兜底 2026-10-22）。
//
// 口径：**必须是相对实例 WorkDir 的路径**，且折叠 `..`、解析链接之后都不得逃出 WorkDir。
//
// 为什么不再允许绝对路径：`log.acq.path_overrides` 由 CP 经心跳下发，语义是「该实例的日志
// 文件在它自己工作目录里的哪一处」。放行绝对路径时，持有 `settings.write` 的人可以把任意
// 实例的日志源指向宿主机任意可读文件（`/etc/shadow` 之类），Worker 会照读并写入 VL——
// CP 与 Worker 分属不同信任域，Worker 不应无条件信任下发来的物理路径。项目内同类守卫的
// 既有口径见 service/storage.go（拒绝绝对路径 + 二次 Rel 确认不逃出根）。
//
// 为什么还要解析链接（字符串判据的**物理层**缺口）：字符串只能证明「字面路径」在目录内，
// 证明不了「打开动作实际落到哪」。采集侧打开文件是裸 `os.Open`（acquire/tailer.go 与
// archive.go），既不带 O_NOFOLLOW、也不先 EvalSymlinks ⇒ 打开动作会跟随链接，工作目录里
// 一个 `logs/latest.log -> /etc/shadow` 就能让字符串判据全部通过而物理读越界。这与
// validateWorkDir 拒绝软链工作目录是**同一判据面**（那里的注释已写明「打开动作同样会跟随
// 它读到约定位置之外的文件」），此前只堵了工作目录本身、没堵目录内的路径，属同一不变量
// 的缺口。注意规则默认路径（bukkit/waterfall/velocity 的 `logs/latest.log`）本就落在这一面
// 上：**不需要任何 path_overrides**，只要有人能往工作目录里放一个链接就成立，因此不能因为
// 「覆盖项只由 settings.write 掌管」就降低本判据的必要性。
//
// 空串合法：表示「用规则默认」（Lookup(RuleKey).LiveRelative，内置规则均为相对路径）。
//
// 判据分三层，缺一层就有一种绕过面：①**宿主语义**字符串层（本机 filepath，GOOS 绑定）；
// ②**跨方言**字符串层（POSIX 与 Windows 各判一次，见 pathDialect——补的是「Linux 上判不出
// Windows 形态」这一可测性缺口，也是 CP 与节点结论一致性的保证）；③**物理落点**层
// （validateNoLinkEscape，补的是链接跟随）。
//
// 三层**都是拒绝条件、取并集**：任一命中即拒绝，都不命中才放行。因此新增后两道绝不会放松
// 第一道（原 filepath 判据逐字保留）。①② 是纯字符串判定、不碰文件系统，故 workDir 为空也
// 能判；③ 需要比对基准，只在 workDir 非空时执行。
func validateInstanceRelativePath(workDir, rel string) error {
	trimmed := strings.TrimSpace(rel)
	if trimmed == "" {
		return nil
	}
	// 第一道：宿主语义（原有判据，逐字保留）。这是本机真实生效的那一道，
	// 不得因下面的跨方言判据而弱化或删除。
	if filepath.IsAbs(filepath.FromSlash(trimmed)) {
		return fmt.Errorf("ingest: 实例日志路径须为相对实例工作目录的路径，不接受绝对路径：%q", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(trimmed))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("ingest: 实例日志路径不得逃出实例工作目录：%q", rel)
	}
	// 第二道：与宿主无关的跨方言判据（2026-10-21 补）。
	//
	// 为什么必须有这一道：上面那道只看得见**编译期 GOOS**。同一份下发的覆盖值，
	// Linux 节点上 `C:\logs\a.log`、`..\..\etc\passwd` 会被 `filepath` 判成「合法相对路径」
	// （反斜杠在 POSIX 里只是普通字符），到了 Windows 节点上却被判绝对/逃逸而拒绝登记，
	// 表现为「CP 保存成功、实例日志登记失败」或反过来的静默不采。判据若只跟宿主走，
	// 两端结论就永远可能不一致，而这条路恰恰无法在本机取证（见下方 pathDialect 注释）。
	//
	// 方向性：这里对两套方言各判一次、**任一**判为绝对/带卷名/逃逸即拒绝，即只会比原
	// 判据更严（并集），任何时候都不会放松逃逸防护。代价是 Linux 节点上形如
	// `C:foo.log` 或 `logs\..\..\x` 的字面文件名也被拒——这类名字在 Windows 上根本
	// 无法存在，跨平台不可移植，拒绝比「一半平台能跑」更可预期。
	//
	// 位置说明：必须排在下面的 workDir 空分支**之前**——本层是纯字符串判定，没有比对
	// 基准也能得出结论；若放到链接兜底之后，workDir 为空的调用点就会整层跳过。
	if isAbsWindows(trimmed) {
		return fmt.Errorf("ingest: 实例日志路径须为相对实例工作目录的路径，不接受 Windows 绝对路径（盘符或 UNC）：%q", rel)
	}
	if hasWindowsVolume(trimmed) {
		return fmt.Errorf("ingest: 实例日志路径不得带 Windows 盘符或 UNC 卷名：%q", rel)
	}
	for _, d := range []pathDialect{dialectPOSIX, dialectWindows} {
		if escapesRoot(trimmed, d) {
			return fmt.Errorf("ingest: 实例日志路径不得逃出实例工作目录（按 %s 分隔符语义）：%q", d.name, rel)
		}
	}
	// 第三道：物理落点（2026-10-22 补）。前两道都是字符串判据，只能证明「字面路径」在目录内；
	// 这一道才证明「打开动作落到哪」——采集侧是裸 os.Open（acquire/tailer.go、archive.go），
	// 会跟随链接，缺了这一道就等于「字符串合法、物理越界」仍可读宿主机任意文件。
	// workDir 为空时没有比对基准，只能由调用点保证（登记路径必带 WorkDir，见
	// RegisterInstance 的入参校验），故此处跳过而不是拒绝。
	if strings.TrimSpace(workDir) == "" {
		return nil
	}
	// 用**宿主语义** Clean 出的 clean 去查落点：那正是 os.Open 实际会打开的字面路径。
	return validateNoLinkEscape(filepath.Clean(workDir), clean)
}

// validateNoLinkEscape 补齐「物理落点仍在 root 内」这一判据（rel 已过字符串层校验）。
//
// 为什么先探链接再决定要不要解析：绝大多数实例路径上根本没有链接，firstReparseLink 不命中时
// 只花几个 Readlink 系统调用即可放行，不必让常规情形去付 EvalSymlinks 的代价（本函数经
// instanceLivePath 出现在生效布局比较路径上，不是热路径但也不该做多余 I/O）。
func validateNoLinkEscape(root, rel string) error {
	abs := filepath.Join(root, rel)
	// 整条链上没有链接级（Unix 符号链接 / Windows 符号链接或 junction）⇒ 打开动作不可能
	// 离开字面路径，字符串层判据已够。firstReparseLink 对不存在的级只返回错误、不中断扫描，
	// 故「路径尚未创建」与「无链接」在这条快路径上等价：两者都无链接可跟随。
	link, target, hasLink := firstReparseLink(abs)
	if !hasLink {
		return nil
	}
	// 最近的**已存在**祖先：日志文件通常尚未创建，直接对整条路径 EvalSymlinks 会因 ENOENT
	// 被误判成「解析失败」而把合法路径拦下来。链接只可能存在于已存在的级上，故从落点向上
	// 找到第一个存在的级，即可覆盖全部可跟随的链接（不存在的级不可能有子级）。
	probe := abs
	for {
		if _, statErr := os.Lstat(probe); statErr == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	resolvedProbe, err := filepath.EvalSymlinks(probe)
	if err != nil {
		// 确有链接却解析不出落点（悬空链接、链接环，或含 junction 的更深路径在 Windows 上
		// 直接报错）⇒ 无法自证仍在目录内，拒绝。宁可让运维把链接换成真实目录，也不放行
		// 一个 Worker 自己都算不出落点的路径。
		return fmt.Errorf("ingest: 实例日志路径含无法解析的链接 %q（%q → %q）：%w", rel, link, target, err)
	}
	if resolvedProbe == probe {
		// 确有链接但 EvalSymlinks 原样返回：Windows junction 的已知行为——junction 的 Lstat
		// 报 ModeIrregular 而非 ModeSymlink，EvalSymlinks 既不解析它、也穿不过它（见
		// validateWorkDir 注释）。落点无法确定 ⇒ 拒绝，与那里的口径保持逐字一致。
		return fmt.Errorf("ingest: 实例日志路径含无法解析的链接 %q（%q → %q；Windows junction 不被 EvalSymlinks 解析）", rel, link, target)
	}
	// 工作目录**也**要解析后再比对：否则 `/var -> /private/var` 这类平台自身的链接（macOS
	// 常见，t.TempDir 就在其下）会让两侧基准不同，把完全合法的路径全判成越界。
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		// 工作目录不存在：登记时 validateWorkDir 已拒绝软链工作目录，此处只剩「尚未创建」
		// 这一种合理情形 ⇒ 其下不可能存在可跟随的链接，放行（与 validateWorkDir 同口径）。
		return nil
	}
	// 二次 Rel 确认不逃出根——与 service/storage.go 的 safeAbs 同一判据写法。
	relBack, err := filepath.Rel(resolvedRoot, resolvedProbe)
	if err != nil || relBack == ".." || strings.HasPrefix(relBack, ".."+string(filepath.Separator)) {
		return fmt.Errorf("ingest: 实例日志路径经链接解析后逃出实例工作目录：%q（%q → %q）", rel, link, resolvedProbe)
	}
	return nil
}

// pathDialect 描述一套路径方言：分隔符集合 + 是否按 Windows 规则识别卷名。
//
// 为什么要把语义降级成「数据」而不是直接用 filepath：Go 的 path/filepath 在**编译期**
// 按 GOOS 绑定实现，Linux 上跑 `go test` 拿到的永远是 POSIX 语义——`filepath.Clean` 会把
// 反斜杠当普通字符，`..\..\etc\passwd` 因此被判「没有逃逸」。而生产上 Worker 可能落
// Windows 节点，「Windows 下绝对路径是否被拒、`..\` 能否逃出工作目录」恰恰是最该有
// 回归证据的一侧。把方言抽成数据后，同一份用例可在任一宿主上跑两种语义，Windows 判据
// 不再只能靠真机取证。
type pathDialect struct {
	name    string // 仅用于报错文案，便于定位是哪套语义判红的
	seps    string // 该方言视作路径分隔符的字节（Windows 同时认 `\` 与 `/`）
	windows bool   // 是否按 Windows 规则识别卷名（盘符 / UNC / 设备前缀）
}

var (
	dialectPOSIX   = pathDialect{name: "POSIX", seps: "/"}
	dialectWindows = pathDialect{name: "Windows", seps: `/\`, windows: true}
)

// isSep 判定字节在本方言下是否为路径分隔符。
func (d pathDialect) isSep(c byte) bool { return strings.IndexByte(d.seps, c) >= 0 }

// isAbsWindows 按 Windows 语义判定绝对路径（纯字符串判定，不依赖宿主 GOOS）。
//
// 判据对齐 GOOS=windows 下的 filepath.IsAbs（见 Go 的 internal/filepathlite/path_windows.go）：
//   - `C:\logs\a.log`、`C:/logs/a.log` → 绝对（盘符 + 分隔符）
//   - `C:`、`C:relative`              → **不是**绝对路径（盘符相对），但带卷名，见 hasWindowsVolume
//   - `\\server\share\x`、`//server/share/x` → 绝对（前两字节皆为分隔符 = UNC）
//   - `\logs\a.log`                   → 不是绝对路径（Windows 的 IsAbs 对根相对路径返回 false）
//
// 唯一比 Windows 更严的例外：卷名成分为 `..` 的畸形串（如 `\\..\x`）在 Go 里卷名长度为 0、
// IsAbs 为 false，这里判绝对。畸形输入只会因此更早被拒，方向安全。
func isAbsWindows(p string) bool {
	if p == "" {
		return false
	}
	if len(p) >= 2 && dialectWindows.isSep(p[0]) && dialectWindows.isSep(p[1]) {
		return true // UNC 或设备路径
	}
	if len(p) >= 2 && p[1] == ':' {
		return len(p) > 2 && dialectWindows.isSep(p[2])
	}
	return false
}

// hasWindowsVolume 判定路径是否带 Windows 卷名（盘符或 UNC/设备前缀）。
//
// 为什么把「带卷名」与「是绝对路径」分开判：`C:relative`、`C:` 是**带卷名但不是绝对路径**
// 的形态（相对当前盘的工作目录），Windows 的 IsAbs 对它们返回 false。可它们在任何情况下
// 都不表达「实例工作目录内的一处」：CP 侧 isWindowsAbsPath 早已按 `X:` 前缀整体拒绝，
// Worker 若放行就与 CP 判据不一致，用户会在 Windows 节点上看到「保存成功但实例日志
// 登记失败」。这里对齐 CP，按前缀一律拒绝。
//
// 注：盘符位置不校验字母范围（与 Go 的 volumeNameLen 一致），非 ASCII 的多字节盘符在此
// 只会被判为非卷名——那类输入仍会被下面的 POSIX/Windows 逃逸判据各过一遍。
func hasWindowsVolume(p string) bool {
	if len(p) >= 2 && p[1] == ':' {
		return true
	}
	return len(p) >= 2 && dialectWindows.isSep(p[0]) && dialectWindows.isSep(p[1])
}

// escapesRoot 按方言判断相对路径折叠 `.`/`..` 后是否越过根（即逃出实例工作目录）。
//
// 为什么手写段游走而不再用 filepath.Clean：Clean 的语义绑定 GOOS，Linux 上
// `..\..\etc\passwd` 里的反斜杠不参与分隔，Clean 后不带 `..\` 前缀，于是**放行**——
// 而 Windows 节点上同一串 Clean 成 `..\..\etc\passwd`（前缀 `..\`）必须拒绝。
// 段游走只把「分隔符集合」当参数，两种方言在任一宿主上都能判。
//
// 与 `filepath.Clean` + `..` 前缀比较**等价**（对相对路径而言）：深度一旦下穿到负即逃逸。
// 故 `..`、`..\..\x`、`logs\..\..\x`、`../../etc/passwd` 均拒绝；`logs/../a.log`、
// `logs\latest.log` 照常放行（后者的反斜杠在 Windows 上是合法分隔符，不得误伤）。
func escapesRoot(p string, d pathDialect) bool {
	depth := 0
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return strings.ContainsRune(d.seps, r) }) {
		switch seg {
		case ".": // 当前目录不改变深度
		case "..":
			depth--
			if depth < 0 {
				return true
			}
		default:
			depth++
		}
	}
	return false
}

// instanceLivePath 按绑定解析实例活动日志的绝对路径（FR-502）。
//
// 解析优先级（高 → 低）：
//  1. binding.RelativePath —— 实例级显式覆盖（来自 CP 或 log_sources 接线）
//  2. binding.RuleKey 对应规则 —— bukkit/bungee/waterfall/velocity
//  3. 空 RuleKey 回退 bukkit —— 旧 CP 兼容，行为与历史逐字一致
//
// 路径一律相对 WorkDir 解析；登记时已经 validateInstanceRelativePath 校验过，
// 这里再兜一层——历史持久化的旧行可能带着未校验的值，绕过登记的构造也不应把采集
// 指向工作目录之外。兜底策略是回退规则默认（仍在 WorkDir 内），而不是返回空路径：
// 后者会让实例静默停采，把配置问题伪装成「没有日志」。
//
// 注意：本函数只影响**物理路径发现**，不改变源标识（sourceID/StorageNamespace）。
func instanceLivePath(binding InstanceBinding) string {
	rel := binding.RelativePath
	if rel == "" || validateInstanceRelativePath(binding.WorkDir, rel) != nil {
		rel = Lookup(binding.RuleKey).LiveRelative
	}
	return filepath.Join(binding.WorkDir, filepath.FromSlash(rel))
}

// fileArchiveGlob 按绑定返回归档 glob（FR-502）。
//
// 历史实现固定返回 live 文件同目录的 "*.gz"，这对 Bukkit 系成立，但按核心类型区分后必须
// 由规则表决定：BungeeCord 的轮转产物是 proxy.log.N（数字后缀、**纯文本**，gzip-only 导入器
// 接不了），规则表已为该核型显式给出 `proxy.log.[1-9]*`（见 rules.go 的 bungee 条目）。
//
// 实例级路径覆盖时**也要先看规则表**（2026-10-21 修复）：修前该分支硬编码返回同目录 *.gz，
// 而显式 glob 在 discoverSourceArchives 里是逐字使用的（不放宽），于是「把 live 覆盖到
// proxy.log.0」的实例其 .1/.2 轮转**永远不可达**——默认发现逻辑已补齐数值轮转，覆盖分支
// 却把它绕过去了。故改为：有规则就给规则的，没有才回落历史口径 *.gz。
func fileArchiveGlob(binding InstanceBinding) string {
	if binding.Mode != pipeline.ModeFilePrimary {
		return ""
	}
	if binding.RelativePath == "" {
		if g := Lookup(binding.RuleKey).ArchiveGlob; g != "" {
			return filepath.Join(binding.WorkDir, filepath.FromSlash(g))
		}
		return ""
	}
	// 实例级覆盖：规则表优先，无规则才沿用历史口径。
	//
	// 基准必须用 **WorkDir** 而不是 live 所在目录：规则表里的 ArchiveGlob 是「相对工作目录」
	// 的 glob（如 `logs/*.gz`、`proxy.log.[1-9]*`），拼到 live 目录会得到 `logs/logs/*.gz`
	// 这类不存在的路径。（此处与未覆盖分支同源，仅 override 的 live 位置不同。）
	if g := Lookup(binding.RuleKey).ArchiveGlob; g != "" {
		return filepath.Join(binding.WorkDir, filepath.FromSlash(g))
	}
	return filepath.Join(filepath.Dir(instanceLivePath(binding)), "*.gz")
}

func (m *Manager) rawInstancePath(binding InstanceBinding, stream string) string {
	identity := sha256.Sum256([]byte(binding.UUID + "\x00" + binding.Generation))
	return filepath.Join(m.logScratch, "raw", fmt.Sprintf("%x", identity), stream+".log")
}

func (m *Manager) pendingInstancePath(uuid, stream string) string {
	identity := sha256.Sum256([]byte(uuid))
	return filepath.Join(m.logScratch, pendingSpoolRoot, fmt.Sprintf("%x", identity), stream+".spool")
}

func (m *Manager) ensureRawFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (m *Manager) appendPendingInstanceOutput(uuid, stream string, data []byte) error {
	m.pendingMu.Lock()
	defer m.pendingMu.Unlock()
	return m.appendPendingInstanceOutputLocked(uuid, stream, data)
}

// Pending 暂存上限（M-6）。未绑定实例的输出在绑定到达前无 pipeline/ledger 兜底，
// 若不加界，持续 stdout/stderr 可耗尽 Worker 数据盘，进而拖垮 ingest.state.json、
// 事件段、WAL 与 VL 运行时。上限取值需容纳正常的重启接管窗口（CP 不可达时可能持续
// 数分钟），同时远低于单盘容量。
const (
	// pendingMaxStreamBytes 单实例单流上限。
	pendingMaxStreamBytes = 64 << 20
	// pendingMaxTotalBytes 全部 pending 暂存总量上限。
	pendingMaxTotalBytes = 512 << 20
)

// pendingSpoolUsage 统计 pending 暂存目录的总字节数与每个文件的大小。
//
// 不缓存结果：spool 会在绑定时被 rename 或删除（见 flushPendingForBinding），
// 缓存会让限额在接管后残留并误伤正常采集。
func (m *Manager) pendingSpoolUsage() (total int64, perFile map[string]int64, err error) {
	root := filepath.Join(m.logScratch, pendingSpoolRoot)
	perFile = map[string]int64{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return 0, perFile, nil
	}
	if err != nil {
		return 0, nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, readErr := os.ReadDir(filepath.Join(root, entry.Name()))
		if readErr != nil {
			return 0, nil, readErr
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".spool") {
				continue
			}
			info, statErr := file.Info()
			if statErr != nil {
				return 0, nil, statErr
			}
			size := info.Size()
			perFile[entry.Name()+"/"+file.Name()] = size
			total += size
		}
	}
	return total, perFile, nil
}

func (m *Manager) appendPendingInstanceOutputLocked(uuid, stream string, data []byte) error {
	// 与托管 Raw 写入同源的门禁：pending 也必须受磁盘阈值约束，否则它成为绕过
	// 暂停阈值的写放大路径（M-6）。
	if m.capacityProvider != nil {
		budget, err := m.capacityProvider()
		if err != nil {
			return err
		}
		pause := budget.PauseAtPercent
		if pause <= 0 {
			pause = 90
		}
		if budget.DiskUsagePercent >= pause {
			return fmt.Errorf("ingest: pending spool disk usage reached pause threshold")
		}
	}
	path := m.pendingInstancePath(uuid, stream)
	rel := filepath.Base(filepath.Dir(path)) + "/" + filepath.Base(path)
	maxStream := m.pendingMaxStream
	if maxStream <= 0 {
		maxStream = pendingMaxStreamBytes
	}
	maxTotal := m.pendingMaxTotal
	if maxTotal <= 0 {
		maxTotal = pendingMaxTotalBytes
	}
	total, perFile, err := m.pendingSpoolUsage()
	if err != nil {
		return fmt.Errorf("ingest: pending spool usage: %w", err)
	}
	if perFile[rel]+int64(len(data)) > maxStream {
		return fmt.Errorf("ingest: pending spool exceeds per-stream limit for %s/%s", uuid, stream)
	}
	if total+int64(len(data)) > maxTotal {
		return fmt.Errorf("ingest: pending spool exceeds total limit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("ingest: pending spool directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("ingest: pending spool open: %w", err)
	}
	n, writeErr := f.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return fmt.Errorf("ingest: pending spool write: %w", writeErr)
	}
	return nil
}

func (m *Manager) flushPendingForBinding(binding InstanceBinding) error {
	for _, stream := range []string{"stdout", "stderr"} {
		fail := func(err error) error { return &pendingFlushError{stream: stream, err: err} }
		spoolPath := m.pendingInstancePath(binding.UUID, stream)
		if _, err := os.Stat(spoolPath); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return fail(fmt.Errorf("ingest: pending spool stat: %w", err))
		}
		rawPath := m.rawInstancePath(binding, stream)
		if _, err := os.Stat(rawPath); os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(rawPath), 0o700); err != nil {
				return fail(err)
			}
			if err := os.Rename(spoolPath, rawPath); err != nil {
				return fail(fmt.Errorf("ingest: adopt pending spool: %w", err))
			}
			continue
		} else if err != nil {
			return fail(fmt.Errorf("ingest: raw stat: %w", err))
		}
		spool, err := os.Open(spoolPath)
		if err != nil {
			return fail(fmt.Errorf("ingest: pending spool open: %w", err))
		}
		raw, err := os.OpenFile(rawPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			_ = spool.Close()
			return fail(fmt.Errorf("ingest: raw open: %w", err))
		}
		_, copyErr := io.Copy(raw, spool)
		if copyErr == nil {
			copyErr = raw.Sync()
		}
		if closeErr := raw.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if closeErr := spool.Close(); copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			return fail(fmt.Errorf("ingest: pending spool flush: %w", copyErr))
		}
		if err := os.Remove(spoolPath); err != nil {
			return fail(fmt.Errorf("ingest: pending spool cleanup: %w", err))
		}
	}
	return nil
}

func pendingFlushStream(err error) string {
	var flushErr *pendingFlushError
	if errors.As(err, &flushErr) && flushErr.stream != "" {
		return flushErr.stream
	}
	return "stdout"
}

func (m *Manager) recordRawWriteFailure(binding InstanceBinding, stream string, size int) {
	if binding.Mode != pipeline.ModeStdioPrimary {
		return
	}
	key := binding.TargetID + "/" + stream + "/" + binding.Generation
	m.mu.Lock()
	pipe := m.pipes[key]
	m.mu.Unlock()
	if pipe == nil {
		return
	}
	entry := pipe.Ledger().Get(pipe.Key())
	if entry == nil {
		return
	}
	_ = pipe.Ledger().RecordGap(pipe.Key(), entry.Positions.Read, entry.Positions.Read, ledger.GapReasonStdioRawWriteFailed, fmt.Sprintf("%d bytes require operator verification", size))
	_ = pipe.Ledger().PauseAcquire(pipe.Key(), "managed Raw write failed; manual recovery required")
	_ = m.persist()
}

func (m *Manager) appendRawInstanceOutputLocked(binding InstanceBinding, stream string, data []byte) error {
	if m.capacityProvider != nil {
		budget, err := m.capacityProvider()
		if err != nil {
			return err
		}
		pause := budget.PauseAtPercent
		if pause <= 0 {
			pause = 90
		}
		if budget.DiskUsagePercent >= pause {
			return fmt.Errorf("ingest: managed Raw disk usage reached pause threshold")
		}
	}
	f, err := os.OpenFile(m.rawInstancePath(binding, stream), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// AppendInstanceOutput 先持久化原始字节，再交给常驻采集循环消费。
// FILE_PRIMARY 的 stdout/stderr 仍只用于控制台，避免重复采集。
func (m *Manager) AppendInstanceOutput(uuid, stream string, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if stream != "stdout" && stream != "stderr" {
		return fmt.Errorf("ingest: unknown output stream")
	}
	m.mu.Lock()
	binding, ok := m.state.Instances[uuid]
	m.mu.Unlock()

	m.pendingMu.Lock()
	if !ok {
		// 注册可能在首次检查后完成，必须在暂存锁内重新读取绑定，避免把新输出留在
		// 已经完成回放的 spool 中。
		m.mu.Lock()
		binding, ok = m.state.Instances[uuid]
		m.mu.Unlock()
	}
	if !ok {
		writeErr := m.appendPendingInstanceOutputLocked(uuid, stream, data)
		m.pendingMu.Unlock()
		if writeErr != nil {
			return writeErr
		}
		return fmt.Errorf("%w: instance %s", ErrInstanceBindingPending, uuid)
	}
	if binding.Mode == pipeline.ModeFilePrimary {
		m.pendingMu.Unlock()
		return nil
	}
	writeErr := m.appendRawInstanceOutputLocked(binding, stream, data)
	m.pendingMu.Unlock()
	if writeErr != nil {
		m.recordRawWriteFailure(binding, stream, len(data))
	}
	return writeErr
}
