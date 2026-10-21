package ingest

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 本文件是「Windows 路径语义」的回归证据（此前一直标注为证据缺失/未测）。
//
// 背景：Go 的 path/filepath 在**编译期**按 GOOS 绑定实现，Linux 上跑的 `go test` 永远
// 只能观测 POSIX 语义——`filepath.IsAbs("C:\logs\a.log")` 返回 false、
// `filepath.Clean("..\..\etc\passwd")` 把反斜杠当普通字符。因此「用 filepath 的判据」
// 在 Linux 上**测不到** Windows 行为，这不是用例写得不够，而是可测性问题。
// 解法是把方言（分隔符集合 + 是否认卷名）抽成纯数据，判据只读这份数据、不碰 filepath，
// 于是同一份用例在任一宿主上都能覆盖两种语义（见 instances.go 的 pathDialect）。

// TestIsAbsWindowsSemantics 固定「按 Windows 语义判绝对路径」的真值表。
//
// 判据须与 GOOS=windows 下 filepath.IsAbs 一致，否则 Worker 在 Windows 节点上的拒绝
// 面与 CP 的写入校验会分叉：CP 放行的值到节点上被拒 → 「保存成功但登记失败」。
func TestIsAbsWindowsSemantics(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
		why  string
	}{
		{"盘符+反斜杠", `C:\logs\a.log`, true, "盘符后紧跟分隔符 = 绝对路径"},
		{"盘符+正斜杠", `C:/logs/a.log`, true, "Windows 内核同时接受 / 与 \\，不得只认反斜杠"},
		{"小写盘符", `d:\logs\a.log`, true, "盘符大小写不敏感"},
		{"UNC 反斜杠", `\\server\share\x`, true, "前两字节皆为分隔符 = UNC（host+share 即卷名）"},
		{"UNC 正斜杠", `//server/share/x`, true, "Windows 上 //host/share 同样是 UNC，Go 的 IsAbs 亦为 true"},
		{"设备路径", `\\?\C:\logs\a.log`, true, "设备前缀同属 UNC 形态"},
		{"盘符相对不是绝对", `C:relative`, false, "Windows 的 IsAbs 对 C:relative 返回 false（相对当前盘工作目录）"},
		{"裸盘符不是绝对", `C:`, false, "Go 的 Windows IsAbs 对裸盘符返回 false"},
		{"根相对不是绝对", `\logs\a.log`, false, "Windows 的 IsAbs 对根相对路径返回 false（需盘符才算绝对）"},
		{"纯相对 POSIX 形态", `logs/a.log`, false, "普通相对路径"},
		{"纯相对 Windows 形态", `logs\a.log`, false, "反斜杠只是分隔符，不构成绝对"},
		{"逃逸形态不是绝对", `..\..\etc\passwd`, false, "相对路径，由 escapesRoot 负责拒绝（职责分离）"},
		{"空串", "", false, "空串合法，表示用规则默认"},
		{"盘符在中间", `logs/C:/x`, false, "卷名只在首部有意义，中段冒号不算盘符"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isAbsWindows(tc.path), "%s（path=%q）", tc.why, tc.path)
		})
	}
}

// TestHasWindowsVolumeSemantics 固定「是否带 Windows 卷名」的真值表。
//
// 单独一轮的原因：`C:relative` 与 `C:` 是**带卷名但不是绝对路径**的形态，
// isAbsWindows 按 Windows 语义必须返回 false，可它们同样不得作为实例级日志路径
// （CP 侧 isWindowsAbsPath 早已按 `X:` 前缀整体拒绝）。若并入 isAbsWindows，
// 就会为了拦住 C:relative 而谎报 IsAbs 语义，两边判据再次分叉。
func TestHasWindowsVolumeSemantics(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{`C:relative`, true}, // 盘符相对：不指向工作目录内任何一处
		{`C:`, true},
		{`C:\logs\a.log`, true},
		{`\\server\share\x`, true},
		{`//server/share`, true},
		{`\logs\a.log`, false}, // 根相对：无卷名（与 Go 的 volumeNameLen 一致）
		{`logs\a.log`, false},
		{`logs/a.log`, false},
		{`logs/a:b.log`, false}, // 冒号不在首部第 2 字节，不构成盘符
		{``, false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			require.Equal(t, tc.want, hasWindowsVolume(tc.path))
		})
	}
}

