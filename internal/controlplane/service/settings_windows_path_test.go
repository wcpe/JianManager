package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 本文件是 CP 侧「Windows 形态路径」判据的回归证据。
//
// 为什么 CP 需要自己的一套判据：CP 通常跑 Linux、节点可能跑 Windows。`filepath` 在 CP 上
// 是 POSIX 语义（编译期按 GOOS 绑定），对 `C:\logs\a.log`、`\\server\share\x` 的
// `filepath.IsAbs` 返回 false，对 `..\..\etc\passwd` 的 `filepath.Clean` 也不会把反斜杠
// 当分隔符——于是一律放行、返回 200。可这些值下发到 Windows 节点后会被 Worker 拒绝登记，
// 用户看到的是「保存成功但实例日志登记失败」，而错误现场在另一台机器上，极难排查。
// 故 CP 必须在**写入侧**就用 Windows 语义提前拒掉，避免「保存成功、实际不生效」。
//
// 判据本身是纯字符串函数（不依赖宿主 GOOS），因此在 Linux 上跑测试即可覆盖 Windows 语义。

// TestIsWindowsAbsPathSemantics 固定「Windows 盘符/UNC」判据的真值表。
func TestIsWindowsAbsPathSemantics(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
		why  string
	}{
		{"盘符+反斜杠", `C:\logs\a.log`, true, "盘符形态在 Linux 的 filepath.IsAbs 上是 false，必须靠本判据拦"},
		{"盘符+正斜杠", `C:/logs/a.log`, true, "Windows 同时认 /，不得只认反斜杠"},
		{"小写盘符", `d:\logs\a.log`, true, "盘符大小写不敏感"},
		{"UNC 反斜杠", `\\server\share\x`, true, "UNC 在 Windows 上必是绝对路径"},
		{"根相对单反斜杠", `\logs\a.log`, true, "前导单反斜杠是「当前盘根相对」，指向盘根而非工作目录；Go 的 filepath.IsAbs 反而判 false"},
		{"盘符相对也拒", `C:relative`, true, "盘符相对（相对当前盘工作目录）同样不指向工作目录内；CP 按 `X:` 前缀整体拒绝"},
		{"裸盘符", `C:`, true, "裸盘符无意义，一律拒"},
		{"POSIX 绝对路径不归本判据", `/etc/shadow`, false, "由 isPOSIXAbsPath 负责（职责分离，本判据不重复）"},
		{"逃逸不归本判据", `..\..\etc\passwd`, false, "由 escapesUnderWindowsSeparators 负责"},
		{"普通相对", `logs/latest.log`, false, ""},
		{"Windows 相对", `logs\latest.log`, false, "反斜杠分隔但不越界，应放行"},
		{"冒号不在首部", `logs/a:b.log`, false, "中段冒号不构成盘符"},
		{"空串", ``, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isWindowsAbsPath(tc.path), "%s（path=%q）", tc.why, tc.path)
		})
	}
}

// TestIsPOSIXAbsPathSemantics 固定「POSIX 绝对路径」判据的真值表，并锁死它**不依赖宿主 GOOS**。
//
// 这条判据存在的理由（2026-11-16）：原实现用 `filepath.IsAbs(filepath.FromSlash(p))` 拒绝对路径，
// 但 `path/filepath` 的语义是**编译期**按 GOOS 绑定的，于是同一份校验在 linux 与 windows 两版
// CP 上结论不同：Windows 版把 `/etc/shadow` 经 FromSlash 变成 `\etc\shadow`，而 Windows 的
// volumeNameLen 不认「单分隔符开头」为卷名 → IsAbs 返回 false → 该值落库放行。
// 而它下发给 Windows 节点后，Worker 会照读宿主机任意可读文件，CP 的 trust 边界形同虚设。
//
// 因此本判据必须纯粹按字符串判定，不接受任何 filepath / os 包的参与。
// 下面的「宿主无关性」子测试用 Linux 上可算的另一种方言反证：它只应看首字符。
func TestIsPOSIXAbsPathSemantics(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
		why  string
	}{
		{"经典绝对", `/etc/shadow`, true, "首字符 `/` 即绝对，与宿主 GOOS 无关"},
		{"根目录", `/`, true, ""},
		{"盘符形态归 Windows 判据", `C:\logs\a.log`, false, "由 isWindowsAbsPath 负责，本判据不重复"},
		{"盘符相对归 Windows 判据", `C:relative`, false, "同上"},
		{"UNC 反斜杠归 Windows 判据", `\\server\share\x`, false, "由 isWindowsAbsPath 负责"},
		{"普通相对", `logs/latest.log`, false, ""},
		{"点开头的相对", `./logs/a.log`, false, "`./` 是相对写法，不得误拒"},
		{"父目录逃逸不归本判据", `../etc/shadow`, false, "由 escapesUnderWindowsSeparators 与 Clean 负责"},
		{"反斜杠相对", `logs\latest.log`, false, "运维自然写法必须放行"},
		{"空串", ``, false, "空串由「值不能为空」单独一道拒"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isPOSIXAbsPath(tc.path), "%s（path=%q）", tc.why, tc.path)
		})
	}
}

