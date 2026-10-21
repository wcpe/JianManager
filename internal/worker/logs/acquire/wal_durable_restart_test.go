package acquire

// 本文件钉住「重启后 durable_position 不得被压缩回退」（2026-10-22 真机缺陷）。
//
// 现场形态（inst:321/file，生产实测）：
//   - 账本 position: read=durable=reclaim=4592494（三水位相等，无滞后）；
//   - 但每 10 秒稳定复现一条缺口：
//     `WAL_COMMIT_FAILED … durable_position must be monotonic within generation:
//      4581644 < 4592494`；
//   - 缺口随之被「登记 → 消解 → 再登记」的活循环反复重建，同区间累计 65 条
//     （id 0..64，前 64 条 resolution 都是 `verified published projection`）；
//   - 期间该源被标成「不可分类缺口阻塞（无自动出路，需人工裁定或重置）」，
//     积压不推进，最坏会让采集停摆。
//
// 根因不在 ledger 的单调性校验（那是正确的防线），而在 WAL 侧：
// `lastDurableEnd` **只是内存字段**，重启后由「当前 WAL 条目」重建；而重启时那些
// 条目往往已被 reclaim 回收 ⇒ 进程内计数从小值起步，却拿去推进一个远更靠前的账本
// 水位。语义上这根本不是错误：durable 是「已耐久到至少这个位置」的**下界**，
// 账本更靠前只能说明这些字节早已耐久（reclaim 的前提就是「已证明安全另存」）。
//
// 修法：Commit 推进前先取账本的当前 durable 兜底（取大），推进量永远单调。
//
// 变异验证：删掉 `if p, ok := w.led.Positions(...); ok && p.Durable > maxEnd` 这段兜底
// ⇒ 本用例在 Commit 处返回 monotonic 错误而转红。
import (
	"fmt"
	"testing"

	"github.com/wcpe/JianManager/internal/worker/logs/logtypes"

	"github.com/wcpe/JianManager/internal/worker/logs/ledger"
)

// makeWALTestEvents 造 n 个 record 递增的事件（位置 1..n*10）。
// 不复用其它用例的构造：那些是包内共享的夹具，语义与位置假设各不相同，
// 复用会让本用例的前提依赖别的用例的改动。
func makeWALTestEvents(n int) []logtypes.Event {
	events := make([]logtypes.Event, 0, n)
	for i := 0; i < n; i++ {
		events = append(events, logtypes.Event{
			EventID:       fmt.Sprintf("ev-%d", i),
			EventTimeUTC:  "2026-10-20T00:00:00Z",
			IngestTimeUTC: "2026-10-20T00:00:00Z",
			Level:         "INFO",
			Stream:        "stdout",
			Message:       fmt.Sprintf("line %d", i),
			Record:        logtypes.RecordRange{Start: uint64(1 + i*10), End: uint64(10 + i*10)},
			CanonicalHash: logtypes.CanonicalContentHash("2026-10-20T00:00:00Z", "INFO", "stdout", fmt.Sprintf("line %d", i)),
		})
	}
	return events
}

func TestWALCommitDoesNotRewindDurableAfterRestart(t *testing.T) {
	key := testKey("src-restart", "g1")

	// 第一生命：写入并提交若干事件，durable 推进到末端。
	led := ledger.New()
	wal := NewWAL(led, key)
	first := makeWALTestEvents(3)
	if err := wal.Append(first...); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := wal.Commit(); err != nil {
		t.Fatalf("首次 commit 必须成功：%v", err)
	}
	before, ok := led.Positions(key)
	if !ok {
		t.Fatal("账本缺少该源的水位记录")
	}
	if before.Durable == 0 {
		t.Fatal("夹具前提：首次提交后 durable 必须已推进")
	}

	// 模拟「重启 + WAL 条目已被 reclaim 回收」：全新 WAL 实例，进程内 lastDurableEnd
	// 从 0 起步，但它面对的账本 durable 仍是 before.Durable。
	restarted := NewWAL(led, key)

	// 重启后只写入**位置更靠前**的一小批事件（这正是回收后重读出现的形态）。
	early := makeWALTestEvents(1)
	for i := range early {
		early[i].Record.Start = 1
		early[i].Record.End = 10
	}
	if err := restarted.Append(early...); err != nil {
		t.Fatalf("append: %v", err)
	}
	err := restarted.Commit()
	if err != nil {
		t.Fatalf("重启后提交不得因 durable 回退而失败（会变成 WAL_COMMIT_FAILED 缺口活循环）：%v", err)
	}

	after, ok := led.Positions(key)
	if !ok {
		t.Fatal("账本缺少该源的水位记录")
	}
	if after.Durable < before.Durable {
		t.Fatalf("durable 不得回退：提交前 %d，提交后 %d", before.Durable, after.Durable)
	}
}
