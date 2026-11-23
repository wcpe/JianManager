package process

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// 就地导入（in_place）的外部进程收养：**不改进程、只登记观测**。
//
// ## 为什么需要一条与 AdoptForeignRuntime 不同的路径
// 既有的 [Manager.AdoptForeignRuntime] 语义是“先停外来进程、再以受管方式拉起”（先停后启）——
// 那对“目录里有残留/来路不明的进程、希望平台用自管方式重跑一份”是对的。
// 但对**就地导入的现成服务器**（典型：运维用 tmux / systemd 已经在跑、业务不停机就不能重启）
// 这就是错的：一次接管 = 一次重启，而且拉起来的还是**另一个进程**（tmux 那份仍占着端口，
// 新的必因端口冲突反复崩溃）。真机实测过的现象：6 台服被接管后全部“崩溃×5 → 熔断”，
// 而 tmux 里的原服务器从头到尾没受影响——平台与磁盘彻底分家。
//
// 本文件提供的就是另一条路：**承认现有进程**，把它登记为“平台的运行态”，
// 全程不发任何信号、不新建进程。代价也要说清楚——
// 平台得不到 stdin 控制通道（不是它 spawn 的），因此：
//   - 终端输入 / 优雅停止 不可用（需回退到操作系统信号，由 Stop 的降级路径处理）；
//   - 日志采集拿不到 wrapper socket 通道（只能靠日志文件）；
//   - 进程生命周期仍归外部管理器（tmux/systemd），平台只是“看着它”。
// 这些限制对“把现成集群搬进面板看”这个诉求是可接受的；需要完全受管时再走 stop → start。

// ErrAdoptNoRuntime 表示目录下没有可收养的活进程（调用方应回退到正常启动）。
var ErrAdoptNoRuntime = errors.New("工作目录下没有可收养的活进程")

