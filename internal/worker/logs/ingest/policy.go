// 本文件实现日志采集运行期策略（FR-503）：接收 CP 经心跳下发的生效配置、持久化到
// 数据根，并供登记路径按策略收敛参数。
//
// 三层语义（高 → 低）：CP 下发的生效值（平台默认 + Worker 覆盖的合并结果）
// > worker.yml 基线（DefaultAcqPolicy）。Worker 侧**不再二次合并**——CP 下发的已是
// 生效值，二次合并会引入「两处都能改、谁也说不清哪边生效」的歧义。
//
// 唯一的例外是「未配置」：定时归档扫描的周期（GzScanInterval）为空/0 表示 CP 未下发该键，
// 此时由调用方回落 worker.yml 基线（见 ArchiveScanInterval 的三条口径）。
//
// 应用约束（spec §3.2）：策略只影响**后续登记/重登记**的实例参数，不触发采集运行时
// 全量重建；已在采实例保持原状（运行中切换路径会导致账本断裂，必须避免）。
package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AcqPolicy 是运行期生效的日志采集配置（FR-503）。
type AcqPolicy struct {
	// RulesEnabled 是否按核心类型规则解析实例日志路径（FR-502 规则引擎总开关）。
	// 关闭 = 回退历史默认布局（bukkit，即引入规则前的 logs/latest.log 行为）——
	// 这是规则表本身出问题时的应急回退档，不影响已在采实例。
	RulesEnabled bool `json:"rules_enabled"`
	// RealtimeTail 实时采集开关（FR-504 消费）：关闭 = 应急停采，不停服、不丢位置。
	RealtimeTail bool `json:"realtime_tail"`
	// GzImportEnabled 定时归档导入扫描的**总开关**（2026-10-15 复核统一口径）：
	// 关闭 = 不启用定时归档扫描，手动入口（ImportArchivesNow）不受影响。
	GzImportEnabled bool `json:"gz_import_enabled"`
	// GzScanInterval 定时归档扫描**周期**；空/0 = 未配置 ⇒ 由上层回落 worker.yml 基线
	// （log_ingest.archive_scan_interval）。总开关关闭时本字段无意义（一律不扫描）。
	// 协议上 0 只表示「未配置」：写入侧禁止配 0 或负数，故可安全区分「关闭」与「未配置」。
	GzScanInterval time.Duration `json:"gz_scan_interval"`
	// PathOverrides 实例级路径覆盖：targetID（`inst:<id>`）→ 相对或绝对路径。
	// 登记实例时**优先于** CP 下发的相对路径与规则默认（手动覆盖档）。
	PathOverrides map[string]string `json:"path_overrides,omitempty"`
}

// ArchiveScanInterval 返回定时归档导入扫描的**生效周期**（FR-504），三条口径（2026-10-15 复核统一）：
//
//   - 总开关（GzImportEnabled）关闭 ⇒ 一律 0（不扫描；手动入口 ImportArchivesNow 不受影响）。
//     这是「gz 导入总开关」的落点：存量源的历史归档既不能走采集轮（acquire 层的不变量：
//     历史段只能前置到**未被读过**的当前段，见 FileTailer.CurrentSegmentUnread 的说明），
//     也不能走停用的定时器，于是「是否允许自动导入历史归档」唯一可用的出口就是这里。
//   - 总开关开启 + 周期为空/0 ⇒ 返回 0，其含义是「**未配置**」：调用方须回落 worker.yml 基线
//     （log_ingest.archive_scan_interval），而不是把它当「关闭」。
//   - 总开关开启 + 配了周期 ⇒ 返回该周期（面板可配、经心跳下发）。
//
// 为什么必须区分「关闭」与「未配置」：协议上 0 只表示未配置（写入侧禁止配 0 或负数），
// 而 worker.yml 的基线是既有部署的唯一配置面——把两者的 0 混为一谈，会让
// `log_ingest.archive_scan_interval` 被平台默认值静默短路（存量 gz 的唯一自动出口失效且无日志）。
//
// 为什么不额外停掉采集轮的自动发现：那是 FR-474 的既有承诺（新绑定源接管时先导入历史、
// 再给当前段分配位置），停掉会造成**静默的历史缺失**；而保守关闭的理由（避免一次性导入
// 大量归档）已由 pipeline.ImportArchive 的 WAL 余量预算覆盖——逐批导入、位置落回账本、
// 下一轮从断点续读，不会把整批压进一轮。
func (p AcqPolicy) ArchiveScanInterval() time.Duration {
	if !p.GzImportEnabled {
		return 0
	}
	return p.GzScanInterval
}

