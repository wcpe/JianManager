package normalize

import (
	"testing"
	"time"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"
)

// 本文件钉住「无时间戳行的接收时间必须随采集时刻前进」（2026-11-05 真机缺陷修复）。
//
// 缺陷形态：Normalizer 在 New() 里取一次 time.Now() 冻成 n.ingest，而 ingest 包构造
// Normalizer 时从不设置 pipeline.Options.IngestTimeUTC ⇒ 每个源永久携带「登记那一刻」。
// 长驻进程跨过 UTC 午夜后，今天的无时间戳行（RAW 失败行 / 无前缀续行 / 乱码行）被归进
// 启动日的分区，查询侧按天选不到 ⇒ 当天日志页空。
//
// 根因与已修过的「Catalog 记录建在源登记日而非事件日」「中段缺天的静默空」同族：
// 都是用错的基准去归天。

// TestIngestTimeFollowsFeedTime 显式 FeedAt 的时间必须成为 IngestTimeUTC。
//
// 转红方式（实测）：把 buildEvent 里的 n.ingestAt() 改回 n.ingest（冻结值），
// 本用例会以「接收时间应随喂入时刻前进」变红。
func TestIngestTimeFollowsFeedTime(t *testing.T) {
	n := New(Options{Source: logtypes.SourceIdentity{LogSourceID: "inst:1", SourceGeneration: "g1"}, Stream: "stdout"})
	// 两天后的一个时刻：模拟长驻进程跨天。
	later := time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC)
	events := n.FeedAt("这是一行没有时间戳的原始日志", later)
	if len(events) == 0 {
		// 单行不会立刻闭合（可能等下一行边界），显式 flush 拿到它。
		if ev, ok := n.FlushPartial(); ok {
			events = append(events, ev)
		}
	}
	if len(events) == 0 {
		t.Fatal("应产出至少一条事件")
	}
	got := events[0].IngestTimeUTC
	if got == "" {
		t.Fatal("IngestTimeUTC 不应为空（下游 eventUTCDay 依赖它归天）")
	}
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("IngestTimeUTC 必须是 RFC3339Nano，实测 %q: %v", got, err)
	}
	if !parsed.Equal(later) {
		t.Fatalf("接收时间应随喂入时刻前进到 %s，实测 %s（冻结在构造时刻就是本缺陷）",
			later.Format(time.RFC3339Nano), got)
	}
	if day := parsed.UTC().Format("2006-01-02"); day != "2026-11-05" {
		t.Fatalf("接收时间应归到喂入那天 2026-11-05，实测 %s", day)
	}
}

// TestIngestTimeAdvancesAcrossDaysWithoutFeedAt 不传 FeedAt 时也必须「取此刻」，
// 不能是构造时刻的常量。
//
// 这条是缺陷的核心：生产路径（pipeline.pollLocked）不传 at 也不设 opts.IngestTimeUTC，
// 所以「未显式指定时用 time.Now()」这条兜底就是唯一生效的分支。
//
// 同时钉住第二件事：**光传 IngestTimeUTC 不足以开启固定时间轴**——必须显式
// `FixedIngestTime: true`。否则任何人「顺手」把 opts.IngestTimeUTC 接上（比如在
// pipeline.New 里填 time.Now()），这个缺陷就会以另一种写法原样复发。
func TestIngestTimeAdvancesAcrossDaysWithoutFeedAt(t *testing.T) {
	// 构造时显式给一个「两天前」的 IngestTimeUTC，模拟长期运行的 Normalizer 持有的旧值。
	stale := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	n := New(Options{Source: logtypes.SourceIdentity{LogSourceID: "inst:1", SourceGeneration: "g1"}, IngestTimeUTC: stale})
	var events []logtypes.Event
	events = append(events, n.Feed("无时间戳的一行")...)
	if ev, ok := n.FlushPartial(); ok {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("应产出至少一条事件")
	}
	parsed, err := time.Parse(time.RFC3339Nano, events[0].IngestTimeUTC)
	if err != nil {
		t.Fatalf("IngestTimeUTC 非法 %q: %v", events[0].IngestTimeUTC, err)
	}
	if time.Since(parsed) > time.Hour {
		t.Fatalf("IngestTimeUTC 应接近此刻，实测 %s（距现在 %v）——"+
			"沿用构造时的旧值会把今天的行归进旧日的分区", events[0].IngestTimeUTC, time.Since(parsed))
	}
}

// TestIngestTimeKeepsSemanticTimeUntouched 有语义时间的行不受影响（零行为变更）。
func TestIngestTimeKeepsSemanticTimeUntouched(t *testing.T) {
	n := New(Options{Source: logtypes.SourceIdentity{LogSourceID: "inst:1", SourceGeneration: "g1"}})
	var events []logtypes.Event
	events = append(events, n.Feed("[12:00:00] [Server thread/INFO]: hello")...)
	if ev, ok := n.FlushPartial(); ok {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("应产出至少一条事件")
	}
	if events[0].EventTimeUTC == "" {
		t.Fatal("带时间戳的行 EventTimeUTC 必须非空（本次修复不应影响它）")
	}
}

// TestFixedIngestTimeHonored 只有显式 FixedIngestTime 时才固定时间轴（测试/重放用）。
//
// 这是上一条的配对断言：放宽的那一半必须仍然可用，否则重放无法复现。
func TestFixedIngestTimeHonored(t *testing.T) {
	fixed := "2026-01-02T03:04:05Z"
	n := New(Options{
		Source:          logtypes.SourceIdentity{LogSourceID: "inst:1", SourceGeneration: "g1"},
		IngestTimeUTC:   fixed,
		FixedIngestTime: true,
	})
	var events []logtypes.Event
	events = append(events, n.Feed("无时间戳的一行")...)
	if ev, ok := n.FlushPartial(); ok {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("应产出至少一条事件")
	}
	if events[0].IngestTimeUTC != fixed {
		t.Fatalf("FixedIngestTime 为 true 时必须用 %s，实测 %s", fixed, events[0].IngestTimeUTC)
	}
}
