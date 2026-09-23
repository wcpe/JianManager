package process

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// minUnboundJavaMajor 是未绑定 JDK 的 MC 实例所需的最低 Java 大版本。
// 现代 MC（1.18+）需 Java 17+；未显式绑定 JDK 时以此为安全下限，拦截
// 「PATH 落到老 Java（如 8）跑现代 Paper 致 UnsupportedClassVersionError
// 静默崩在游戏服自身日志」（BUG-012）。绑定了 JDK 则不施此下限（CP 已按实例要求选定）。
const minUnboundJavaMajor = 17

// javaMajorProbe 探测某 java 的大版本：javaBin 为 java 可执行所在目录（空=用 PATH 的 java）。
// 返回 (大版本, 是否成功跑起来)。包级变量以便单测替换。
var javaMajorProbe = probeJavaMajor

// preflightJavaVersion 在启动 MC（java）实例前校验将实际使用的 java 版本，
// 不满足时返回明确错误（指引绑定/安装合适 JDK），避免游戏服以
// UnsupportedClassVersionError 静默崩在自身日志、面板只见 CRASHED 无因（BUG-012）。
//
// 规则：
//   - 非 java 启动命令（node/shell 等）→ 跳过，不误伤非 MC/非 java 实例。
//   - 绑定了 JDK（JavaHome/JDKBinPath 非空）→ 探测绑定的 bin/java；能跑即通过
//     （不施下限，CP 已按实例要求选定该 JDK），跑不起来则报错。
//   - 未绑定 → 探测 PATH 的 java；跑不起来或大版本 < minUnboundJavaMajor 则报错。
func preflightJavaVersion(spec CommandSpec) error {
	if !isJavaStartCommand(spec.StartCommand) {
		return nil
	}
	bound := spec.JDKBinPath != "" || spec.JavaHome != ""
	javaBin := spec.JDKBinPath
	if javaBin == "" && spec.JavaHome != "" {
		javaBin = filepath.Join(spec.JavaHome, "bin")
	}
	major, ok := javaMajorProbe(javaBin)
	if bound {
		if !ok {
			return fmt.Errorf("实例绑定的 JDK 无法运行（%s）：请检查该 JDK 是否完好，或为实例重新绑定可用 JDK", javaBin)
		}
		return nil
	}
	if !ok {
		return fmt.Errorf("实例未绑定 JDK 且 PATH 上无可用 java：请为实例绑定 JDK，或在该节点安装合适大版本的 JDK")
	}
	if major < minUnboundJavaMajor {
		return fmt.Errorf("实例未绑定 JDK，PATH 上的 java 为版本 %d（低于现代 MC 所需的 Java %d）：请为实例绑定合适大版本的 JDK，避免游戏服因 Java 版本不符崩溃", major, minUnboundJavaMajor)
	}
	return nil
}

// isJavaStartCommand 判断启动命令是否调用 java（首个 token 的可执行名为 java/java.exe）。
func isJavaStartCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	exe := strings.ToLower(filepath.Base(strings.Trim(fields[0], `"`)))
	return exe == "java" || exe == "java.exe"
}

// probeJavaMajor 跑 `<javaBin>/java -version` 解析大版本；javaBin 空时用 PATH 的 java。
func probeJavaMajor(javaBin string) (int, bool) {
	exe := "java"
	if javaBin != "" {
		exe = filepath.Join(javaBin, "java")
	}
	out, err := exec.Command(exe, "-version").CombinedOutput()
	if err != nil {
		return 0, false
	}
	return parseJavaMajor(string(out))
}

// parseJavaMajor 从 `java -version` 输出解析大版本（`1.8.0_422`→8、`21.0.4`→21、`11.0.2`→11）。
func parseJavaMajor(out string) (int, bool) {
	m := regexp.MustCompile(`version "(\d+)(?:\.(\d+))?`).FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	first, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	if first == 1 && m[2] != "" {
		// 旧式 1.X 命名 → 大版本取 X（如 1.8 → 8）。
		second, err := strconv.Atoi(m[2])
		if err != nil {
			return 0, false
		}
		return second, true
	}
	return first, true
}

// preflightListProcesses 是启动预检枚举本机进程的入口（测试可替换）。
// 与孤儿扫描共用 defaultListProcesses 的枚举实现（cmdline/cwd 快照）。
var preflightListProcesses = defaultListProcesses

// preflightPortFree 是端口占用探测入口（测试可替换）。
var preflightPortFree = checkPortFree

// PreflightCheckResult 单项启动预检结果（FR-314）。
type PreflightCheckResult struct {
	Name    string
	OK      bool
	Message string
}

