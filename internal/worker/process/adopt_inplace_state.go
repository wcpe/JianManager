package process

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 就地收养状态的**落盘持久化**。
//
// ## 为什么必须持久化
// `AdoptedPID` 原本只活在内存里。Worker 一重启（升级、崩溃、`systemctl restart`），
// 收养关系就彻底消失——而那个外部进程（tmux/systemd 起的服）还活着。
// 后果是面板上“平台记录的状态”与“磁盘事实”重新分家：进程在跑、CP 显示 STOPPED。
// 真机验证时反复踩到：每次重启 Worker，5 台收养的服全部变回 STOPPED。
//
// ## 为什么不靠周期扫描自愈
// 扫描只能算出“目录里有活进程”，但算不出“平台是不该看着它”——
// 收养是一个**人工意图**（”这个进程归我管“），不能从盘上推断出来。
// 所以必须把那次意图落盘。
//
// ## 存放位置
// 与 daemon 的 PID 文件同目录（`<数据根>/var/servers/`），文件名 `<uuid>.adopted`。
// 复用同一目录的理由：它们描述同一件事（“这个实例当前有个活进程”），
// 生命周期与清理时机也一致（实例删除时一起清）。

// adoptedRecordFileSuffix 是就地收养记录的文件后缀。
const adoptedRecordFileSuffix = ".adopted"

// adoptedRecord 是落盘的一条收养记录。
//
// 只存恢复必需的最小信息：PID 与当时的命令行。
// 命令行用于恢复时**复核归属**（PID 可能已被 OS 回收给无关进程）——
// 不复核就直接相信一个旧 PID 是危险的。
type adoptedRecord struct {
	PID     int    `json:"pid"`
	Cmdline string `json:"cmdline,omitempty"`
}

// adoptedRecordPath 返回某实例的收养记录路径。
func (m *Manager) adoptedRecordPath(uuid string) string {
	if m == nil || m.pidDir == "" || uuid == "" {
		return ""
	}
	return filepath.Join(m.pidDir, uuid+adoptedRecordFileSuffix)
}

