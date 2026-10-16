package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSegmentRotatedTextIsDistinctKind 锁定纯文本轮转归档的形态取值：它是**新形态**，
// 不得与既有任何形态取值相同。
//
// 为什么不复用 SegmentGzip：语义不符会让统计与诊断失真——账本分段是「这段字节从哪来」的唯一
// 判据（形态决定它是压缩归档还是明文轮转），把 proxy.log.N 记成 gzip 之后，排障时无法回答
// 「这个源到底有没有 gzip 归档」，而形态一旦记错，后续任何按形态判定的逻辑都跟着错。
//
// 为什么不能复用 SegmentRotated：`rotated` 是 **FileTailer 自己**登记的「活动文件被轮转后的
// 明文分段」（生命周期由 tailer 管、可被后续轮次继续扩展），而本形态是**归档导入器**登记的
// 「已收尾的历史轮转文件」。两者混同会让导入器的已导入判定与 tailer 的分段推进互相覆盖。
func TestSegmentRotatedTextIsDistinctKind(t *testing.T) {
	for _, other := range []SegmentKind{SegmentLive, SegmentRotated, SegmentGzip, SegmentStdio} {
		require.NotEqual(t, other, SegmentRotatedText,
			"纯文本轮转归档必须是与 %q 不同的形态（复用会让统计与诊断失真）", other)
	}
	require.Equal(t, "rotated_text", string(SegmentRotatedText),
		"取值即持久化契约：索引/账本里已写入的取值不得漂移（漂移会让历史分段读不出来）")
}

// TestIsArchiveSegmentKindCoversArchiveFormats 锁定「归档类形态」集合的单一真源。
//
// 为什么必须有这个判定：归档的「已导入」判定分散在两处（导入器自己的跳过检查、ingest 的
// 待导入集合发现），它们都按**形态**过滤账本分段。新增一种归档形态时若只改一处，表现是
// 「新形态被反复重导」这种无声失效——归档每轮被重新发现、重新解析，而账本上它明明已经导完。
// 故形态集合只在这里定义一次，各处引用它。
//
// 变异验证（实测）：把 SegmentRotatedText 从 true 分支删掉（等价于「新增 kind 不参与归档类判定」），
// 本用例立即转红；同时 acquire 的 TestImportTextProducesSameEventsAsGzip /
// TestImportTextIsBoundedAndResumes 转红（形态判定缺失 ⇒ MarkImported/markFailed 把纯文本
// 归档记成 gzip）。完整记录见 acquire/text_rotated_archive_test.go 的文件头。
func TestIsArchiveSegmentKindCoversArchiveFormats(t *testing.T) {
	require.True(t, IsArchiveSegmentKind(SegmentGzip), "gzip 压缩归档属归档类形态")
	require.True(t, IsArchiveSegmentKind(SegmentRotatedText), "纯文本轮转归档属归档类形态")

	// 非归档形态（含未知取值与空值）：默认拒绝。空值会出现在「分段不存在」的零值场景，
	// 它绝不能被当成归档——否则任何零值分段都会被当成「已导入的归档」。
	for _, kind := range []SegmentKind{SegmentLive, SegmentRotated, SegmentStdio, "", "future_kind"} {
		require.False(t, IsArchiveSegmentKind(kind),
			"形态 %q 不是归档：它由别的链路登记（或根本不存在），不得参与归档的已导入判定", kind)
	}
}
