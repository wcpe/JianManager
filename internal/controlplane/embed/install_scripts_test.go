package embed

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestInstallScripts_NonEmpty 内嵌的一键安装脚本不得为空（FR-080 一键安装依赖 CP 托管脚本）。
func TestInstallScripts_NonEmpty(t *testing.T) {
	if len(InstallWorkerScriptSh()) == 0 {
		t.Fatal("内嵌 install-worker.sh 为空")
	}
	if len(InstallWorkerScriptPs1()) == 0 {
		t.Fatal("内嵌 install-worker.ps1 为空")
	}
}

// TestInstallScripts_MatchCanonical 守护单一真源：内嵌副本必须与仓库根 scripts/ 下 canonical
// 脚本字节一致，防止两份漂移（改了 scripts/ 却忘同步内嵌副本，导致 CP 下发旧脚本）。
func TestInstallScripts_MatchCanonical(t *testing.T) {
	// embed 包目录在 internal/controlplane/embed，仓库根为其上溯三级。
	repoRoot := filepath.Join("..", "..", "..")
	cases := []struct {
		name      string
		canonical string
		embedded  []byte
	}{
		{"install-worker.sh", filepath.Join(repoRoot, "scripts", "install-worker.sh"), InstallWorkerScriptSh()},
		{"install-worker.ps1", filepath.Join(repoRoot, "scripts", "install-worker.ps1"), InstallWorkerScriptPs1()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := os.ReadFile(tc.canonical)
			if err != nil {
				t.Fatalf("读取 canonical 脚本 %s 失败: %v", tc.canonical, err)
			}
			if !bytes.Equal(want, tc.embedded) {
				t.Fatalf("内嵌 %s 与 canonical %s 字节不一致（需 make embed-install-scripts 同步）",
					tc.name, tc.canonical)
			}
		})
	}
}