// AdoptInPlaceRuntime 把就地导入实例目录下**已在运行的外部进程**收养为平台运行态。
//
// 与 [Manager.AdoptForeignRuntime] 的根本区别：
//   - 不向外部进程发任何信号（不 SIGTERM、不 SIGKILL）；
//   - 不创建第二个进程；
//   - 只是把实例状态置为 RUNNING 并记录被收养的 PID，供观测与状态对齐。
//
// 前置条件（任一不满足即报错，**绝不猜**）：
//  1. 实例存在且当前不是 RUNNING（已在运行时幂等返回 nil）；
//  2. 目录下确实观测到活进程，且归属复核通过（防 PID 回收后误收养无关进程）；
//  3. 该进程的命令行看起来像本实例的启动目标（jar 名 / 主类命中）——见 looksLikeInstanceRuntime。
//
// @param uuid 实例 UUID。
// @return 被收养的 PID；错误时非 nil。
func (m *Manager) AdoptInPlaceRuntime(uuid string) (int, error) {
	inst, exists := m.lockInstanceOperation(uuid)
	if !exists {
		return 0, fmt.Errorf("实例 %s 不存在", uuid)
	}
	defer inst.operationMu.Unlock()

	m.mu.RLock()
	state := inst.State
	workDir := inst.WorkDir
	startCommand := inst.StartCommand
	adoptedPID := inst.AdoptedPID
	m.mu.RUnlock()

	// 已经 RUNNING **且被收养的那个进程还活着**：幂等。重复点击接管不应报错，也不该去碰进程。
	//
	// 为什么必须判 adoptedPID 存活，而不能只看 state==RUNNING：
	// 运维重启服务器（tmux 里 Ctrl+C 再拉起）后，旧 java 退出、新 java 起来，但实例状态
	// 会被心跳周期维持成 RUNNING，而 AdoptedPID 仍指向**已死掉的旧 PID**。
	// 旧实现此时直接 return nil——看似“已经是运行中”，实则收养关系早已失效：
	// 存活探测每拍都报 “进程不存活”，面板出现「RUNNING + 进程/容器不存活」这种自相矛盾，
	// 而且重新点接管也修不好（同样被这个早退吃掉）。
	// 真机实测：重启 4 台后重新接管全回 adopted:true 但 .adopted 记录一条没重建。
	//
	// 所以：仍活且 PID 一致 → 真幂等直接返回；否则继续往下走，重新认领目录里现在那个进程。
	if state == StateRunning && adoptedPID > 0 && m.pidAlive(adoptedPID) {
		return 0, nil
	}

	fr := m.foreignRuntime(uuid)
	if fr.PID <= 0 || !m.pidAlive(fr.PID) {
		// 缓存里没有不代表真的没有：孤儿扫描是 60s 一轮，而 Worker 刚重启 / 实例刚导入时
		// 缓存必为空。这时如果直接报“没东西可收养”，用户会看到一个明明在跑的服被拒——
		// 真机踩过：重启 Worker 后立即收养，5 台全回 adopted:false，而 java 进程都在。
		//
		// 故缓存未命中时**现探一次**（只读 /proc 与目录，不发信号），命令行也能拿到。
		if probed, ok := m.probeForeignRuntimeOnDemand(workDir); ok {
			fr = probed
		} else {
			return 0, ErrAdoptNoRuntime
		}
	}
	if fr.PID <= 0 || !m.pidAlive(fr.PID) {
		return 0, ErrAdoptNoRuntime
	}
	// 归属复核：观测与点击之间可能隔了几十秒，PID 可能已被 OS 回收给无关进程。
	// 这里传 expectWrapper=false：就地导入的进程不可能是平台 wrapper。
	if !m.verifyProcessOwnership(fr.PID, uuid, workDir, false) {
		return 0, fmt.Errorf("无法确认 pid=%d 仍属实例 %s 的目录（%s），拒绝收养（PID 可能已被回收）",
			fr.PID, uuid, workDir)
	}
	// 命令行对照：只拒绝“明显不是这个实例的进程”，不追求精确匹配。
	// 理由：外部启动方式千差万别（脚本包一层、jre 路径不同、参数顺序不同），
	// 要求精确匹配会让绝大多数真实场景收养失败；而目录归属已由上面复核锁死。
	if !looksLikeInstanceRuntime(fr.Cmdline, startCommand) {
		// 最典型的情形：目录里只剩 tmux/systemd 的空壳（`-bash`），真正的服已退出。
		// 这不是“收养失败”，而是“根本没东西可收养”——报清楚让调用方知道可以直接启动，
		// 而不是去排一个不存在的进程不匹配问题。
		if !looksLikeRuntimeCmdline(fr.Cmdline) {
			return 0, fmt.Errorf("%w（目录下只有 pid=%d 的 %q，不像游戏服运行时）",
				ErrAdoptNoRuntime, fr.PID, fr.Cmdline)
		}
		return 0, fmt.Errorf("pid=%d 的命令行（%s）与本实例启动目标（%s）不匹配，拒绝收养", fr.PID, fr.Cmdline, startCommand)
	}

	// 登记为运行态。**刻意不碰 strategy**：strategy 代表“平台自己 spawn 的句柄”，
	// 外部进程没有这个句柄，伪造一个只会在 Stop/终端的路径上摸到空指针。
	// 运行是否真活着，由 health 巡检每拍的 pidAlive 判定。
	m.mu.Lock()
	if current, ok := m.instances[uuid]; !ok || current != inst {
		m.mu.Unlock()
		return 0, fmt.Errorf("实例 %s 不存在或已被替换", uuid)
	}
	inst.State = StateRunning
	inst.AdoptedPID = fr.PID
	inst.StartedAt = time.Now()
	m.mu.Unlock()

	// 落盘：Worker 重启后靠它把收养关系恢复回来。
	// 不落盘的话，一次升级/重启就让“进程在跑但平台说停了”——真机反复踩到。
	m.persistAdopted(uuid, fr.PID, fr.Cmdline)

	// 漂移观测消解：收养后它不再是“外来”的（已记账）。
	m.clearForeignRuntime(uuid)

	m.auditOrphan("orphan.inplace_runtime_adopted", uuid,
		fmt.Sprintf(`{"instanceUuid":%q,"pid":%d,"cmdline":%q,"policy":"adopt_inplace"}`,
			uuid, fr.PID, fr.Cmdline), true, "")
	slog.Info("已收养就地导入实例的外部运行进程（未改进程、未重启）",
		"instanceId", uuid, "pid", fr.PID, "cmdline", fr.Cmdline)
	return fr.PID, nil
}

