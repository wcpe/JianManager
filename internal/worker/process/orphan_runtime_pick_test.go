package process

import "testing"

// TestLooksLikeRuntimeCmdline 运行时命令行判据。
//
// 这个判据决定孤儿扫描在“目录里同时有 tmux shell 与 java”时选谁做代表——
// 选错会让接管拿 `-bash` 去比启动命令，直接判不匹配而拒绝收敛（真实机踩过）。
func TestLooksLikeRuntimeCmdline(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"真实 paper 服", "/opt/graalvm/bin/java -Xms2G -Xmx4G -jar paper.jar --nogui", true},
		{"带路径 jar", "java -jar /srv/mc/game-1/paper-1.20.1-196.jar nogui", true},
		{"基岩版", "./bedrock_server", true},
		{"velocity 代理", "java -jar velocity-3.1.1.jar", true},
		{"forge 核心", "java @libraries/forge/args.txt", true},
		{"tmux 的 bash（关键反例）", "-bash", false},
		{"启动脚本包装", "/bin/sh start.sh", false},
		{"tee 管道", "tee -a console.log", false},
		{"空命令行", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeRuntimeCmdline(c.cmdline); got != c.want {
				t.Fatalf("cmdline=%q got=%v want=%v", c.cmdline, got, c.want)
			}
		})
	}
}