// persistAdopted 把收养关系落盘（原子写）。
//
// 失败只记日志不报错：收养本身已经生效（内存里置了 RUNNING），
// 落盘只是为了让重启后记得住——不能因为写文件失败就把已成功的收养回滚掉。
func (m *Manager) persistAdopted(uuid string, pid int, cmdline string) {
	path := m.adoptedRecordPath(uuid)
	if path == "" || pid <= 0 {
		return
	}
	raw, err := json.Marshal(adoptedRecord{PID: pid, Cmdline: truncateCmdline(cmdline)})
	if err != nil {
		slog.Warn("就地收养：序列化记录失败（重启后将失去该收养）", "instanceId", uuid, "error", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		slog.Warn("就地收养：写记录失败（重启后将失去该收养）", "instanceId", uuid, "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		slog.Warn("就地收养：落位记录失败（重启后将失去该收养）", "instanceId", uuid, "error", err)
	}
}

// forgetAdopted 删除某实例的收养记录（释放 / 实例删除时调）。
//
// 只删文件、不碰进程：本函数只负责“不再记得”，停不停服是调用方的事。
func (m *Manager) forgetAdopted(uuid string) {
	path := m.adoptedRecordPath(uuid)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("就地收养：删除记录失败", "instanceId", uuid, "error", err)
	}
}

// loadAdopted 读某实例的收养记录。返回 ok=false 表示无记录或记录不可用。
func (m *Manager) loadAdopted(uuid string) (adoptedRecord, bool) {
	path := m.adoptedRecordPath(uuid)
	if path == "" {
		return adoptedRecord{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return adoptedRecord{}, false
	}
	var rec adoptedRecord
	if err := json.Unmarshal(raw, &rec); err != nil || rec.PID <= 0 {
		return adoptedRecord{}, false
	}
	return rec, true
}

// restoreAdoptedRuntimes Worker 启动时把落盘的就地收养恢复回内存。
//
// ## 恢复时必须复核，不能盲信旧 PID
// PID 会被 OS 回收。重启间隔里那个号可能已经属于别的进程了——
// 直接认账会让平台对一台**无关进程**发信号（停机/强杀）时打错人。
// 所以逐条做两道校：
//  1. 进程还活着（pidAlive）；
//  2. 命令行变化不大（looksLikeInstanceRuntime 的粗判，容忍外层包装差异）。
//
// 任一不过即当作“已退出”：删记录，实例保持 STOPPED（如实反映）。
//
// @return 恢复的实例数与清理掉的陈旧记录数。
func (m *Manager) restoreAdoptedRuntimes() (restored, cleaned int) {
	if m == nil || m.pidDir == "" {
		return 0, 0
	}
	entries, err := os.ReadDir(m.pidDir)
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), adoptedRecordFileSuffix) {
			continue
		}
		uuid := strings.TrimSuffix(entry.Name(), adoptedRecordFileSuffix)
		rec, ok := m.loadAdopted(uuid)
		if !ok {
			_ = os.Remove(filepath.Join(m.pidDir, entry.Name()))
			cleaned++
			continue
		}

		m.mu.Lock()
		inst, exists := m.instances[uuid]
		m.mu.Unlock()
		if !exists {
			// 实例还没注册（Resync 未到）：先留着记录，后续轮次再恢复。
			continue
		}

		if !m.pidAlive(rec.PID) || !looksLikeInstanceRuntime(rec.Cmdline, inst.StartCommand) {
			// 进程已退出 / PID 已被回收：如实反映为停止，清掉陈旧记录。
			slog.Warn("就地收养恢复：进程已不在或已不是它，放弃收养",
				"instanceId", uuid, "pid", rec.PID)
			m.forgetAdopted(uuid)
			// 必须同时把内存里的运行态改回 STOPPED。
			//
			// 只删记录是不够的：实例的 AdoptedPID/State 还停在 RUNNING，
			// 而收养恢复路径是**唯一知道“那个进程已经没了”的地方**:
			//   - 存活探测（evidence）看到 AdoptedPID>0 就去探它，报“进程不存活”；
			//   - 健康巡检据此每拍写一条“进程/容器不存活”；
			//   - CP 侧状态停在 RUNNING，而 Start 的合法转换里没有 RUNNING→STARTING，
			//     于是**用户再也无法用平台重新拉起它**（真机踩到：只能手工改库）。
			// 所以这里必须一并清掉 AdoptedPID 并置 STOPPED，让状态真正回到可操作。
			m.mu.Lock()
			if cur, ok := m.instances[uuid]; ok && cur == inst {
				inst.AdoptedPID = 0
				inst.State = StateStopped
				inst.StartedAt = time.Time{}
			}
			m.mu.Unlock()
			cleaned++
			continue
		}

		m.mu.Lock()
		// 已恢复成完全相同的状态（同一 PID 已认领且已是 RUNNING）：不重复计数、不重复记日志。
		//
		// 为什么要这道早退：本函数会被 CP 每次重推（实测 60–90s 一拍）重复调用，
		// 没有它会每拍刷 5 条「已重新认领外部进程」——日志噪声会把真正的事件埋掉。
		// 「幂等」在这里的具体含义就是：结果相同就不算一次新的恢复。
		if inst.AdoptedPID == rec.PID && inst.State == StateRunning {
			m.mu.Unlock()
			continue
		}
		inst.AdoptedPID = rec.PID
		inst.State = StateRunning
		m.mu.Unlock()
		restored++
		slog.Info("就地收养恢复：已重新认领外部进程", "instanceId", uuid, "pid", rec.PID)
	}
	return restored, cleaned
}

// RestoreAdoptedRuntimes 是 Worker 启动时恢复就地收养的**公开入口**。
//
// 包一层是为了让调用方（apps/worker）不接触内部细节。
// 幂等：多次调用无害——已处于「同一 PID + RUNNING」的实例直接跳过（不重复计数、不重复记日志），
// 已清理的记录不会再生。
// 可在 ResyncInstances 之后再次调用，把“首次恢复时实例还没注册”的那批补齐。
func (m *Manager) RestoreAdoptedRuntimes() (restored, cleaned int) {
	return m.restoreAdoptedRuntimes()
}

// adoptedPidText 供日志/审计拼字符串用（避免在各处重复 strconv）。
func adoptedPidText(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return strconv.Itoa(pid)
}