// TestValidateSettingValueRejectsPOSIXAbsPathOnAnyHost 是**根因回归**：
// 在 Windows 版 CP 上，这条值原本会因 filepath.IsAbs 的 GOOS 绑定而落库放行。
//
// 本用例在 Linux 上跑时新旧实现都通过，看似无用；但它是 Windows 版 `go test ./internal/controlplane/service/`
// 的红/绿分界——没有它，判据回退到 filepath.IsAbs 不会被任何 Linux CI 拦住（因为 Linux 上 IsAbs 恰好正确）。
// 故保留并注明：**不要把它改写成直接调 filepath.IsAbs 的形态**。
func TestValidateSettingValueRejectsPOSIXAbsPathOnAnyHost(t *testing.T) {
	for _, val := range []string{
		`{"inst:1":"/etc/shadow"}`,
		`{"inst:1":"/var/log/syslog"}`,
		`{"inst:1":"/"}`,
	} {
		require.ErrorIs(t, validateSettingValue(SettingKeyLogAcqPathOverrides, val), ErrSettingValueInvalid,
			"POSIX 绝对路径在**任何**宿主上都须被拒（否则 Windows 版 CP 会静默放行）：%s", val)
	}
}

// TestEscapesUnderWindowsSeparators 固定「按 Windows 分隔符折叠 ..」判据的真值表。
//
// 这是 CP 侧此前真实放行的一类：`filepath.Clean` 在 Linux 上把反斜杠当普通字符，
// `..\..\etc\passwd` 因此不带 `../` 前缀、通过了宿主判据。
func TestEscapesUnderWindowsSeparators(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"反斜杠逃逸", `..\..\etc\passwd`, true},      // 改造前在 Linux 上被放行
		{"中段越界", `logs\..\..\x`, true},           // 先入后出仍下穿到根外
		{"单个 ..", `..`, true},                    //
		{"混用分隔符", `logs/..\..\x`, true},          // 混用不得成为绕过路径
		{"正斜杠逃逸", `../../etc/passwd`, true},      // 既有防护，不得因改造放松
		{"内部回退", `logs\..\a.log`, false},         // 回退但未越根，应放行
		{"Windows 相对", `logs\latest.log`, false}, // 运维自然写法必须放行
		{"POSIX 相对", `logs/latest.log`, false},
		{"点段冗余", `.\logs\.\latest.log`, false},
		{"空串", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, escapesUnderWindowsSeparators(tc.path), "path=%q", tc.path)
		})
	}
}

// TestValidateSettingValueRejectsWindowsShapedPathOverride 在**值校验入口**上验证
// path_overrides 的 Windows 形态值被拒。
//
// 走 validateSettingValue 而不是直接调判据：真正的契约是「这类值进不了库」，
// 判据只是实现手段；用入口测才能防止将来重排校验顺序时把某一道悄悄跳过。
func TestValidateSettingValueRejectsWindowsShapedPathOverride(t *testing.T) {
	rejected := []struct {
		name string
		val  string
	}{
		{"盘符绝对路径", `{"inst:1":"C:\\logs\\a.log"}`},
		{"UNC 路径", `{"inst:1":"\\\\server\\share\\x"}`},
		{"盘符相对路径", `{"inst:1":"C:relative"}`},
		{"反斜杠逃逸", `{"inst:1":"..\\..\\etc\\passwd"}`}, // 改造前放行，CP 侧主要缺口
		{"反斜杠中段越界", `{"inst:1":"logs\\..\\..\\x"}`},
		{"混用分隔符逃逸", `{"inst:1":"logs/..\\..\\x"}`},
		{"POSIX 经典逃逸", `{"inst:1":"../../etc/passwd"}`}, // 既有防护
		{"POSIX 中段越界", `{"inst:1":"logs/../../../x"}`},  // 既有防护
		{"POSIX 绝对路径", `{"inst:1":"/etc/shadow"}`},      // 既有防护
	}
	for _, tc := range rejected {
		t.Run("拒绝/"+tc.name, func(t *testing.T) {
			require.ErrorIs(t, validateSettingValue(SettingKeyLogAcqPathOverrides, tc.val), ErrSettingValueInvalid,
				"该值须被拒（否则会在 Windows 节点上表现为登记失败）：%s", tc.val)
		})
	}

	accepted := []struct {
		name string
		val  string
	}{
		{"POSIX 相对", `{"inst:1":"logs/latest.log"}`},
		{"Windows 相对（非逃逸）", `{"inst:1":"logs\\latest.log"}`}, // 反斜杠分隔但不越界，不得误伤
		{"规则默认形态", `{"inst:1":"proxy.log.0"}`},
		{"多实例混合", `{"inst:1":"logs/latest.log","inst:2":"sub/dir/a.log"}`},
	}
	for _, tc := range accepted {
		t.Run("放行/"+tc.name, func(t *testing.T) {
			require.NoError(t, validateSettingValue(SettingKeyLogAcqPathOverrides, tc.val),
				"合法相对路径不得被误拒：%s", tc.val)
		})
	}
}

// TestUpdateRejectsWindowsShapedPathOverrideWithoutPersisting 验证被拒的值**不落库**。
//
// 为什么要单独一条：只断言返回错误还不够——若校验发生在落库之后，库里的脏值仍会被心跳
// 下发，症状（Windows 节点登记失败）照旧发生，而用户以为保存失败已经拦住了。
func TestUpdateRejectsWindowsShapedPathOverrideWithoutPersisting(t *testing.T) {
	for _, val := range []string{
		`{"inst:1":"..\\..\\etc\\passwd"}`,
		`{"inst:1":"C:\\logs\\a.log"}`,
	} {
		svc := NewSettingsService(newSettingsTestDB(t), testConfig())
		require.ErrorIs(t, svc.Update(map[string]string{SettingKeyLogAcqPathOverrides: val}), ErrSettingValueInvalid)
		require.Empty(t, svc.EffectiveValue(SettingKeyLogAcqPathOverrides), "被拒绝的值不应落库：%s", val)
	}
}
