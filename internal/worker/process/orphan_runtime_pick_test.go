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

// TestLooksLikeRuntimeExecutable 「可执行体像运行时」判据（启动预检专用，比上面的宽口径严格）。
//
// 与 TestLooksLikeRuntimeCmdline 的关键差异就在后两组：`.jar`/`paper` 只作为**参数文件名**出现时
// 不再算运行时——否则「往工作目录拷贝 paper.jar 的那条 cp 命令」会被预检判成活进程（issue #101）。
func TestLooksLikeRuntimeExecutable(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"java 带完整路径", "/opt/graalvm/bin/java -Xms2G -jar paper.jar --nogui", true},
		{"Windows 带引号且路径含空格", `"C:\Program Files\Java\jdk-21\bin\java.exe" -jar server.jar`, true},
		{"javaw 无控制台", `C:\jdk\bin\javaw.exe -jar server.jar`, true},
		{"基岩版相对路径", "./bedrock_server", true},
		{"基岩版带连字符", "/srv/bedrock-server --dir /srv/world", true},
		{"拷贝命令（关键反例）", "bash -c 'cp /tmp/x /srv/pa/paper.jar'", false},
		{"仅参数里出现 jar", "vim /srv/pa/server.jar", false},
		{"tmux 的 bash", "-bash", false},
		{"空命令行", "", false},
		{"只有空白", "   ", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeRuntimeExecutable(c.cmdline); got != c.want {
				t.Fatalf("cmdline=%q got=%v want=%v", c.cmdline, got, c.want)
			}
		})
	}
}

// TestCmdlineExecutableBase 可执行体基名提取：去引号、去目录、去 .exe 后缀，兼容 Windows 反斜杠。
func TestCmdlineExecutableBase(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		want    string
	}{
		{"裸名", "java -jar server.jar", "java"},
		{"Unix 绝对路径", "/opt/jdk-21/bin/java -version", "java"},
		{"Windows 反斜杠路径", `C:\jdk\bin\java.exe -jar server.jar`, "java"},
		{"Windows 引号路径含空格", `"C:\Program Files\Java\bin\java.exe" -jar server.jar`, "java"},
		{"单引号", `'java' -jar server.jar`, "java"},
		{"未闭合引号", `"java -jar server.jar`, "java -jar server.jar"},
		{"空命令行", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cmdlineExecutableBase(c.cmdline); got != c.want {
				t.Fatalf("cmdline=%q got=%q want=%q", c.cmdline, got, c.want)
			}
		})
	}
}
