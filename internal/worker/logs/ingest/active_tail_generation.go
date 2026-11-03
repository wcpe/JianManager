package ingest

import (
	"github.com/wcpe/JianManager/internal/worker/logs/catalog"
)

// 活跃尾代次资格仅在本进程完整写入、核验、发布后建立；不是可跨重启继承的许可。
//
// ## 2026-11-03：只剩类型定义，无任何读者
//
// 原有两个消费者（`claimActiveTailGeneration` / `uncertainPublishedTailNeedsReplacement`）
// 均已删除——它们在非测试代码里没有任何调用者（投递路径简化后不再有「活跃追加资格」这个概念）。
// 而写入方 `rememberActiveTailGeneration` 也一并删除：写一个**无人读取**的 map
// 只会在每次投递时白拿 `publishMu` + `m.mu` 两把锁。
//
// 保留类型与 `Manager.activeTails` 字段的理由：一批已 `t.Skip` 的回归用例仍引用它们作为
// 「失去的对象」的占位（见 ingest_six_fix_* 的各条 Skip 说明）。
type activeTailReceipt struct {
	day        string
	generation string
	closed     uint64
	authority  *catalog.Record
	token      string
}