// DefaultAcqPolicy 返回无任何外部配置时的基线（等价于 worker.yml 基线的内置表达）。
//
// 取值理由：规则引擎与实时采集默认开（与 FR-502 落地后的实际行为一致，不能因引入配置而
// 改变默认采集行为）；定时归档扫描默认 **开关开、周期空** —— 效果仍是「默认不扫描」，
// 但扫描与否交回 worker.yml 基线决定（2026-10-15 复核：旧默认开关 false 会让基线的回落
// 分支在实践中不可达，配了 archive_scan_interval 的部署升级后定时扫描静默变关）。
func DefaultAcqPolicy() AcqPolicy {
	return AcqPolicy{
		RulesEnabled:    true,
		RealtimeTail:    true,
		GzImportEnabled: true,
		GzScanInterval:  0,
	}
}

// SetAcqPolicy 覆盖运行期采集策略，返回是否**实际发生变化**（幂等）。
//
// 无变化时不写状态、不产生副作用，供心跳每拍调用（CP 每拍重发生效值）。
func (m *Manager) SetAcqPolicy(p AcqPolicy) bool {
	if m == nil {
		return false
	}
	normalized := normalizeAcqPolicy(p)
	if cur := m.acqPolicy.Load(); cur != nil && AcqPolicyEqual(*cur, normalized) {
		return false
	}
	m.acqPolicy.Store(&normalized)
	return true
}

// AcqPolicySnapshot 返回当前生效的采集策略（副本；调用方改返回值不影响内部状态）。
//
// 未设置过（尚未下发/加载）时返回 DefaultAcqPolicy：零值 Manager 也安全，
// 不会因「没配置」而把规则引擎当成关闭。读取为无锁原子操作——采集轮每源每轮都会调它。
func (m *Manager) AcqPolicySnapshot() AcqPolicy {
	if m == nil {
		return DefaultAcqPolicy()
	}
	if cur := m.acqPolicy.Load(); cur != nil {
		return cloneAcqPolicy(*cur)
	}
	return DefaultAcqPolicy()
}

// SyncArchiveScan 按给定周期同步定时归档导入扫描的启停（FR-504），返回是否发生变化。
//
// interval <= 0 表示关闭（不启动任何 goroutine）。值变化时停旧启新——周期来自采集策略、
// 经心跳每拍下发，因此面板改配置后无需重启 Worker；值未变化时不动（避免每拍重启扫描 goroutine）。
//
// 为什么把它放在 ingest 包而不是由调用方直接 RunArchiveScan：RunArchiveScan 每次调用都会
// 新起一个 goroutine，若由调用方自行判断启停，很容易在「策略每拍下发」的路径上反复重启；
// 由本方法收口后，调用方只需无脑同步当前周期。
func (m *Manager) SyncArchiveScan(interval time.Duration) bool {
	if m == nil {
		return false
	}
	if interval < 0 {
		interval = 0
	}
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	// 幂等判据基于「已同步过的周期」而非「是否在运行」：关闭状态（running=false）下
	// 重复同步关闭也必须返回 false，否则每拍心跳都会报告一次「发生变化」。
	if m.scanSynced && m.scanInterval == interval {
		return false
	}
	m.scanSynced = true
	if m.scanStop != nil {
		m.scanStop()
		m.scanStop = nil
	}
	m.scanRunning = false
	m.scanInterval = interval
	if interval <= 0 {
		return true
	}
	if stop := m.RunArchiveScan(interval); stop != nil {
		m.scanStop = stop
		m.scanRunning = true
	}
	return true
}

// StopArchiveScan 停止定时归档扫描（幂等）；供运行时停止路径调用，避免 goroutine 泄漏。
func (m *Manager) StopArchiveScan() {
	if m == nil {
		return
	}
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	if m.scanStop != nil {
		m.scanStop()
		m.scanStop = nil
	}
	m.scanRunning = false
	m.scanInterval = 0
}

// AcqPolicyEqual 比较两份策略是否等价（含 PathOverrides 的逐键比较）。
func AcqPolicyEqual(a, b AcqPolicy) bool {
	if a.RulesEnabled != b.RulesEnabled || a.RealtimeTail != b.RealtimeTail ||
		a.GzImportEnabled != b.GzImportEnabled || a.GzScanInterval != b.GzScanInterval {
		return false
	}
	if len(a.PathOverrides) != len(b.PathOverrides) {
		return false
	}
	for k, v := range a.PathOverrides {
		if b.PathOverrides[k] != v {
			return false
		}
	}
	return true
}

