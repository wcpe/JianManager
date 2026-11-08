package embed

import (
	"bytes"
	"testing"
)

// TestInstallScripts_NonEmpty 内嵌的一键安装脚本不得为空（FR-080 一键安装依赖 CP 托管脚本）。
//
// 空字节会让 CP 的 /install-worker.sh 回 503 INSTALL_SCRIPT_UNAVAILABLE，
// 一键命令 `curl <cp>/install-worker.sh | sh` 直接失败。
func TestInstallScripts_NonEmpty(t *testing.T) {
	if len(InstallWorkerScriptSh()) == 0 {
		t.Fatal("内嵌 install-worker.sh 为空")
	}
	if len(InstallWorkerScriptPs1()) == 0 {
		t.Fatal("内嵌 install-worker.ps1 为空")
	}
}

// TestInstallWorkerScript_KillModeProcess 守护 worker systemd 单元含 KillMode=process。
//
// daemon wrapper 必须在 Worker 重启后存活（ADR-003、FR-341）：缺此则 systemd 默认
// control-group 会连坐 SIGKILL cgroup 内经 setsid 脱离的 wrapper，游戏服随之被杀、
// 命中「清理残留」而非「接管重连」。
//
// 这条断言原本住在 internal/controlplane/embed/install_scripts_test.go，随「嵌入包搬到
// scripts/embed、脚本合并为一份」一起被删——但被删掉的是副本一致性守护（无副本后无对象），
// 这一条是**内容守护**，对象（脚本本身）还在，不能一并消失。故迁到新家。
func TestInstallWorkerScript_KillModeProcess(t *testing.T) {
	body := InstallWorkerScriptSh()
	// 光有这一串不够——必须在 [Service] 段里且是配置行（不是注释），
	// 否则 systemd 根本不认。脚本里恰好有 `# KillMode=process：…` 的注释行，
	// 单用 bytes.Contains 会被它蒙混过关，所以按段+非注释行判定。
	svc := serviceSection(body)
	if svc == nil {
		t.Fatal("install-worker.sh 里找不到 [Service] 段")
	}
	if hasConfigLine(svc, "KillMode=process") {
		return
	}
	// 区分两种失败形态，让排查方向不同：写错位置 vs 压根没写。
	if hasConfigLine(body, "KillMode=process") {
		t.Fatal("KillMode=process 写在 [Service] 段外（写在段外 systemd 不生效）")
	}
	t.Fatal("install-worker.sh 缺少 KillMode=process（FR-341：daemon wrapper 需逃过 systemd cgroup 连坐）")
}

// hasConfigLine 报告块里是否存在以 prefix 开头的**配置行**（跳过空行与 # 注释行）。
func hasConfigLine(block []byte, prefix string) bool {
	for _, line := range bytes.Split(block, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		if bytes.HasPrefix(trimmed, []byte(prefix)) {
			return true
		}
	}
	return false
}

// serviceSection 返回 [Service] 到下一个段头（`[Xxx]`）之间的内容，找不到则 nil。
func serviceSection(body []byte) []byte {
	const head = "[Service]"
	i := bytes.Index(body, []byte(head))
	if i < 0 {
		return nil
	}
	rest := body[i+len(head):]
	// 段头形如行首 '[' …… ']'
	for off := 0; off < len(rest); {
		nl := bytes.IndexByte(rest[off:], '\n')
		if nl < 0 {
			return rest
		}
		line := rest[off : off+nl+1]
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 && trimmed[0] == '[' {
			return rest[:off]
		}
		off += nl + 1
	}
	return rest
}
