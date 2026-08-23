package process

import (
	"fmt"
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
	}, nil
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
