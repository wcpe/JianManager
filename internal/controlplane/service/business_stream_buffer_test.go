package service

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖 FR-522 §4 环形缓冲的核心性质：写入、溢出丢最旧、since 补读、gap 判定、时间窗老化。
// 全部为纯内存用例（不依赖 DB / 网络），便于在高频回归中反复跑。

// mkFrame 造一条最简业务帧（payload 为 JSON 对象）。
func mkFrame(domain, topic string) *BusinessFrame {
	return &BusinessFrame{
		Node:       "node-main",
		Instance:   5,
		Domain:     domain,
		Topic:      topic,
		DedupKey:   "e-" + topic,
		OccurredAt: 1730000000,
		Payload:    json.RawMessage(`{"k":1}`),
	}
}

// TestBusinessStreamBuffer_AppendAssignsMonotonicSeq 写入即分配严格递增 seq，且 id 与 seq 一致。
func TestBusinessStreamBuffer_AppendAssignsMonotonicSeq(t *testing.T) {
	buf := NewBusinessStreamBuffer(0, time.Minute)
	for i := 1; i <= 3; i++ {
		if got := buf.Append(mkFrame("activity", "activity.a")); got != int64(i) {
			t.Fatalf("第 %d 次 Append seq=%d，期望 %d", i, got, i)
		}
	}
	if buf.NewestSeq() != 3 || buf.OldestSeq() != 1 || buf.Size() != 3 {
		t.Fatalf("边界不符: newest=%d oldest=%d size=%d", buf.NewestSeq(), buf.OldestSeq(), buf.Size())
	}
	// SSE 帧的 id 必须等于 seq（FR-522 §8 验收 6）。
	frames, gap := buf.Since(0)
	if gap {
		t.Fatal("未溢出时不应有 gap")
	}
	if len(frames) != 3 {
		t.Fatalf("Since(0) 返回 %d 帧，期望 3", len(frames))
	}
	for _, f := range frames {
		want := "id: " + fmtInt64(f.Seq) + "\nevent: business.event\ndata: "
		if !strings.HasPrefix(string(f.Bytes()), want) {
			t.Fatalf("SSE 帧头不符，实得 %q，期望前缀 %q", string(f.Bytes()), want)
		}
	}
}

// fmtInt64 是测试内的最小整数格式化（避免与包内既有 itoa 辅助重名）。
func fmtInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}

// TestBusinessStreamBuffer_CapacityEvictionDropsOldest 容量打满丢最旧，dropped 递增，最旧 seq 前移。
func TestBusinessStreamBuffer_CapacityEvictionDropsOldest(t *testing.T) {
	buf := NewBusinessStreamBuffer(5, time.Minute)
	for i := 0; i < 8; i++ {
		buf.Append(mkFrame("activity", "activity.a"))
	}
	if buf.Size() != 5 {
		t.Fatalf("容量应为 5，实得 %d", buf.Size())
	}
	if buf.OldestSeq() != 4 || buf.NewestSeq() != 8 {
		t.Fatalf("溢出后边界应为 [4,8]，实得 [%d,%d]", buf.OldestSeq(), buf.NewestSeq())
	}
	if buf.Dropped() != 3 {
		t.Fatalf("dropped 应为 3，实得 %d", buf.Dropped())
	}
	frames, gap := buf.Since(0)
	if !gap {
		t.Fatal("位点 0 早于窗口起点，必须判定 gap")
	}
	if len(frames) != 5 || frames[0].Seq != 4 || frames[4].Seq != 8 {
		t.Fatalf("窗口内容不符: len=%d 首帧=%d 末帧=%d", len(frames), frames[0].Seq, frames[4].Seq)
	}
}

// TestBusinessStreamBuffer_SinceWindowAndGap 覆盖 §3.3 补读语义表的三条分支。
func TestBusinessStreamBuffer_SinceWindowAndGap(t *testing.T) {
	buf := NewBusinessStreamBuffer(100, time.Minute)
	for i := 0; i < 6; i++ {
		buf.Append(mkFrame("activity", "activity.a"))
	}

	// ① 位点在窗口内：从该位点之后补发，无 gap。
	frames, gap := buf.Since(4)
	if gap {
		t.Fatal("窗口内位点不应判定 gap")
	}
	if len(frames) != 2 || frames[0].Seq != 5 || frames[1].Seq != 6 {
		t.Fatalf("窗口内补读不符: %v", seqsOf(frames))
	}

	// ② 位点等于最新：无补发。
	if frames, gap := buf.Since(6); gap || len(frames) != 0 {
		t.Fatalf("位点等于最新时应无补发，实得 gap=%v len=%d", gap, len(frames))
	}

	// ③ 位点超前（异常）：无补发、不 panic。
	if frames, gap := buf.Since(99); gap || len(frames) != 0 {
		t.Fatalf("超前位点应无补发，实得 gap=%v len=%d", gap, len(frames))
	}

	// ④ 空位点（0，未带 Last-Event-ID 时的等价处理）：整窗返回。
	if frames, gap := buf.Since(0); gap || len(frames) != 6 {
		t.Fatalf("位点 0 应返回整窗，实得 gap=%v len=%d", gap, len(frames))
	}
}