// normalizeAcqPolicy 归一化外部输入：负数周期一律归零（= 关闭），
// 并复制 PathOverrides（避免调用方后续改动影响到已生效状态）。
func normalizeAcqPolicy(p AcqPolicy) AcqPolicy {
	if p.GzScanInterval < 0 {
		p.GzScanInterval = 0
	}
	p.PathOverrides = clonePathOverrides(p.PathOverrides)
	return p
}

func cloneAcqPolicy(p AcqPolicy) AcqPolicy {
	p.PathOverrides = clonePathOverrides(p.PathOverrides)
	return p
}

// clonePathOverrides 复制映射；空映射归一为 nil（使 len 判定与相等比较口径一致）。
func clonePathOverrides(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// acqPolicyFileName 是采集策略持久化文件名（FR-503，spec §3.4），存于 Worker 数据根 etc/ 下。
const acqPolicyFileName = "log-acquisition.json"

// acqPolicyFile 是持久化形态：与内存结构分开，duration 落盘用 Go duration 文本，
// 避免纳秒整数在人工排查时不可读。
type acqPolicyFile struct {
	RulesEnabled    bool              `json:"rules_enabled"`
	RealtimeTail    bool              `json:"realtime_tail"`
	GzImportEnabled bool              `json:"gz_import_enabled"`
	GzScanInterval  string            `json:"gz_scan_interval"`
	PathOverrides   map[string]string `json:"path_overrides,omitempty"`
}

// SaveAcqPolicy 把策略持久化到 dir（不存在则创建），供 Worker 重启后恢复。
//
// 采用「写临时文件 + 改名」：改名在 POSIX 上是原子的，避免进程在写中途退出
// 留下半截 JSON（下次启动解析失败）。
func SaveAcqPolicy(dir string, p AcqPolicy) error {
	if dir == "" {
		return fmt.Errorf("ingest: 采集策略持久化目录为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ingest: 创建采集策略目录失败: %w", err)
	}
	p = normalizeAcqPolicy(p)
	payload, err := json.MarshalIndent(acqPolicyFile{
		RulesEnabled:    p.RulesEnabled,
		RealtimeTail:    p.RealtimeTail,
		GzImportEnabled: p.GzImportEnabled,
		GzScanInterval:  p.GzScanInterval.String(),
		PathOverrides:   p.PathOverrides,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("ingest: 序列化采集策略失败: %w", err)
	}
	target := filepath.Join(dir, acqPolicyFileName)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return fmt.Errorf("ingest: 写入采集策略临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, target); err != nil {
		// 清理临时文件；失败不影响主流程（下次覆盖写会重建）。
		_ = os.Remove(tmp)
		return fmt.Errorf("ingest: 提交采集策略失败: %w", err)
	}
	return nil
}

// LoadAcqPolicy 读取持久化策略。
//
// 返回 (策略, 是否存在, 错误)。文件不存在返回 (DefaultAcqPolicy(), false, nil)；
// 存在但整体 JSON 非法时返回错误，由调用方告警并回退默认——**不阻断启动**：
// 采集配置问题绝不能让 Worker 起不来（那会让节点直接离线）。
func LoadAcqPolicy(dir string) (AcqPolicy, bool, error) {
	if dir == "" {
		return DefaultAcqPolicy(), false, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, acqPolicyFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultAcqPolicy(), false, nil
		}
		return DefaultAcqPolicy(), false, fmt.Errorf("ingest: 读取采集策略失败: %w", err)
	}
	var f acqPolicyFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return DefaultAcqPolicy(), false, fmt.Errorf("ingest: 解析采集策略失败: %w", err)
	}
	p := AcqPolicy{
		RulesEnabled:    f.RulesEnabled,
		RealtimeTail:    f.RealtimeTail,
		GzImportEnabled: f.GzImportEnabled,
		PathOverrides:   f.PathOverrides,
	}
	// 周期文本非法时按「关闭」处理：与写入侧的校验口径一致（空/非法 = 关闭），
	// 且不在加载路径上报错——一个字段坏掉不该让整份策略被丢弃。
	if f.GzScanInterval != "" {
		if d, err := time.ParseDuration(f.GzScanInterval); err == nil && d > 0 {
			p.GzScanInterval = d
		}
	}
	return normalizeAcqPolicy(p), true, nil
}