// TestEscapesRootUnderBothDialects 固定两套方言下的逃逸判定，并显式记录两者的**不对称**：
// `logs\..\..\x` 在 POSIX 语义下不是逃逸（反斜杠只是普通字符），在 Windows 语义下必须拒绝。
// 这条不对称正是「只在 Linux 上测」必然漏掉的那一支。
func TestEscapesRootUnderBothDialects(t *testing.T) {
	const (
		posix   = "POSIX"
		windows = "Windows"
	)
	cases := []struct {
		name    string
		path    string
		dialect string
		want    bool
		why     string
	}{
		// —— POSIX 语义 ——
		{"POSIX 经典逃逸", `../../etc/passwd`, posix, true, "改造前后都必须拒绝，不得因新增判据而放松"},
		{"POSIX 多个 ..", `../../../etc/passwd`, posix, true, ""},
		{"POSIX 单个 ..", `..`, posix, true, ""},
		{"POSIX 中段越界", `logs/../../../etc/shadow`, posix, true, "先入后出仍下穿到根外"},
		{"POSIX 反斜杠非分隔符", `logs\..\..\x`, posix, false, "POSIX 里反斜杠是普通字符，这一串本身不逃逸（由 Windows 方言那一轮拒绝）"},
		{"POSIX 内部回退", `logs/../a.log`, posix, false, "回退但未越根，应放行"},
		{"POSIX 点段冗余", `./logs/./latest.log`, posix, false, "`.` 不改变深度"},
		{"POSIX 连分隔符", `logs//latest.log`, posix, false, "空段忽略"},

		// —— Windows 语义 ——
		{"Windows 反斜杠逃逸", `..\..\etc\passwd`, windows, true, "此前无回归证据的那一支"},
		{"Windows 中段越界", `logs\..\..\x`, windows, true, "先入后出仍下穿到根外"},
		{"Windows 单个 ..", `..`, windows, true, ""},
		{"Windows 正斜杠逃逸", `../../etc/passwd`, windows, true, "Windows 同时认 /，故 POSIX 写法在 Windows 上同样逃逸"},
		{"Windows 混用分隔符", `logs/..\..\x`, windows, true, "混用不得成为绕过路径"},
		{"Windows 混用另一方向", `logs\../..\x`, windows, true, ""},
		{"Windows 内部回退", `logs\..\a.log`, windows, false, "回退但未越根，应放行"},
		{"Windows 正常相对", `logs\latest.log`, windows, false, "Windows 运维自然写法必须放行，改造不得误伤"},
		{"Windows 点段冗余", `.\logs\.\latest.log`, windows, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := dialectPOSIX
			if tc.dialect == windows {
				d = dialectWindows
			}
			require.Equal(t, tc.want, escapesRoot(tc.path, d), "path=%q（%s）%s", tc.path, tc.dialect, tc.why)
		})
	}
}

// TestValidateInstanceRelativePathWindowsSemantics 在**Linux 宿主**上验证 Worker 的登记期
// 校验对 Windows 形态输入给出与 Windows 节点一致的结论。
//
// 这是本项缺口的核心用例：改造前这些值在 Linux 上全部被放行（`filepath` 看不到反斜杠
// 与盘符），要到 Windows 节点才失败。
func TestValidateInstanceRelativePathWindowsSemantics(t *testing.T) {
	rejected := []struct {
		name string
		path string
	}{
		{"盘符绝对路径", `C:\logs\a.log`},
		{"盘符+正斜杠", `C:/logs/a.log`},
		{"UNC 路径", `\\server\share\x`},
		{"盘符相对路径", `C:relative`},
		{"裸盘符", `C:`},
		{"反斜杠逃逸", `..\..\etc\passwd`},
		{"反斜杠中段越界", `logs\..\..\x`},
		{"混用分隔符逃逸", `logs/..\..\x`},
		{"POSIX 经典逃逸", `../../etc/passwd`},         // 既有防护，不得因改造而放松
		{"POSIX 中段越界", `logs/../../../etc/shadow`}, // 既有防护
		{"POSIX 绝对路径", `/etc/shadow`},              // 既有防护
		{"首尾空白后仍是绝对", "  C:\\logs\\a.log  "},       // trim 后再判，不留旁路
	}
	for _, tc := range rejected {
		t.Run("拒绝/"+tc.name, func(t *testing.T) {
			// 只走纯字符串层（本判据不碰文件系统）：一条路径进、一个结论出，
			// 断言因此不依赖本机文件系统状态。workDir 传空串即「不做物理落点判定」
			// （第三道链接判据需要比对基准，空基准时按契约跳过）。
			require.Error(t, validateInstanceRelativePath("", tc.path),
				"Windows 语义下该值须被拒：%q", tc.path)
		})
	}

	accepted := []struct {
		name string
		path string
	}{
		{"POSIX 相对", `logs/latest.log`},
		{"Windows 相对（非逃逸）", `logs\latest.log`}, // 反斜杠分隔但不越界：Windows 运维的自然写法
		{"规则默认形态", `proxy.log.0`},
		{"内部回退", `logs/../custom.log`},
		{"空串=用规则默认", ``},
		{"纯空白=用规则默认", "   "},
	}
	for _, tc := range accepted {
		t.Run("放行/"+tc.name, func(t *testing.T) {
			require.NoError(t, validateInstanceRelativePath("", tc.path),
				"合法相对路径不得被误拒：%q", tc.path)
		})
	}
}

// TestInstanceLivePathFallsBackOnWindowsEscapingBinding 验证解析侧的兜底对 Windows 形态的
// 脏数据同样生效：即便绑定里带着 `..\..\etc\passwd`（历史库、人工改库、绕过登记的构造），
// 物理路径也不会跑到工作目录之外，而是回退规则默认。
//
// 与 POSIX 用例（TestInstanceLivePathFallsBackWhenBindingEscapes）互补：那条只覆盖 `/` 语义，
// 在 Linux 上永远是宿主语义命中；这里补 Windows 形态，且不依赖宿主 GOOS。
func TestInstanceLivePathFallsBackOnWindowsEscapingBinding(t *testing.T) {
	workDir := t.TempDir()
	fallback := filepath.Join(workDir, Lookup("").LiveRelative)

	for _, bad := range []string{
		`..\..\etc\passwd`,
		`logs\..\..\x`,
		`C:\logs\a.log`,
		`\\server\share\x`,
		`C:relative`,
	} {
		got := instanceLivePath(InstanceBinding{WorkDir: workDir, RelativePath: bad})
		require.Equal(t, fallback, got, "Windows 形态的越界/绝对绑定须回退规则默认，got %q for %q", got, bad)
	}

	// 对照：Windows 形态的合法相对路径不得被误判为越界，仍按覆盖值解析
	// （在 Linux 宿主上反斜杠只是普通字符，文件名如此——重点是「不回退」这一结论）。
	got := instanceLivePath(InstanceBinding{WorkDir: workDir, RelativePath: `logs\latest.log`})
	require.Equal(t, filepath.Join(workDir, `logs\latest.log`), got,
		"非逃逸的 Windows 相对路径应生效，而不是静默回退")
}
