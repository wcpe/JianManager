package process

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// 实例清单快照（供 jmctl 等本机只读工具消费）。
//
// 为什么需要它：daemon 帧协议只懂 stdio 与 stop/kill，不知道「实例」是什么；
// PID 文件只记录寻址信息（uuid/pid/workdir），没有实例名。于是本机运维工具
// （以及刚连上服务器的 agent）只能看到一串 UUID，不知道那是什么服。
//
// 设计约束：
//   - **纯增量**：不参与 Worker 的任何决策，删掉它 Worker 照常运行
//   - **只读消费**：jmctl 只读，不写、不锁；文件用原子替换，读到旧值也自洽
//   - **失败不致命**：写快照出错只记日志，绝不影响实例生命周期
//
// 存放位置：数据根下 var/servers/instances.json —— 与 <uuid>.pid 同目录，
// 运维找实例的地方就能看到清单，不必另记路径。

// InstanceIndexFilename 是实例清单快照的文件名。
const InstanceIndexFilename = "instances.json"

// InstanceIndex 是快照整体结构。
type InstanceIndex struct {
	// GeneratedAt 生成时刻（RFC3339），用于判断快照有多旧。
	GeneratedAt string `json:"generatedAt"`
	// Instances 实例清单，按 Name 排序便于人读。
	Instances []InstanceEntry `json:"instances"`
}

// InstanceEntry 是单个实例在清单中的投影。
//
// 只放「本机可见且稳定」的字段：进程内的瞬时信息（健康结论、漂移进程等）
// 由心跳链路负责，塞进来只会让快照频繁变化而无助于识别。
type InstanceEntry struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name,omitempty"`
	State   string `json:"state"`
	PID     int    `json:"pid,omitempty"`
	WorkDir string `json:"workDir,omitempty"`
	// ProcessType 说明启动方式（direct/daemon/docker/rcon），排查时先看这个。
	ProcessType string `json:"processType,omitempty"`
	// ServerPort / QueryPort 非零时一并带上，方便直接连服或做端口冲突排查。
	ServerPort int `json:"serverPort,omitempty"`
	QueryPort  int `json:"queryPort,omitempty"`
}

// BuildInstanceIndex 依据当前实例表构造快照。
//
// 需要调用方持锁（与 GetAllInstanceStates 一致），故不加锁，
// 由 WriteInstanceIndex 在同一次加锁内调用，避免重复进出临界区。
func (m *Manager) buildInstanceIndexLocked() InstanceIndex {
	entries := make([]InstanceEntry, 0, len(m.instances))
	for uuid, inst := range m.instances {
		state := inst.State
		if inst.State == StateRunning && inst.strategy != nil && inst.strategy.State() == StateCrashed {
			state = StateCrashed
		}
		pid := 0
		if inst.strategy != nil {
			pid = inst.strategy.GetPID()
		}
		entries = append(entries, InstanceEntry{
			UUID:        uuid,
			Name:        inst.Name,
			State:       string(state),
			PID:         pid,
			WorkDir:     inst.WorkDir,
			ProcessType: string(inst.processType),
			ServerPort:  inst.ServerPort,
			QueryPort:   inst.QueryPort,
		})
	}
	// 按名称排序，空名排最后：文件是给人看的，稳定顺序才便于 diff 与扫读。
	slices.SortFunc(entries, func(a, b InstanceEntry) int {
		if a.Name == b.Name {
			return compareUUIDs(a.UUID, b.UUID)
		}
		if a.Name == "" {
			return 1
		}
		if b.Name == "" {
			return -1
		}
		if a.Name < b.Name {
			return -1
		}
		return 1
	})
	return InstanceIndex{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Instances:   entries,
	}
}

// compareUUIDs 是 UUID 的稳定排序比较。
func compareUUIDs(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// refreshInstanceIndexAsync 异步刷新实例清单快照。
//
// 用「单飞 + 合并」而非每次事件起一个 goroutine：批量启停 50 个实例会产生 100 次
// 状态变更，若每次都写盘就是 100 次 IO，而中间态对读者毫无价值——只关心最终结果。
// 因此在飞期间只置脏标记，由正在跑的那一轮在结束时补跑一次。
//
// reason 仅用于日志定位触发来源。
func (m *Manager) refreshInstanceIndexAsync(reason string) {
	if m.serversDir == "" {
		return
	}
	m.indexMu.Lock()
	m.indexDirty = true
	if m.indexRunning {
		// 已有写盘在进行：标记脏后立即返回，由那一轮收尾时补跑。
		m.indexMu.Unlock()
		return
	}
	m.indexRunning = true
	m.indexMu.Unlock()

	go func() {
		for {
			m.indexMu.Lock()
			if !m.indexDirty {
				m.indexRunning = false
				m.indexMu.Unlock()
				return
			}
			m.indexDirty = false
			m.indexMu.Unlock()

			if err := m.WriteInstanceIndex(m.serversDir); err != nil {
				// 快照是旁路产物：失败只记日志，绝不向上传播。
				// 下一次状态变更或心跳周期会重新触发，自愈。
				slog.Warn("写入实例清单快照失败（不影响实例运行）", "dir", m.serversDir, "reason", reason, "error", err)
			}
		}
	}()
}

// WriteInstanceIndex 把快照写到 dir/instances.json。
//
// 原子替换：先写临时文件再 rename。jmctl 随时可能读，读到半截 JSON 会直接解析失败，
// 而 rename 在 POSIX 与 Windows 上都保证「要么旧文件、要么新文件」。
//
// 返回错误供调用方记日志；**调用方不应因此中断生命周期操作**——
// 快照是旁路产物，缺一次下次事件会补上。
func (m *Manager) WriteInstanceIndex(dir string) error {
	m.mu.RLock()
	idx := m.buildInstanceIndexLocked()
	m.mu.RUnlock()

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化实例清单失败: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建清单目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, InstanceIndexFilename+".tmp*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	// 失败路径上清理临时文件，避免数据根里堆积 .tmp 残骸。
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入清单失败: %w", err)
	}
	// 先落盘再 rename：断电时宁可丢新快照，也不要留下内容空洞的文件。
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步清单失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭清单失败: %w", err)
	}

	target := filepath.Join(dir, InstanceIndexFilename)
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("替换清单失败: %w", err)
	}
	tmpName = "" // 已成功 rename，勿再删
	return nil
}
