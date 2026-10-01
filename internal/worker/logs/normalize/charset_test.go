package normalize

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 缺陷 B 回归（GBK 日志乱码）。转红说明见各用例：每条都对准一个「改动前必然失败」的
// 可观测事实——改动前不做任何解码，GBK 字节一律被 U+FFFD 替换。

func gbk(t *testing.T, text string) string {
	t.Helper()
	encoded, _, err := transform.String(simplifiedchinese.GBK.NewEncoder(), text)
	require.NoError(t, err)
	return encoded
}

func charsetOptions(charset string) Options {
	return Options{
		Source:  logtypes.SourceIdentity{LogSourceID: "src-gbk", SourceGeneration: "g1"},
		Stream:  "stdout",
		Charset: charset,
	}
}

// TestGBKLineDecodesToCorrectUTF8 是缺陷 B 的核心转红闸门。
//
// 现场（2026-10-01/02）：MC 的 Java 日志由中文 locale 的 JVM 以 GBK 写出，
// UI 显示 `[Lodestone] �Ѱַ�ѽ� BC �ʵ�`——即 GBK 字节被按 UTF-8 直读。
//
// 转红：改动前无解码路径，Message 会是含 U+FFFD 的乱码且没有 source_charset 字段 → 必红。
func TestGBKLineDecodesToCorrectUTF8(t *testing.T) {
	// 与现场同型的两行：第二行用于闭合第一行的多行事件。
	first := gbk(t, "[23:54:02] [Server thread/INFO]: [Lodestone] 已解析 BC 实例")
	second := gbk(t, "[23:54:03] [Server thread/INFO]: 下一行")
	res := ProcessLines([]string{first, second}, charsetOptions(""))
	require.Len(t, res.Events, 2)

	ev := res.Events[0]
	// Message 是整行原文（含时间戳与级别前缀），只是字节已被正确解码。
	require.Equal(t, "[23:54:02] [Server thread/INFO]: [Lodestone] 已解析 BC 实例", ev.Message)
	require.NotContains(t, ev.Message, "\uFFFD", "解码后不得残留替换字符")
	require.Equal(t, "gb18030", ev.Fields[FieldSourceCharset],
		"由非 UTF-8 解码而来的事件必须标记源字符集，供审计与历史重写筛选")
	require.NotContains(t, ev.Fields, FieldEncodingSanitized, "成功解码不是净化")
	// 级别与时间戳解析不受中文编码影响（前缀是 ASCII）。
	require.Equal(t, "INFO", ev.Level)
	require.Contains(t, ev.EventTimeUTC, "T23:54:02")
}

// TestUTF8SourceIsNeverMisdetected 守住「合法 UTF-8 零行为变化」。
//
// 转红：若实现改成「先按 GB 解一遍再比对」，合法中文/emoji 会被改写 → 必红。
func TestUTF8SourceIsNeverMisdetected(t *testing.T) {
	lines := []string{
		"[23:54:02] [Server thread/INFO]: 正常中文与 emoji 🚀 ok",
		"[23:54:03] [Server thread/INFO]: second",
	}
	res := ProcessLines(lines, charsetOptions(""))
	require.Len(t, res.Events, 2)
	require.Equal(t, "[23:54:02] [Server thread/INFO]: 正常中文与 emoji 🚀 ok", res.Events[0].Message)
	require.NotContains(t, res.Events[0].Fields, FieldSourceCharset, "合法 UTF-8 不得被标记为已转码")
	require.NotContains(t, res.Events[0].Fields, FieldEncodingSanitized)
}

// TestCorruptBytesAreNotDecodedIntoChinese 守住检测稳定性：
// 损坏字节（孤立高位字节、0xff 0xfe）宁愿走既有净化路径，也不能被当成汉字文本展示。
//
// 转红：若判据只看「非法 UTF-8 就按 GB 解码」，随机字节会变成汉字且无净化标记 → 必红。
func TestCorruptBytesAreNotDecodedIntoChinese(t *testing.T) {
	corrupt := "[23:54:02] [Server thread/INFO]: broken \xff\xfe and \xc4 tail"
	res := ProcessLines([]string{corrupt, "[23:54:03] [Server thread/INFO]: next"}, charsetOptions(""))
	require.Len(t, res.Events, 2)
	ev := res.Events[0]
	require.Contains(t, ev.Message, "\uFFFD")
	require.Equal(t, "true", ev.Fields[FieldEncodingSanitized],
		"无法判定的字节必须走净化路径并留标记")
	require.NotContains(t, ev.Fields, FieldSourceCharset)
}