// ReleaseInPlaceRuntime 释放就地收养：只清平台侧登记，**不动进程**。
//
// 用于“我不想再看了，但也不能让平台去杀我的服”的场景（如实例迁走、或运维要切回纯外部管理）。
// 若调用方真想停服务，应走正常的 Stop（它会尝试优雅停止，失败后视配置决定是否强杀）。
func (m *Manager) ReleaseInPlaceRuntime(uuid string) error {
	inst, exists := m.lockInstanceOperation(uuid)
	if !exists {
		return fmt.Errorf("实例 %s 不存在", uuid)
	}
	defer inst.operationMu.Unlock()

	m.mu.Lock()
	adopted := inst.AdoptedPID
	inst.AdoptedPID = 0
	inst.State = StateStopped
	inst.StartedAt = time.Time{}
	m.mu.Unlock()

	// 同步清掉落盘记录：释放的本意就是“以后不再记得它”，
	// 留着记录会让下次 Worker 重启又把它认回来。
	m.forgetAdopted(uuid)

	m.auditOrphan("orphan.inplace_runtime_released", uuid,
		fmt.Sprintf(`{"instanceUuid":%q,"pid":%d,"policy":"release_inplace"}`, uuid, adopted), true, "")
	slog.Info("已释放就地收养（进程未被触碰，仍在外部管理器下运行）", "instanceId", uuid, "pid", adopted)
	return nil
}

// looksLikeInstanceRuntime 粗判一个外部进程是否像本实例的运行时。
//
// 判据（任一命中即通过）：
//   - 外部命令行包含启动命令里的 jar 文件名（最常见且最可靠的信号）；
//   - 外部命令行包含启动命令里的主类/可执行文件名；
//   - 两边都是 java 且原启动命令无可识别工件（如自定义脚本），给予放行。
//
// 刻意宽松：这是一道“防手滑”的阀门，不是安全边界——目录归属已由 verifyProcessOwnership 保证。
func looksLikeInstanceRuntime(foreignCmdline, startCommand string) bool {
	fc := strings.ToLower(strings.TrimSpace(foreignCmdline))
	sc := strings.ToLower(strings.TrimSpace(startCommand))
	if fc == "" || sc == "" {
		// 任一为空时无法比对；不武断拒绝（目录归属已复核）。
		return true
	}
	// 提取启动命令里出现的 .jar 文件名逐个比对。
	for _, jar := range jarsInCommand(sc) {
		if strings.Contains(fc, jar) {
			return true
		}
	}
	// 无 jar 可比（脚本/二进制）：只要两边都提到 java 就放行。
	if strings.Contains(fc, "java") && strings.Contains(sc, "java") {
		return true
	}
	// 兜底：外部命令行里出现启动命令的第一个 token（可执行名）。
	if f := firstToken(sc); f != "" && strings.Contains(fc, f) {
		return true
	}
	// 最后一道：两边都没有可识别工件（自定义包装），不阻止（见函数注释的刻意宽松）。
	return len(jarsInCommand(sc)) == 0 && !strings.Contains(sc, "java")
}

// jarsInCommand 抽出一行命令行里所有 .jar 的基名（小写）。
func jarsInCommand(cmd string) []string {
	out := make([]string, 0, 2)
	for _, tok := range strings.Fields(cmd) {
		t := strings.Trim(tok, "\"'")
		if strings.HasSuffix(strings.ToLower(t), ".jar") {
			if i := strings.LastIndexAny(t, "/\\"); i >= 0 {
				t = t[i+1:]
			}
			out = append(out, strings.ToLower(t))
		}
	}
	return out
}

// firstToken 取命令行第一个空白分隔的 token。
func firstToken(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// pidFromString 把文本 PID 解析为正整数（解析失败返回 0）。
//
// 保留给审计 detail 与外部输入路径使用；解析失败一律当“未知”而不是 0 号进程。
func pidFromString(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// adoptInPlaceUnsupported 说明为何某些 processType 不支持就地收养。
//
// docker 实例的“进程”在容器里，pidAlive 在宿主上看不到，靠 cgroup/容器状态判定；
// 把它也纳入本路径会让“是否真在跑”的判定分叉，故 v1 明确只支持进程可见的本机模式。
func adoptInPlaceUnsupported(processType ProcessType) error {
	return fmt.Errorf("就地收养暂不支持 processType=%s（仅支持进程可见的本机模式）", processType)
}