// PreflightStart 对已注册实例做启动前同步预检（FR-314）：校验 java 运行时、工作目录、启动目标 jar，
// 供 CP 在转 STARTING 前同步拦截配置错误，终结「配置错误也要启动中→崩溃兜一圈」。
// docker 实例整体放行（本地 JDK/文件语义不适用）。实例未注册返回 error。
// 与 Start 内嵌的 preflightJavaVersion 同源复用、并存不冲突（纵深防御，防 CP 绕过与竞态窗口）。
//
// FR-471 在既有三项后补两项**现场占用**检查（docker 仍整体放行）：
//   - work_dir_busy：工作目录下已有活进程（可能为未纳管进程或残留）；
//   - port_free：实例 server-port 已被本机监听。
//
// 真机背景：外部启动的服务器在磁盘上跑、平台记 STOPPED，此时从面板点「启动」就会双开——
// 撞 world/session.lock、抢端口、两个进程写同一批文件。这两项把双开拦在转 STARTING 之前。
func (m *Manager) PreflightStart(uuid string) ([]PreflightCheckResult, error) {
	m.mu.RLock()
	inst, ok := m.instances[uuid]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("实例未注册: %s", uuid)
	}
	// 实例配置字段由 Manager 锁保护（Create/SetLaunchConfig 均在 m.mu 下写），读时持读锁。
	startCmd := inst.StartCommand
	workDir := inst.WorkDir
	jdkPath := inst.JDKPath
	jdkBinPath := inst.JDKBinPath
	serverPort := inst.ServerPort
	ptype := inst.processType
	m.mu.RUnlock()

	if ptype == ProcessTypeDocker {
		return []PreflightCheckResult{{Name: "docker", OK: true, Message: "docker 实例跳过本地启动预检"}}, nil
	}

	return []PreflightCheckResult{
		toPreflightCheck("java_runtime", preflightJavaVersion(CommandSpec{
			StartCommand: startCmd,
			JavaHome:     jdkPath,
			JDKBinPath:   jdkBinPath,
		})),
		toPreflightCheck("work_dir", checkWorkDir(workDir)),
		toPreflightCheck("launch_target", checkLaunchTarget(startCmd, workDir)),
		toPreflightCheck("work_dir_busy", checkWorkDirBusy(workDir)),
		toPreflightCheck("port_free", preflightPortFree(serverPort)),
	}, nil
}

// checkWorkDirBusy 报告工作目录下是否已有活进程（FR-471 防双开）。
//
// 判据与孤儿扫描同源（procInWorkDir）：进程 cwd 落在该目录之下，或其命令行引用了该目录。
// 命中即失败，message 含占用 PID 与处置指引（接管或清理）。
//
// 枚举失败时**不拦启动**（只告警）：本项是「尽力而为的现场探测」，基础设施读取失败（/proc 权限等）
// 不应把正常启动挡在门外；真正的双开仍有端口检查与游戏服自身的 world 锁兜底。
func checkWorkDirBusy(workDir string) error {
	if workDir == "" {
		return nil // 空工作目录由 work_dir 项负责报错
	}
	procs, err := preflightListProcesses()
	if err != nil {
		slog.Warn("启动预检：进程枚举失败，跳过工作目录占用检查", "workDir", workDir, "error", err)
		return nil
	}
	clean := filepath.Clean(workDir)
	// 排除本进程：若实例工作目录恰是 Worker 数据根/其父目录，Worker 自己会被自己的 cwd 误判为占用。
	self := os.Getpid()
	for _, proc := range procs {
		if proc.PID <= 0 || proc.PID == self {
			continue
		}
		if !procInWorkDir(proc, clean) {
			continue
		}
		return fmt.Errorf("工作目录下已有活进程（pid=%d：%s），可能为未纳管进程或残留，请先接管或清理：%s",
			proc.PID, truncateCmdline(proc.Cmdline), clean)
	}
	return nil
}

// checkPortFree 探测端口在本机是否可监听（FR-471 防双开抢端口）。
// 用 net.Listen("tcp", ":<port>") 试绑：成功即空闲（立即 Close 释放，无副作用），失败即视为被占用。
// port<=0（未配置/未知端口）不检查——由 CP 侧的端口分配负责。
func checkPortFree(port int) error {
	if port <= 0 {
		return nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("端口 %d 在本节点已被监听（可能为未纳管进程或残留实例），请先接管或释放该端口后再启动", port)
	}
	_ = ln.Close()
	return nil
}

// toPreflightCheck 把 err 归一为预检项结果：nil→通过，否则失败并带面向用户的原因。
func toPreflightCheck(name string, err error) PreflightCheckResult {
	if err != nil {
		return PreflightCheckResult{Name: name, OK: false, Message: err.Error()}
	}
	return PreflightCheckResult{Name: name, OK: true}
}

// checkWorkDir 校验实例工作目录存在且为目录。
func checkWorkDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("实例工作目录未设置")
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("实例工作目录不存在：%s", dir)
		}
		return fmt.Errorf("无法访问实例工作目录（%s）：%v", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("实例工作目录不是目录：%s", dir)
	}
	return nil
}

// checkLaunchTarget 若启动命令含 `-jar <path>`，校验该 jar（相对工作目录解析）存在；
// 解析不出 jar（自定义命令）则保守放行，不误拦非 -jar 启动方式（宁漏勿误伤）。
func checkLaunchTarget(startCommand, workDir string) error {
	jar := parseJarPath(startCommand)
	if jar == "" {
		return nil
	}
	path := jar
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, jar)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("启动目标 jar 不存在：%s（请确认已放入工作目录或完成搭建）", jar)
		}
		return fmt.Errorf("无法访问启动目标 jar（%s）：%v", jar, err)
	}
	return nil
}

// parseJarPath 从启动命令解析 `-jar` 后的 jar 路径；无则返回空串。
func parseJarPath(cmd string) string {
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f == "-jar" && i+1 < len(fields) {
			return strings.Trim(fields[i+1], `"`)
		}
	}
	return ""
}
