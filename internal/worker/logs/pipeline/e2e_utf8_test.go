package pipeline

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
	"github.com/wcpe/JianManager/internal/worker/logs/normalize"
)

// 验证：非法 UTF-8 正文经 canonical hash 承诺后，投递 JSON 是否改变了内容。
func TestInvalidUTF8RoundTripSelfCheck(t *testing.T) {
	b := NewNormalizeBoundary(normalize.Options{
		Source: logtypes.SourceIdentity{LogSourceID: "s", SourceGeneration: "g1"},
		Stream: "stdout",
	})
	// 含非法字节的日志行（模拟损坏编码）
	raw := []byte("[12:00:00] [Server thread/INFO]: ")
	raw = append(raw, 0xff, 0xfe)
	raw = append(raw, []byte(" broken")...)
	b.Feed(raw, 0, uint64(len(raw)))
	evs := b.DrainEvents()
	if len(evs) == 0 {
		// 单行是未闭合尾行：用 FlushPartial 关闭后再取。
		b.FlushPartial()
		evs = b.DrainEvents()
	}
	if len(evs) == 0 {
		t.Fatal("未产出事件")
	}
	ev := evs[0]
	t.Logf("原文 message: %q", ev.Message)
	t.Logf("canonical hash: %s", ev.CanonicalHash)

	// 模拟投递：JSON 序列化（ingest 用 json.Marshal 逐行写入）
	line, _ := json.Marshal(map[string]string{"_msg": ev.Message, "canonical_content_hash": ev.CanonicalHash})
	t.Logf("投递 JSON: %s", line)

	var back map[string]string
	_ = json.Unmarshal(line, &back)
	t.Logf("VL 侧回读 _msg: %q", back["_msg"])

	// 重算 hash：用 VL 侧收到的正文
	recomputed := logtypes.CanonicalContentHash(ev.EventTimeUTC, ev.Level, ev.Stream, back["_msg"])
	ok := recomputed == ev.CanonicalHash
	t.Logf("用 VL 侧正文重算 hash 是否等于承诺值: %v", ok)
	if !ok {
		t.Errorf("✗ 内容完整性断裂：承诺 hash=%s，按 VL 实际正文重算=%s", ev.CanonicalHash, recomputed)
	}
	// 非法字节在归一化阶段即已净化，故含 U+FFFD 是**预期**行为；
	// 关键要求是净化事实被标记（否则调用方会把改变过的正文当作原文一致）。
	if strings.Contains(back["_msg"], "\ufffd") {
		if ev.Fields[normalize.FieldEncodingSanitized] != "true" {
			t.Errorf("✗ 正文含替换字符但未标记 encoding_sanitized，内容变更不可审计")
		} else {
			t.Logf("✓ 正文含替换字符且已标记 encoding_sanitized（预期行为）")
		}
	}
}
