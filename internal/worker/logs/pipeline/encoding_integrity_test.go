package pipeline

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

// FR-474 规格 §5 要求「损坏编码可恢复」「解析失败保留原文」。
//
// 本文件固化损坏编码（非法 UTF-8）的不变量：
//  1. 正文在归一化阶段即净化，使 canonical_content_hash 与投递 JSON 往返后的正文**一致**
//     （若不在此时净化，JSON 编码会把非法字节换成 U+FFFD，导致 hash 承诺的内容与
//     VL 实际存储的内容不是同一份——这会让「按 hash 校验完整性」失去意义）；
//  2. 净化事实必须可审计：置 encoding_sanitized 字段，调用方不得把该事件当作原文一致；
//  3. 合法 UTF-8 不受影响（零行为变化）。

// feedOne 喂入一行原始字节并取回事件（单行为未闭合尾行，需 FlushPartial 关闭）。
func feedOne(t *testing.T, raw []byte) logtypes.Event {
	t.Helper()
	b := NewNormalizeBoundary(normalize.Options{
		Source: logtypes.SourceIdentity{LogSourceID: "src-enc", SourceGeneration: "g1"},
		Stream: "stdout",
	})
	b.Feed(raw, 0, uint64(len(raw)))
	evs := b.DrainEvents()
	if len(evs) == 0 {
		b.FlushPartial()
		evs = b.DrainEvents()
	}
	require.Len(t, evs, 1, "应产出恰好一条事件")
	return evs[0]
}

// 非法 UTF-8 经投递 JSON 往返后，承诺的 hash 必须仍能用实际正文重算出来。
func TestInvalidUTF8HashMatchesDeliveredBody(t *testing.T) {
	raw := append([]byte("[12:00:00] [Server thread/INFO]: "), 0xff, 0xfe)
	raw = append(raw, []byte(" broken")...)
	ev := feedOne(t, raw)

	// 模拟 ingest 的投递编码（逐行 JSON）。
	line, err := json.Marshal(map[string]string{
		"_msg":                   ev.Message,
		"canonical_content_hash": ev.CanonicalHash,
	})
	require.NoError(t, err)
	var back map[string]string
	require.NoError(t, json.Unmarshal(line, &back))

	recomputed := logtypes.CanonicalContentHash(ev.EventTimeUTC, ev.Level, ev.Stream, back["_msg"])
	require.Equal(t, ev.CanonicalHash, recomputed,
		"承诺的 canonical hash 必须与 VL 实际收到的正文一致，否则内容校验失去意义")
	require.Equal(t, ev.Message, back["_msg"], "投递往返不得改变正文")
}

// 净化必须留下可审计标记，且正文确实不同于源文件原始字节。
func TestInvalidUTF8IsFlaggedAsSanitized(t *testing.T) {
	raw := append([]byte("[12:00:00] [Server thread/INFO]: "), 0xff, 0xfe)
	raw = append(raw, []byte(" broken")...)
	ev := feedOne(t, raw)

	require.Equal(t, "true", ev.Fields[normalize.FieldEncodingSanitized],
		"含非法字节的正文必须标记为已净化，不得当作原文一致")
	require.Contains(t, ev.Message, "\uFFFD", "非法字节应替换为 U+FFFD")
	require.NotEqual(t, string(raw), ev.Message, "净化后正文不得等于原始字节")
}

// 合法 UTF-8 不得被标记、不得被改动（零行为变化）。
func TestValidUTF8IsUntouched(t *testing.T) {
	raw := []byte("[12:00:00] [Server thread/INFO]: 正常中文与 emoji 🚀 ok")
	ev := feedOne(t, raw)

	require.NotContains(t, ev.Fields, normalize.FieldEncodingSanitized,
		"合法 UTF-8 不得被标记为已净化")
	require.True(t, strings.HasSuffix(ev.Message, "正常中文与 emoji 🚀 ok"),
		"合法正文必须原样保留，实得 %q", ev.Message)
}