// TestConfiguredCharsetOverridesDetection 守住「可按源配置/覆盖」：
// 显式声明比自动检测优先，两个方向都要生效。
func TestConfiguredCharsetOverridesDetection(t *testing.T) {
	gbkLine := gbk(t, "[23:54:02] [Server thread/INFO]: 强制解码")
	next := gbk(t, "[23:54:03] [Server thread/INFO]: next")

	// 显式 utf-8：即便字节是 GBK 也不解码（配置即事实，由配置错误暴露而不是静默改文本）。
	forcedUTF8 := ProcessLines([]string{gbkLine, next}, charsetOptions("utf-8"))
	require.Len(t, forcedUTF8.Events, 2)
	require.Contains(t, forcedUTF8.Events[0].Message, "\uFFFD")
	require.Equal(t, "true", forcedUTF8.Events[0].Fields[FieldEncodingSanitized])
	require.NotContains(t, forcedUTF8.Events[0].Fields, FieldSourceCharset)

	// 显式 gbk：按声明解码。
	forcedGBK := ProcessLines([]string{gbkLine, next}, charsetOptions("gbk"))
	require.Len(t, forcedGBK.Events, 2)
	require.Equal(t, "[23:54:02] [Server thread/INFO]: 强制解码", forcedGBK.Events[0].Message)
	require.NotContains(t, forcedGBK.Events[0].Message, "\uFFFD")
	require.Equal(t, "gbk", forcedGBK.Events[0].Fields[FieldSourceCharset])
}

// TestStickyDetectionSurvivesAsciiAndRecoversOnUTF8 守住粘滞判定：
// GBK 源里的纯 ASCII 行必须按同一口径处理；而真 UTF-8 源里的偶发损坏行不得把该源永久锁死。
func TestStickyDetectionSurvivesAsciiAndRecoversOnUTF8(t *testing.T) {
	// 先 GBK 中文（判定为 GB 系），再纯 ASCII 行（不得让判定回退到 UTF-8）。
	gbkLine := gbk(t, "[23:54:02] [Server thread/INFO]: 中文一")
	asciiLine := "[23:54:03] [Server thread/INFO]: plain ascii"
	res := ProcessLines([]string{gbkLine, asciiLine, gbk(t, "[23:54:04] [Server thread/INFO]: 中文二")}, charsetOptions(""))
	require.Len(t, res.Events, 3)
	require.Equal(t, "[23:54:02] [Server thread/INFO]: 中文一", res.Events[0].Message)
	require.Equal(t, "[23:54:04] [Server thread/INFO]: 中文二", res.Events[2].Message)
	require.Equal(t, "gb18030", res.Events[1].Fields[FieldSourceCharset],
		"同一源的纯 ASCII 行沿用源级判定，保持口径一致")

	// 真 UTF-8 源里的损坏行（判定 utf-8）之后，正常中文行不得被按 GB 解码。
	n := New(charsetOptions(""))
	n.Feed("[23:54:02] [Server thread/INFO]: broken \xff\xfe")
	// 同一源的第二行是正常 UTF-8 中文：必须原样保留（损坏行不得把源锁进 GB 系解码）。
	out := n.Feed("[23:54:03] [Server thread/INFO]: 正常中文")
	require.Len(t, out, 1, "第二行的事件头应闭合第一行")
	var joined string
	for _, e := range out {
		joined += e.Message + "\n"
	}
	ev, ok := n.FlushPartial()
	require.True(t, ok)
	joined += ev.Message
	require.Contains(t, joined, "正常中文", "损坏行不得把源锁进 GB 系解码")
	require.NotContains(t, joined, "姝ｅ父", "正常中文被按 GB 解码时会出现这类错字")
}

// TestParseCharsetRejectsUnknownValues 守住「配置错误必须显式暴露」。
func TestParseCharsetRejectsUnknownValues(t *testing.T) {
	for _, ok := range []string{"", "auto", "AUTO", "utf-8", "UTF8", "gbk", "GB2312", "cp936", "gb18030"} {
		require.True(t, IsValidCharset(ok), "应接受 %q", ok)
	}
	require.False(t, IsValidCharset("big5"))
	require.False(t, IsValidCharset("shift_jis"))
}
