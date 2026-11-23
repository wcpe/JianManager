package process

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// 平台口径：**Windows 上收养必须 fail-closed**（只告警、不收养、不杀）。
//
// 这份用例的判据放在无构建约束的文件里，Linux 与 Windows 都编译、都运行；它断言的东西在两平台上
// **完全一致**，因此本机（Linux）跑它也是有效证据，而不是「假装验过 Windows」。
//
// 事实与取向（spec §2.1 / §5）：Windows 没有 `/proc`，`readProcessEnviron` 恒失败 ⇒
// `processEnvMatchesInstance` 恒 false ⇒
//   - wrapper 分支：`daemon_argv+env_uuid` 这条唯一通过路径不可能成立 ⇒ 恒被拒（`env_uuid_missing`）；
//   - Java 分支：环境强特征不可能成立，只剩 cmdline 含 UUID 一条路（极少成立）；否则一律拒。
//
// 这正是规格刻意的取舍：**宁可不收养，绝不误纳管**。代价是 Windows 上收养不生效、退回原来的
//「只告警」行为——这一点在 spec §5 已如实登记为已知降级，本用例把它钉成可回归的契约。
//
// 能在这份用例里断言、而**不能**在 Linux 上验证的部分（真机才能验，尚未验证，见报告）：
//   - `IsPIDAlive` 走 OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)，与 signal 0 的语义差异；
//   - `taskkill /T /F` 的进程树终止范围与异步性；
//   - Windows 上 PID 复用判据（`GetProcessTimes` 创建时刻在本项目里并未参与归属判定）；
//   - `samePath` 在 Windows 上**大小写不敏感**（`C:\Foo` 与 `c:\foo` 是同一路径）——当前实现走
//     filepath.Abs + 直接字符串比较，大小写差异会被判成不同路径。这是弱特征判定，
//     只会让「cwd 命中」更保守（少判真），方向与 fail-closed 一致，故本次不做改动；
//     但它会进 `DefaultVerifyProcessOwnership` 的宽口径（强杀路径），属既有行为，记录待后续处理。

// TestVerifyAdoptOwnership_NoEnvironFailsClosed 环境读不到时**两个分支都不得判真**。
//
// 构造：用一个几乎必然不存在的 PID 反例已经不够（那会先命中 process_unreadable）；
// 这里直接用 self 进程——它一定可读，但它的环境里不会有下面这个凭空 UUID。
func TestVerifyAdoptOwnership_NoEnvironFailsClosed(t *testing.T) {
	const uuid = "deadbeef-0000-1111-2222-333333333333"
	cwd, err := os.Getwd()
	assert.NoError(t, err)

	// Java 分支：self 进程 cwd 恰是传入 workDir（弱特征成立）但环境无该 UUID。
	ok, evidence := DefaultVerifyAdoptOwnership(os.Getpid(), uuid, cwd, false)
	assert.False(t, ok, "环境无本实例 UUID 时不得判属")
	assert.Equal(t, "weak_evidence_only", evidence)

	// wrapper 分支：self 进程 argv 不是 daemon ⇒ 先撞 argv 门。
	ok, evidence = DefaultVerifyAdoptOwnership(os.Getpid(), uuid, cwd, true)
	assert.False(t, ok)
	assert.Equal(t, "wrapper_argv_mismatch", evidence)
}

// TestContainsUUIDToken_Boundaries UUID token 边界判定的跨平台纯函数用例。
//
// 归属复核的强特征判据在 Windows 上虽然走不到（环境读不到），但 `containsUUIDToken` 也会被
// Java 分支的 **cmdline** 强特征用到（cmdline 在 Windows 上可读）。故这条判据的边界语义
// 必须两平台一致，且必须对 Windows 的路径形态（反斜杠、盘符、大小写）不误判。
func TestContainsUUIDToken_Boundaries(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"单独出现", uuid, true},
		{"JSON 值中", `{"instance_uuid":"` + uuid + `"}`, true},
		{"命令行参数", `java -Djm.instance=` + uuid + ` -jar server.jar`, true},
		{"Windows 命令行", `C:\Java\bin\java.exe -Djm.instance=` + uuid + ` -jar C:\srv\server.jar`, true},
		{"后接 hex", uuid + "aa", false},
		{"前缀 hex", "aa" + uuid, false},
		{"后缀连字符串", uuid + "-older", false},
		{"被路径分隔包裹", `C:\srv\` + uuid + `\server.jar`, true},
		{"大写下一位 hex", uuid + "A", false},
		{"恰好等于值", "X=" + uuid, true},
		{"空串", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, containsUUIDToken(c.in, uuid), "in=%q", c.in)
		})
	}
}
