package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// 交互式选择的目标（宿主或自定义目录）。
//
// 与 detectHosts / hostSkillRoot 的分工：那两个只负责「知道有哪些宿主、各自装到哪」，
// 这里负责「问用户想装哪一个」。分开是因为选择逻辑在非交互场景完全不参与，
// 不该把提示语混进探测函数里。

// skillTarget 是一个候选安装位置。
type skillTarget struct {
	// Label 展示给用户的名称（如 "claude"）；自定义目录为空。
	Label string
	// Path 实际安装目录（已是 skills 根下的 jianmanager 子目录）。
	Path string
	// Detected 表示该宿主在本机已存在配置目录。
	Detected bool
}

// isInteractive 判断当前是否可与之交互。
//
// 两道判断都要过：stdin 与 stdout 都是字符设备（TTY）。
// 只看 stdin 不够——`jm skill install | tail` 时 stdout 是管道，
// 提示语会被吞进管道，用户看不到菜单却要盲输，比不交互更糟。
func isInteractive() bool {
	if os.Getenv("JM_NO_PROMPT") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// buildTargets 汇总全部候选安装位置。
//
// 顺序：已探测到的宿主在前（用户最可能选这些），其余已知宿主排后，
// 最后附「自定义目录」。这样默认序号 1 就是最常用的那个。
func buildTargets(detected []string) []skillTarget {
	detectedSet := make(map[string]bool, len(detected))
	for _, h := range detected {
		detectedSet[h] = true
	}

	var out []skillTarget
	for _, h := range detected {
		if root, err := hostSkillRoot(h); err == nil {
			out = append(out, skillTarget{Label: h, Path: joinSkillDir(root), Detected: true})
		}
	}
	for _, h := range []string{"claude", "codex", "cursor"} {
		if detectedSet[h] {
			continue
		}
		if root, err := hostSkillRoot(h); err == nil {
			out = append(out, skillTarget{Label: h, Path: joinSkillDir(root), Detected: false})
		}
	}
	return out
}

// promptTargets 让用户选择安装位置，返回选中的目标。
//
// 交互约定：
//   - 直接回车 = 装到全部「已探测到」的宿主（保持旧行为，零输入即可完成）
//   - 输入序号（可多个，空格/逗号分隔）= 只装选中的
//   - 输入 new = 手动输入一个目录
//   - 输入 q = 取消
func promptTargets(targets []skillTarget) ([]skillTarget, string, error) {
	if len(targets) == 0 {
		return nil, "", fmt.Errorf("本机没有可用的宿主目录")
	}

	fmt.Println("选择安装位置：")
	for i, t := range targets {
		mark := " "
		if t.Detected {
			mark = "*"
		}
		fmt.Printf("  %s %d) %-8s %s\n", mark, i+1, t.Label, t.Path)
	}
	fmt.Printf("    %d) 自定义目录\n", len(targets)+1)
	fmt.Println()
	fmt.Println("  * = 本机已有该宿主")
	fmt.Println("  直接回车装到全部带 * 的宿主；输入序号可多选（如 1 3）；new 自定义；q 取消")
	fmt.Print("\n> ")

	line, err := readLine()
	if err != nil {
		return nil, "", err
	}
	line = strings.TrimSpace(line)

	if line == "" {
		var picked []skillTarget
		for _, t := range targets {
			if t.Detected {
				picked = append(picked, t)
			}
		}
		if len(picked) == 0 {
			return nil, "", fmt.Errorf("没有已探测到的宿主可装；请显式输入序号或 new")
		}
		return picked, "", nil
	}

	switch strings.ToLower(line) {
	case "q", "quit", "exit":
		return nil, "", errUserCancelled
	case "new":
		fmt.Print("安装目录: ")
		dir, err := readLine()
		if err != nil {
			return nil, "", err
		}
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return nil, "", fmt.Errorf("目录不能为空")
		}
		return nil, dir, nil
	}

	// 序号多选：容忍空格、逗号、分号混用。
	var picked []skillTarget
	seen := make(map[int]bool)
	for _, field := range strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t'
	}) {
		n, err := strconv.Atoi(field)
		if err != nil {
			return nil, "", fmt.Errorf("无法识别 %q：请输入序号、new 或 q", field)
		}
		if n == len(targets)+1 {
			fmt.Print("安装目录: ")
			dir, err := readLine()
			if err != nil {
				return nil, "", err
			}
			if dir = strings.TrimSpace(dir); dir == "" {
				return nil, "", fmt.Errorf("目录不能为空")
			}
			return nil, dir, nil
		}
		if n < 1 || n > len(targets) {
			return nil, "", fmt.Errorf("序号 %d 超出范围（1-%d）", n, len(targets)+1)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		picked = append(picked, targets[n-1])
	}
	if len(picked) == 0 {
		return nil, "", fmt.Errorf("未选择任何目标")
	}
	return picked, "", nil
}

// errUserCancelled 表示用户主动取消（非错误，退出码按成功处理）。
var errUserCancelled = fmt.Errorf("已取消")

// readLine 读一行输入（去掉行尾换行）。
//
// 逐字节读到 '\n' 为止，不预读：bufio.Reader 会一次性把后续内容吸入自身缓冲，
// 导致「问完选项再问目录」时第二次读不到数据（MinTTY 下更明显，会表现为卡住）。
// 逐字节虽然慢，但一次交互只有几十个字符，代价可忽略。
func readLine() (string, error) {
	var sb strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			if buf[0] != '\r' {
				sb.WriteByte(buf[0])
			}
		}
		if err != nil {
			if sb.Len() == 0 {
				return "", err
			}
			break
		}
	}
	return sb.String(), nil
}
