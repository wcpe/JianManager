package ingest

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	if !binding.Mode.Valid() {
		return fmt.Errorf("ingest: invalid instance acquisition mode")
	}
	// 登记只取 registerMu，**不取 cycleMu**：cycleMu 被整轮采集/预备切换/停止长持有
	// （饱和期分钟级），取它会把「CP 启动实例」这一步拖过 RPC 截止时间。
	// 详见 Manager.registerMu 的注释与 docs/specs/log-ingest-register-lock/spec.md 的锁审计表。
	m.registerMu.Lock()
	defer m.registerMu.Unlock()
	m.mu.Lock()
	existing, exists := m.state.Instances[reg.UUID]
	m.mu.Unlock()
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
		if err := m.detachSourcesForRuleChange(existing); err != nil {
			return err
		}
		// 旧源已摘除：改走下面的完整登记分支，以新规则重建源与绑定。
		exists = false
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

	streams := []string{"stdout"}
	if old.Mode == pipeline.ModeStdioPrimary {
		streams = append(streams, "stderr")
	}

	m.mu.Lock()
	for _, stream := range streams {
		sourceID := old.TargetID + "/file"
		if old.Mode == pipeline.ModeStdioPrimary {
			sourceID = old.TargetID + "/" + stream
		}
		key := sourceID + "/" + old.Generation
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

// instanceLivePath 按绑定解析实例活动日志的绝对路径（FR-502）。
//
// 解析优先级（高 → 低）：
//  1. binding.RelativePath —— 实例级显式覆盖（来自 CP 或 log_sources 接线）
//  2. binding.RuleKey 对应规则 —— bukkit/bungee/waterfall/velocity
//  3. 空 RuleKey 回退 bukkit —— 旧 CP 兼容，行为与历史逐字一致
//
// RelativePath 若已是绝对路径则原样使用；否则相对 WorkDir 解析。
//
// 注意：本函数只影响**物理路径发现**，不改变源标识（sourceID/StorageNamespace）。
func instanceLivePath(binding InstanceBinding) string {
	rel := binding.RelativePath
	if rel == "" {
		rel = Lookup(binding.RuleKey).LiveRelative
	}
	// filepath.Join 不会因第二个参数是绝对路径而丢弃前者（与 Python 语义不同），
	// 故这里必须显式判断，否则配置的绝对路径会被错误地拼到工作目录之下。
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	return filepath.Join(binding.WorkDir, filepath.FromSlash(rel))
}

// fileArchiveGlob 按绑定返回归档 glob（FR-502）。
//
// 历史实现固定返回 live 文件同目录的 "*.gz"，这对 Bukkit 系成立，
// 但 BungeeCord 的轮转产物是 proxy.log.N（数字后缀、非 gzip）——
// 故改为按规则取 glob。实例级路径覆盖时，归档 glob 跟随覆盖后 live 文件的
// 目录与既有形态（保持与历史一致：同目录同类归档）。
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
	// 实例级覆盖：沿用历史口径——live 文件同目录的 *.gz。
	return filepath.Join(filepath.Dir(instanceLivePath(binding)), "*.gz")
}

func (m *Manager) rawInstancePath(binding InstanceBinding, stream string) string {
	identity := sha256.Sum256([]byte(binding.UUID + "\x00" + binding.Generation))
	return filepath.Join(m.root, "var", "log", "raw", fmt.Sprintf("%x", identity), stream+".log")
}

func (m *Manager) pendingInstancePath(uuid, stream string) string {
	identity := sha256.Sum256([]byte(uuid))
	return filepath.Join(m.root, "var", "log", pendingSpoolRoot, fmt.Sprintf("%x", identity), stream+".spool")
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
	root := filepath.Join(m.root, "var", "log", pendingSpoolRoot)
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
