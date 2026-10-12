package logcoord

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 红绿（145 族 / f2cfbd5b）：读面双键去重 ＋ 重复降级，三态直测。
//
//  ① 两代次持同 id 同 hash（跨代次并集残留，实测 145 同页 91/500）⇒
//     去重后 uniq==items ✓，且 duplicated=true ⇒ 调用方把质量从 exact 降级 unresolved ✓（现行红 ✗）；
//  ② 守门：同 id **不同 hash**（内容不同）⇒ 必须保留 ✓，不得误删；
//  ③ 无重复 ⇒ 不做任何改动、不回报 duplicated ✓。
func TestDedupeMergedRowsDoubleKey(t *testing.T) {
	// ① 同 id 同 hash：跨代次并集的两条 ⇒ 去一保一，且回报重复。
	dup := []Event{
		{EventID: "e1", CanonicalHash: "h1", SourceGeneration: "projection-1"},
		{EventID: "e2", CanonicalHash: "h2", SourceGeneration: "projection-1"},
		{EventID: "e1", CanonicalHash: "h1", SourceGeneration: "projection-2"}, // 跨代次残留
	}
	out, duplicated := dedupeMergedRows(dup)
	require.True(t, duplicated, "同 EventID 多行必须回报（供质量降级 unresolved）✗")
	require.Len(t, out, 2, "同 id 同 hash 只保留一份（保留排序键最靠前者）✓")
	require.Equal(t, "projection-1", out[0].SourceGeneration, "保留先出现者 ✓")
	uniq := map[string]bool{}
	for _, e := range out {
		uniq[e.EventID] = true
	}
	require.Equal(t, len(out), len(uniq), "去重后 uniq==items ✓")

	// ② 守门：同 id 不同 hash（真·不同内容）⇒ 不得被删。
	diff := []Event{
		{EventID: "e9", CanonicalHash: "ha"},
		{EventID: "e9", CanonicalHash: "hb"},
	}
	out2, dup2 := dedupeMergedRows(diff)
	require.Len(t, out2, 2, "同 id 不同内容必须保留（不得误删）✗")
	require.True(t, dup2, "同 id 多行仍应回报（供质量降级）✓")

	// ③ 无重复：原样返回、不回报。
	clean := []Event{{EventID: "a", CanonicalHash: "1"}, {EventID: "b", CanonicalHash: "2"}}
	out3, dup3 := dedupeMergedRows(clean)
	require.False(t, dup3, "无重复不得回报 ✓")
	require.Equal(t, clean, out3)
}