// TestBusinessStreamBuffer_TimeWindowAgesOut 时间窗到期后旧帧不可补读，且判定 gap。
func TestBusinessStreamBuffer_TimeWindowAgesOut(t *testing.T) {
	now := time.Unix(1730000000, 0)
	buf := NewBusinessStreamBuffer(100, 30*time.Second)
	buf.now = func() time.Time { return now }

	buf.Append(mkFrame("activity", "activity.old"))
	buf.Append(mkFrame("activity", "activity.new"))
	if buf.OldestSeq() != 1 {
		t.Fatalf("窗口内最旧应为 1，实得 %d", buf.OldestSeq())
	}

	// 时间推进 31 秒：两帧都超窗。
	now = now.Add(31 * time.Second)
	if buf.Size() != 0 {
		t.Fatalf("超窗后有效条数应为 0，实得 %d", buf.Size())
	}
	if buf.OldestSeq() != 0 {
		t.Fatalf("超窗后 oldestSeq 应为 0，实得 %d", buf.OldestSeq())
	}
	if frames, gap := buf.Since(0); gap || len(frames) != 0 {
		t.Fatalf("超窗后不应返回任何可补读帧，实得 gap=%v len=%d", gap, len(frames))
	}
	// 时间窗老化不计入 dropped（它由 gap_served 体现）。
	if buf.Dropped() != 0 {
		t.Fatalf("时间窗老化不应计入 dropped，实得 %d", buf.Dropped())
	}

	// 新事件写入后窗口从新值重新开始（§8 验收 10 的口径）。
	if seq := buf.Append(mkFrame("activity", "activity.newer")); seq != 3 {
		t.Fatalf("重启位点应从既有 newest 继续（3），实得 %d", seq)
	}
	if buf.OldestSeq() != 3 {
		t.Fatalf("新窗口最旧应为 3，实得 %d", buf.OldestSeq())
	}
}

// TestBusinessStreamBuffer_SinceBoundaryIsConsistentWithSnapshot 补读边界与快照必须同源（防重复下发）。
func TestBusinessStreamBuffer_SinceBoundaryIsConsistentWithSnapshot(t *testing.T) {
	buf := NewBusinessStreamBuffer(10, time.Minute)
	for i := 0; i < 3; i++ {
		buf.Append(mkFrame("activity", "activity.a"))
	}
	frames, gap, boundary := buf.SinceWithBoundary(1)
	if gap || boundary != 3 || len(frames) != 2 {
		t.Fatalf("边界快照不符: gap=%v boundary=%d len=%d", gap, boundary, len(frames))
	}
	// 边界之后新写入的帧 seq 必须严格大于 boundary。
	if seq := buf.Append(mkFrame("activity", "activity.b")); seq <= boundary {
		t.Fatalf("边界后新帧 seq=%d 应大于 boundary=%d", seq, boundary)
	}
}

// TestBusinessStreamBuffer_PayloadPassThroughVerbatim 载荷原样透传（不解析、不改写）。
func TestBusinessStreamBuffer_PayloadPassThroughVerbatim(t *testing.T) {
	buf := NewBusinessStreamBuffer(10, time.Minute)
	payload := `{"nested":{"a":[1,2,3]},"unicode":"活动","n":1.5}`
	frame := mkFrame("activity", "activity.a")
	frame.Payload = json.RawMessage(payload)
	buf.Append(frame)

	frames, _ := buf.Since(0)
	if len(frames) != 1 {
		t.Fatalf("应返回 1 帧，实得 %d", len(frames))
	}
	dataLine := extractDataLine(string(frames[0].Bytes()))
	if !strings.Contains(dataLine, `"payload":`+payload) {
		t.Fatalf("载荷未原样透传，实得 data=%s", dataLine)
	}
}

// TestBusinessStreamBuffer_InvalidPayloadDegradesToJSONString 坏载荷降级为 JSON 字符串（保证 data 段合法）。
func TestBusinessStreamBuffer_InvalidPayloadDegradesToJSONString(t *testing.T) {
	buf := NewBusinessStreamBuffer(10, time.Minute)
	frame := mkFrame("activity", "activity.a")
	frame.Payload = json.RawMessage(`not-json`)
	buf.Append(frame)

	frames, _ := buf.Since(0)
	dataLine := extractDataLine(string(frames[0].Bytes()))
	if !json.Valid([]byte(dataLine)) {
		t.Fatalf("降级后 data 段必须仍是合法 JSON，实得 %s", dataLine)
	}
	var decoded struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal([]byte(dataLine), &decoded); err != nil {
		t.Fatalf("解析降级载荷失败: %v", err)
	}
	if decoded.Payload != "not-json" {
		t.Fatalf("坏载荷内容不得丢失，实得 %q", decoded.Payload)
	}
}

// TestBusinessStreamBuffer_ConcurrentAppendIsRaceFree 并发写入 seq 不重复（配合 -race 更有意义）。
func TestBusinessStreamBuffer_ConcurrentAppendIsRaceFree(t *testing.T) {
	buf := NewBusinessStreamBuffer(1000, time.Minute)
	const writers, perWriter = 8, 100
	var wg sync.WaitGroup
	seen := make(chan int64, writers*perWriter)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				seen <- buf.Append(mkFrame("activity", "activity.a"))
			}
		}()
	}
	wg.Wait()
	close(seen)

	unique := make(map[int64]struct{}, writers*perWriter)
	for seq := range seen {
		if _, dup := unique[seq]; dup {
			t.Fatalf("seq %d 被分配两次", seq)
		}
		unique[seq] = struct{}{}
	}
	if len(unique) != writers*perWriter {
		t.Fatalf("应分配 %d 个不同 seq，实得 %d", writers*perWriter, len(unique))
	}
	if buf.NewestSeq() != int64(writers*perWriter) {
		t.Fatalf("newestSeq 应为 %d，实得 %d", writers*perWriter, buf.NewestSeq())
	}
}

// seqsOf 提取帧位点列表（断言用）。
func seqsOf(frames []*BusinessFrame) []int64 {
	out := make([]int64, len(frames))
	for i, f := range frames {
		out[i] = f.Seq
	}
	return out
}

// extractDataLine 从 SSE 帧里取出 data 段内容（断言用）。
func extractDataLine(frame string) string {
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: ")
		}
	}
	return ""
}
