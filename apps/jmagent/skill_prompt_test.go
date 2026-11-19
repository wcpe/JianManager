package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildTargets 的排序与标记：已探测宿主在前且 Detected=true。
func TestBuildTargetsOrderAndFlags(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无主目录")
	}

	targets := buildTargets([]string{"codex"})
	if len(targets) != 3 {
		t.Fatalf("应列出 3 个宿主，实际 %d", len(targets))
	}
	if targets[0].Label != "codex" || !targets[0].Detected {
		t.Errorf("首个应为已探测的 codex，实际 %+v", targets[0])
	}
	if targets[0].Path != filepath.Join(home, ".codex", "skills", "jianmanager") {
		t.Errorf("codex 路径不对: %s", targets[0].Path)
	}
	// 其余未探测，Detected 必须为 false，否则「回车装全部 * 」会误装。
	for _, tg := range targets[1:] {
		if tg.Detected {
			t.Errorf("%s 不应标记为已探测", tg.Label)
		}
	}
}

// 空输入（直接回车）= 选中全部已探测宿主。
func TestPromptTargetsEnterPicksDetected(t *testing.T) {
	targets := []skillTarget{
		{Label: "claude", Path: "/x/claude", Detected: true},
		{Label: "codex", Path: "/x/codex", Detected: false},
	}
	picked, custom, err := promptWithInput(t, targets, "\n")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if custom != "" {
		t.Errorf("不应返回自定义目录，得到 %q", custom)
	}
	if len(picked) != 1 || picked[0].Label != "claude" {
		t.Fatalf("应只选中 claude，实际 %+v", picked)
	}
}

// 序号多选，容忍空格与逗号混用。
func TestPromptTargetsNumberSelection(t *testing.T) {
	targets := []skillTarget{
		{Label: "claude", Path: "/x/claude", Detected: true},
		{Label: "codex", Path: "/x/codex", Detected: true},
		{Label: "cursor", Path: "/x/cursor", Detected: false},
	}
	picked, _, err := promptWithInput(t, targets, "1, 3\n")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(picked) != 2 {
		t.Fatalf("应选中 2 个，实际 %d", len(picked))
	}
	if picked[0].Label != "claude" || picked[1].Label != "cursor" {
		t.Errorf("选中项不对: %+v", picked)
	}
}

// 重复序号只算一次。
func TestPromptTargetsDedupSelection(t *testing.T) {
	targets := []skillTarget{{Label: "claude", Path: "/x/c", Detected: true}}
	picked, _, err := promptWithInput(t, targets, "1 1 1\n")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(picked) != 1 {
		t.Errorf("重复序号应去重，实际 %d 个", len(picked))
	}
}

// 输入 new 后跟着自定义目录。
func TestPromptTargetsCustomDir(t *testing.T) {
	targets := []skillTarget{{Label: "claude", Path: "/x/c", Detected: true}}
	_, custom, err := promptWithInput(t, targets, "new\nD:/my/skills\n")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if custom != "D:/my/skills" {
		t.Errorf("自定义目录 = %q", custom)
	}
}

// 最后一项序号等价于「自定义目录」。
func TestPromptTargetsLastIndexIsCustom(t *testing.T) {
	targets := []skillTarget{{Label: "claude", Path: "/x/c", Detected: true}}
	_, custom, err := promptWithInput(t, targets, "2\n/tmp/skills\n")
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if custom != "/tmp/skills" {
		t.Errorf("最后一项应为自定义目录，得到 %q", custom)
	}
}

// q / quit / exit 都表示取消。
func TestPromptTargetsCancel(t *testing.T) {
	targets := []skillTarget{{Label: "claude", Path: "/x/c", Detected: true}}
	for _, in := range []string{"q\n", "quit\n", "exit\n", "Q\n"} {
		_, _, err := promptWithInput(t, targets, in)
		if err != errUserCancelled {
			t.Errorf("输入 %q 应返回取消，实际 %v", strings.TrimSpace(in), err)
		}
	}
}

// 越界与非法输入必须报错，而不是静默装错地方。
func TestPromptTargetsInvalidInput(t *testing.T) {
	targets := []skillTarget{{Label: "claude", Path: "/x/c", Detected: true}}
	if _, _, err := promptWithInput(t, targets, "9\n"); err == nil {
		t.Error("越界序号应报错")
	}
	if _, _, err := promptWithInput(t, targets, "abc\n"); err == nil {
		t.Error("非数字应报错")
	}
}

// 没有任何已探测宿主时，回车不能静默成功。
func TestPromptTargetsEnterWithNoDetected(t *testing.T) {
	targets := []skillTarget{{Label: "claude", Path: "/x/c", Detected: false}}
	if _, _, err := promptWithInput(t, targets, "\n"); err == nil {
		t.Error("无已探测宿主时回车应报错")
	}
}

// 非交互环境（JM_NO_PROMPT）一律不提示。
func TestIsInteractiveRespectsEnv(t *testing.T) {
	t.Setenv("JM_NO_PROMPT", "1")
	if isInteractive() {
		t.Error("设置了 JM_NO_PROMPT 时不应判定为交互")
	}
}

// promptWithInput 用临时文件替换 stdin，喂入指定内容后执行 promptTargets。
func promptWithInput(t *testing.T, targets []skillTarget, input string) ([]skillTarget, string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old; f.Close() })

	// 提示输出到真实 stdout 会污染测试日志；这里只是容忍，不重定向。
	return promptTargets(targets)
}
