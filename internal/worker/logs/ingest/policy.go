// 本文件实现日志采集运行期策略（FR-503）：接收 CP 经心跳下发的生效配置、持久化到
// 数据根，并供登记路径按策略收敛参数。
//
// 三层语义（高 → 低）：CP 下发的生效值（平台默认 + Worker 覆盖的合并结果）
// > worker.yml 基线（DefaultAcqPolicy）。Worker 侧**不再二次合并**——CP 下发的已是
// 生效值，二次合并会引入「两处都能改、谁也说不清哪边生效」的歧义。
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
	// GzImportEnabled gz 导入开关（FR-504 消费）：关闭 = 停自动发现，手动导入入口仍可用。
	GzImportEnabled bool `json:"gz_import_enabled"`
	// GzScanInterval gz 定时扫描周期（FR-504 消费）；<=0 = 关闭定时扫描。
	GzScanInterval time.Duration `json:"gz_scan_interval"`
	// PathOverrides 实例级路径覆盖：targetID（`inst:<id>`）→ 相对或绝对路径。
	// 登记实例时**优先于** CP 下发的相对路径与规则默认（手动覆盖档）。
	PathOverrides map[string]string `json:"path_overrides,omitempty"`
}

// DefaultAcqPolicy 返回无任何外部配置时的基线（等价于 worker.yml 基线的内置表达）。
//
// 取值理由：规则引擎与实时采集默认开（与 FR-502 落地后的实际行为一致，
// 不能因引入配置而改变默认采集行为）；gz 导入与定时扫描默认关（保守，
// 避免在无人察觉时一次性导入大量历史归档）。
func DefaultAcqPolicy() AcqPolicy {
	return AcqPolicy{
		RulesEnabled:    true,
		RealtimeTail:    true,
		GzImportEnabled: false,
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.acqPolicy != nil && AcqPolicyEqual(*m.acqPolicy, normalized) {
		return false
	}
	stored := normalized
	m.acqPolicy = &stored
	return true
}

// AcqPolicySnapshot 返回当前生效的采集策略（副本；调用方改返回值不影响内部状态）。
//
// 未设置过（尚未下发/加载）时返回 DefaultAcqPolicy：零值 Manager 也安全，
// 不会因「没配置」而把规则引擎当成关闭。
func (m *Manager) AcqPolicySnapshot() AcqPolicy {
	if m == nil {
		return DefaultAcqPolicy()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.acqPolicy == nil {
		return DefaultAcqPolicy()
	}
	return cloneAcqPolicy(*m.acqPolicy)
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
